package extendible

import (
	"errors"
	"github.com/dbms-go/v2/dbms/lib/index/common"
	"github.com/dbms-go/v2/dbms/lib/storage"
	"os"
	"path/filepath"
	"testing"
)

func TestPersistsAcrossCloseOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hash.idx")
	idx, err := Create(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 500; i++ {
		if err := idx.Insert(int64(i), storage.RID{PageID: uint32(i / 8), SlotID: uint16(i % 8)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := idx.Insert(int64(42), storage.RID{PageID: 999, SlotID: 7}); err != nil {
		t.Fatal(err)
	}
	if idx.GlobalDepth() <= 1 {
		t.Fatalf("expected splits, depth=%d", idx.GlobalDepth())
	}
	if err := idx.Validate(); err != nil {
		t.Fatal(err)
	}
	st, err := idx.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.FileBytes == 0 {
		t.Fatal("expected file bytes")
	}
	if err := idx.Close(); err != nil {
		t.Fatal(err)
	}

	idx, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	if err := idx.Validate(); err != nil {
		t.Fatal(err)
	}
	got, err := idx.Search(int64(42))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("key 42 got %d rids: %#v", len(got), got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestDeletePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hash.idx")
	idx, err := Create(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	rid := storage.RID{PageID: 1, SlotID: 2}
	if err := idx.Insert("alpha", rid); err != nil {
		t.Fatal(err)
	}
	ok, err := idx.Delete("alpha", rid)
	if err != nil || !ok {
		t.Fatalf("delete ok=%v err=%v", ok, err)
	}
	if err := idx.Close(); err != nil {
		t.Fatal(err)
	}
	idx, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	got, err := idx.Search("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %#v", got)
	}
}

func TestRangeStillUnsupported(t *testing.T) {
	idx := New(4)
	defer idx.Close()
	if idx.SupportsRange() {
		t.Fatal("hash must not support range")
	}
	_, err := idx.RangeSearch(1, 10)
	if !errors.Is(err, common.ErrRangeNotSupported) {
		t.Fatalf("got %v", err)
	}
}

func TestDirectoryCanSpanMultiplePages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large-dir.idx")
	idx, err := Create(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3000; i++ {
		if err := idx.Insert(int64(i), storage.RID{PageID: uint32(i), SlotID: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if idx.GlobalDepth() < 10 {
		t.Fatalf("expected large directory, depth=%d", idx.GlobalDepth())
	}
	if err := idx.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := idx.Close(); err != nil {
		t.Fatal(err)
	}
	idx, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	if err := idx.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, k := range []int64{0, 17, 999, 2999} {
		got, err := idx.Search(k)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].PageID != uint32(k) {
			t.Fatalf("key %d got %#v", k, got)
		}
	}
}

func TestBucketCanSpanMultiplePagesWithDuplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large-bucket.idx")
	idx, err := Create(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	const n = 3000
	for i := 0; i < n; i++ {
		if err := idx.Insert("same-key", storage.RID{PageID: uint32(i), SlotID: uint16(i % 100)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := idx.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := idx.Close(); err != nil {
		t.Fatal(err)
	}
	idx, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	got, err := idx.Search("same-key")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("expected %d rids, got %d", n, len(got))
	}
}
