package sequential

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"sort"
	"testing"
)

func TestRIDStableAcrossReorganize(t *testing.T) {
	dir := t.TempDir()
	s, err := Create(filepath.Join(dir, "main.seq"), filepath.Join(dir, "ovf.seq"), 3, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetOverflowReorgThreshold(1.1)
	rids := map[int64]RecordID{}
	for i := int64(1); i <= 20; i++ {
		rid, err := s.InsertRecord(i, []byte(fmt.Sprintf("v%d", i)))
		if err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
		rids[i] = rid
	}
	if err := s.Reorganize(); err != nil {
		t.Fatal(err)
	}
	for k, rid := range rids {
		got, err := s.Read(rid)
		if err != nil {
			t.Fatalf("Read(%v): %v", rid, err)
		}
		want := fmt.Sprintf("v%d", k)
		if string(got) != want {
			t.Fatalf("RID %v got %q want %q", rid, got, want)
		}
	}
}

func TestDuplicateKeysSupportedViaInsertRecord(t *testing.T) {
	s := newTestSeqFile(t, 2, 16)
	s.SetOverflowReorgThreshold(1.1)
	r1, err := s.InsertRecord(7, []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	r2, err := s.InsertRecord(7, []byte("b"))
	if err != nil {
		t.Fatal(err)
	}
	r3, err := s.InsertRecord(7, []byte("c"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SearchAll(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d duplicates", len(got))
	}
	wantRIDs := []RecordID{r1, r2, r3}
	for i := range got {
		if got[i].RID != wantRIDs[i] {
			t.Fatalf("order mismatch at %d", i)
		}
	}
	if err := s.DeleteRID(r2); err != nil {
		t.Fatal(err)
	}
	got, err = s.SearchAll(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].RID != r1 || got[1].RID != r3 {
		t.Fatalf("after delete: %+v", got)
	}
}

func TestOverflowDeletedNodeDoesNotResurrect(t *testing.T) {
	s := newTestSeqFile(t, 1, 16)
	s.SetReorgThreshold(1.1)
	s.SetOverflowReorgThreshold(1.1)
	if err := s.Insert(1, []byte("one")); err != nil {
		t.Fatal(err)
	}
	oldRID, err := s.InsertRecord(2, []byte("old"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRID(oldRID); err != nil {
		t.Fatal(err)
	}
	newRID, err := s.InsertRecord(2, []byte("new"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SearchAll(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].RID != newRID || string(got[0].Payload) != "new" {
		t.Fatalf("deleted overflow entry resurrected: %+v", got)
	}
	if _, err := s.Read(oldRID); err != ErrNotFound {
		t.Fatalf("old RID err=%v", err)
	}
}

func TestPersistencePreservesRID(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "main.seq")
	ovfPath := filepath.Join(dir, "ovf.seq")
	s, err := Create(mainPath, ovfPath, 2, 16)
	if err != nil {
		t.Fatal(err)
	}
	rid, err := s.InsertRecord(42, []byte("answer"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := Open(mainPath, ovfPath)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := r.Read(rid)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "answer" {
		t.Fatalf("got %q", got)
	}
}

func TestRandomizedSequentialModel(t *testing.T) {
	s := newTestSeqFile(t, 5, 32)
	rng := rand.New(rand.NewSource(12345))
	type modelRec struct {
		rid RecordID
		key int64
		val string
	}
	live := map[RecordID]modelRec{}
	for step := 0; step < 1000; step++ {
		if len(live) == 0 || rng.Intn(100) < 70 {
			key := int64(rng.Intn(40) - 20)
			val := fmt.Sprintf("v-%d", step)
			rid, err := s.InsertRecord(key, []byte(val))
			if err != nil {
				t.Fatalf("step %d insert: %v", step, err)
			}
			live[rid] = modelRec{rid: rid, key: key, val: val}
		} else {
			idx := rng.Intn(len(live))
			var victim RecordID
			i := 0
			for rid := range live {
				if i == idx {
					victim = rid
					break
				}
				i++
			}
			if err := s.DeleteRID(victim); err != nil {
				t.Fatalf("step %d delete: %v", step, err)
			}
			delete(live, victim)
		}
		if step%50 == 0 {
			recs, err := s.ScanRecords()
			if err != nil {
				t.Fatal(err)
			}
			want := make([]modelRec, 0, len(live))
			for _, r := range live {
				want = append(want, r)
			}
			sort.Slice(want, func(i, j int) bool {
				return want[i].key < want[j].key || (want[i].key == want[j].key && want[i].rid < want[j].rid)
			})
			if len(recs) != len(want) {
				t.Fatalf("step %d len=%d want=%d", step, len(recs), len(want))
			}
			for i := range want {
				if recs[i].RID != want[i].rid || recs[i].Key != want[i].key || string(recs[i].Payload) != want[i].val {
					t.Fatalf("step %d mismatch at %d got=%+v want=%+v", step, i, recs[i], want[i])
				}
			}
		}
	}
}
