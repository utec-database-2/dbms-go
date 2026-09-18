package sequential

import (
	"fmt"
	"path/filepath"
	"testing"
)

func newTestSeqFile(t *testing.T, pageCapacity, payloadSize int) *SeqFile {
	t.Helper()
	dir := t.TempDir()
	s, err := Create(filepath.Join(dir, "main.seq"), filepath.Join(dir, "ovf.seq"), pageCapacity, payloadSize)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func payload(s string) []byte { return []byte(s) }

func TestInsertAndSearch(t *testing.T) {
	s := newTestSeqFile(t, 4, 16)

	if err := s.Insert(10, payload("ten")); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, ok, err := s.Search(10)
	if err != nil || !ok {
		t.Fatalf("Search: got=%v ok=%v err=%v", got, ok, err)
	}
	if string(got) != "ten" {
		t.Fatalf("got %q, want %q", got, "ten")
	}
}

func TestSearchMissingKey(t *testing.T) {
	s := newTestSeqFile(t, 4, 16)
	if err := s.Insert(1, payload("one")); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	_, ok, err := s.Search(999)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if ok {
		t.Fatalf("expected key 999 to be missing")
	}
}

func TestInsertRejectsDuplicateKey(t *testing.T) {
	s := newTestSeqFile(t, 4, 16)
	if err := s.Insert(5, payload("a")); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.Insert(5, payload("b")); err != ErrKeyExists {
		t.Fatalf("got err=%v, want ErrKeyExists", err)
	}
}

func TestInsertMaintainsOrderWithinPage(t *testing.T) {
	s := newTestSeqFile(t, 8, 16)

	keys := []int64{5, 1, 9, 3, 7}
	for _, k := range keys {
		if err := s.Insert(k, payload(fmt.Sprintf("v%d", k))); err != nil {
			t.Fatalf("Insert %d: %v", k, err)
		}
	}

	scan, err := s.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := []int64{1, 3, 5, 7, 9}
	if len(scan) != len(want) {
		t.Fatalf("scan length = %d, want %d", len(scan), len(want))
	}
	for i, kv := range scan {
		if kv.Key != want[i] {
			t.Fatalf("scan[%d].Key = %d, want %d", i, kv.Key, want[i])
		}
	}
}

func TestOverflowChainKeepsGlobalOrder(t *testing.T) {
	// pageCapacity=2 fuerza que todo insert extra vaya a la cadena de overflow
	s := newTestSeqFile(t, 2, 16)

	keys := []int64{20, 5, 15, 1, 25, 10, 30}
	for _, k := range keys {
		if err := s.Insert(k, payload(fmt.Sprintf("v%d", k))); err != nil {
			t.Fatalf("Insert %d: %v", k, err)
		}
	}

	scan, err := s.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	prev := int64(-1 << 62)
	for _, kv := range scan {
		if kv.Key < prev {
			t.Fatalf("scan not sorted: %v", scan)
		}
		prev = kv.Key
	}
	if len(scan) != len(keys) {
		t.Fatalf("scan length = %d, want %d", len(scan), len(keys))
	}
	for _, k := range keys {
		got, ok, err := s.Search(k)
		if err != nil || !ok {
			t.Fatalf("Search(%d): ok=%v err=%v", k, ok, err)
		}
		if string(got) != fmt.Sprintf("v%d", k) {
			t.Fatalf("Search(%d) = %q, want %q", k, got, fmt.Sprintf("v%d", k))
		}
	}
}

func TestLazyDelete(t *testing.T) {
	s := newTestSeqFile(t, 4, 16)
	s.SetReorgThreshold(1.1) // disable auto reorg for this test

	for _, k := range []int64{1, 2, 3, 4} {
		if err := s.Insert(k, payload("x")); err != nil {
			t.Fatalf("Insert %d: %v", k, err)
		}
	}
	ok, err := s.Delete(2)
	if err != nil || !ok {
		t.Fatalf("Delete: ok=%v err=%v", ok, err)
	}
	_, ok, err = s.Search(2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if ok {
		t.Fatalf("expected key 2 to be gone after delete")
	}

	ok, err = s.Delete(2)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if ok {
		t.Fatalf("deleting an already-deleted key should return false")
	}

	stats := s.Stats()
	if stats.LiveCount != 3 || stats.DeadCount != 1 {
		t.Fatalf("stats = %+v, want live=3 dead=1", stats)
	}
}

func TestDeleteFromOverflowChain(t *testing.T) {
	s := newTestSeqFile(t, 2, 16)
	s.SetReorgThreshold(1.1)

	for _, k := range []int64{1, 2, 3, 4, 5} {
		if err := s.Insert(k, payload("x")); err != nil {
			t.Fatalf("Insert %d: %v", k, err)
		}
	}
	ok, err := s.Delete(4)
	if err != nil || !ok {
		t.Fatalf("Delete: ok=%v err=%v", ok, err)
	}
	scan, err := s.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, kv := range scan {
		if kv.Key == 4 {
			t.Fatalf("deleted key 4 still present in scan: %v", scan)
		}
	}
	if len(scan) != 4 {
		t.Fatalf("scan length = %d, want 4", len(scan))
	}
}

func TestRangeScan(t *testing.T) {
	s := newTestSeqFile(t, 3, 16)
	for i := int64(1); i <= 20; i++ {
		if err := s.Insert(i, payload(fmt.Sprintf("v%d", i))); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	got, err := s.RangeScan(5, 10)
	if err != nil {
		t.Fatalf("RangeScan: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("range length = %d, want 6", len(got))
	}
	for i, kv := range got {
		if kv.Key != int64(5+i) {
			t.Fatalf("range[%d].Key = %d, want %d", i, kv.Key, 5+i)
		}
	}
}

func TestAutoReorganizationOnThreshold(t *testing.T) {
	s := newTestSeqFile(t, 4, 16)
	s.SetReorgThreshold(0.30)

	for i := int64(1); i <= 10; i++ {
		if err := s.Insert(i, payload(fmt.Sprintf("v%d", i))); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	// eliminar 4 de 10 (40%) supera el umbral del 30% y dispara la reorganización
	for _, k := range []int64{1, 2, 3, 4} {
		if _, err := s.Delete(k); err != nil {
			t.Fatalf("Delete %d: %v", k, err)
		}
	}

	stats := s.Stats()
	if stats.DeadCount != 0 {
		t.Fatalf("expected reorganization to reclaim tombstones, got DeadCount=%d", stats.DeadCount)
	}
	if stats.LiveCount != 6 {
		t.Fatalf("LiveCount = %d, want 6", stats.LiveCount)
	}

	scan, err := s.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := []int64{5, 6, 7, 8, 9, 10}
	if len(scan) != len(want) {
		t.Fatalf("scan length = %d, want %d", len(scan), len(want))
	}
	for i, kv := range scan {
		if kv.Key != want[i] {
			t.Fatalf("scan[%d].Key = %d, want %d", i, kv.Key, want[i])
		}
	}
}

func TestManualReorganizeAfterDeletes(t *testing.T) {
	s := newTestSeqFile(t, 2, 16)
	s.SetReorgThreshold(1.1) // disable auto reorg, we call it manually

	for i := int64(1); i <= 6; i++ {
		if err := s.Insert(i, payload(fmt.Sprintf("v%d", i))); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	for _, k := range []int64{2, 4} {
		if _, err := s.Delete(k); err != nil {
			t.Fatalf("Delete %d: %v", k, err)
		}
	}
	if err := s.Reorganize(); err != nil {
		t.Fatalf("Reorganize: %v", err)
	}

	stats := s.Stats()
	if stats.DeadCount != 0 || stats.LiveCount != 4 {
		t.Fatalf("stats after reorganize = %+v, want live=4 dead=0", stats)
	}

	if err := s.Insert(100, payload("hundred")); err != nil {
		t.Fatalf("Insert after reorganize: %v", err)
	}
	got, ok, err := s.Search(100)
	if err != nil || !ok || string(got) != "hundred" {
		t.Fatalf("Search(100) after reorganize: got=%q ok=%v err=%v", got, ok, err)
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "main.seq")
	ovfPath := filepath.Join(dir, "ovf.seq")

	s, err := Create(mainPath, ovfPath, 3, 16)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for i := int64(1); i <= 9; i++ {
		if err := s.Insert(i, payload(fmt.Sprintf("v%d", i))); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	if _, err := s.Delete(5); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(mainPath, ovfPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reopened.Close()

	_, ok, err := reopened.Search(5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if ok {
		t.Fatalf("deleted key 5 resurfaced after reopen")
	}
	for i := int64(1); i <= 9; i++ {
		if i == 5 {
			continue
		}
		got, ok, err := reopened.Search(i)
		if err != nil || !ok {
			t.Fatalf("Search(%d) after reopen: ok=%v err=%v", i, ok, err)
		}
		if string(got) != fmt.Sprintf("v%d", i) {
			t.Fatalf("Search(%d) after reopen = %q", i, got)
		}
	}

	if err := reopened.Insert(50, payload("fifty")); err != nil {
		t.Fatalf("Insert after reopen: %v", err)
	}
	got, ok, err := reopened.Search(50)
	if err != nil || !ok || string(got) != "fifty" {
		t.Fatalf("Search(50) after reopen: got=%q ok=%v err=%v", got, ok, err)
	}
}

func TestPayloadTooLargeRejected(t *testing.T) {
	s := newTestSeqFile(t, 4, 4)
	if err := s.Insert(1, payload("waytoobig")); err == nil {
		t.Fatalf("expected error inserting a payload larger than payloadSize")
	}
}
