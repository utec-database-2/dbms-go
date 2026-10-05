package extendible

import (
	"errors"
	"testing"

	"github.com/dbms-go/v2/dbms/lib/index/common"
	"github.com/dbms-go/v2/dbms/lib/storage"
)

func TestExtendibleHashSearchOnEmptyIndex(t *testing.T) {
	idx := New(4)
	defer idx.Close()

	rids, err := idx.Search("missing")
	if err != nil {
		t.Fatalf("Search on empty index failed: %v", err)
	}
	if len(rids) != 0 {
		t.Fatalf("expected no results, got %v", rids)
	}
}

func TestExtendibleHashInsertSearchAndDelete(t *testing.T) {
	idx := New(4)
	defer idx.Close()

	rid1 := storage.RID{File: 1, Page: 2, Slot: 3}
	rid2 := storage.RID{File: 1, Page: 2, Slot: 4}
	if err := idx.Insert("ana", rid1); err != nil {
		t.Fatal(err)
	}
	if err := idx.Insert("ana", rid2); err != nil {
		t.Fatal(err)
	}
	if err := idx.Insert("ana", rid1); err != nil {
		t.Fatal(err)
	}

	got, err := idx.Search("ana")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected three stored RIDs, got %v", got)
	}

	deleted, err := idx.Delete("ana", rid1)
	if err != nil || !deleted {
		t.Fatalf("Delete returned deleted=%v err=%v", deleted, err)
	}
	got, err = idx.Search("ana")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != rid2 || got[1] != rid1 {
		t.Fatalf("after delete got %v, expected [%v %v]", got, rid2, rid1)
	}
}

func TestExtendibleHashSplitsAndKeepsEntries(t *testing.T) {
	idx := New(2)
	defer idx.Close()

	for i := 0; i < 100; i++ {
		if err := idx.Insert(i, storage.RID{File: 1, Page: int32(i), Slot: 1}); err != nil {
			t.Fatalf("Insert(%d) failed: %v", i, err)
		}
	}
	if idx.GlobalDepth() <= 1 {
		t.Fatalf("expected directory splits, depth=%d", idx.GlobalDepth())
	}

	for i := 0; i < 100; i++ {
		got, err := idx.Search(i)
		if err != nil {
			t.Fatalf("Search(%d) failed: %v", i, err)
		}
		want := storage.RID{File: 1, Page: int32(i), Slot: 1}
		if len(got) != 1 || got[0] != want {
			t.Fatalf("Search(%d) = %v, expected [%v]", i, got, want)
		}
	}
}

func TestExtendibleHashDoesNotSupportRanges(t *testing.T) {
	idx := New(4)
	defer idx.Close()

	if idx.SupportsRange() {
		t.Fatal("Extendible Hashing must not support range searches")
	}
	_, err := idx.RangeSearch(1, 10)
	if !errors.Is(err, common.ErrRangeNotSupported) {
		t.Fatalf("expected ErrRangeNotSupported, got %v", err)
	}
}
