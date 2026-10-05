package extendible

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dbms-go/v2/dbms/lib/storage"
)

func TestExtendibleHashPersistsAcrossCloseAndOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hash.idx")
	idx, err := Create(path, 4)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 500; i++ {
		rid := storage.RID{File: 1, Page: int32(i / 8), Slot: int32(i % 8)}
		if err := idx.Insert(int64(i), rid); err != nil {
			t.Fatalf("Insert(%d) failed: %v", i, err)
		}
	}
	extra := storage.RID{File: 1, Page: 999, Slot: 7}
	if err := idx.Insert(int64(42), extra); err != nil {
		t.Fatal(err)
	}
	if err := idx.Validate(); err != nil {
		t.Fatalf("invalid index before close: %v", err)
	}
	stats := idx.Stats()
	if stats.FileBytes == 0 {
		t.Fatal("expected a non-empty index file")
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
		t.Fatalf("invalid index after open: %v", err)
	}
	got, err := idx.Search(int64(42))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected two RIDs for key 42, got %v", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("index file disappeared: %v", err)
	}
}

func TestExtendibleHashDeletePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hash.idx")
	idx, err := Create(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	rid := storage.RID{File: 1, Page: 1, Slot: 2}
	if err := idx.Insert("alpha", rid); err != nil {
		t.Fatal(err)
	}
	deleted, err := idx.Delete("alpha", rid)
	if err != nil || !deleted {
		t.Fatalf("Delete returned deleted=%v err=%v", deleted, err)
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
		t.Fatalf("expected deleted key to be absent, got %v", got)
	}
}

func TestExtendibleHashPersistsEncodedKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "binary-keys.idx")
	idx, err := Create(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	rid := storage.RID{File: 1, Page: 2, Slot: 3}
	if err := idx.Insert([]byte("binary-key"), rid); err != nil {
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
	got, err := idx.Search([]byte("binary-key"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != rid {
		t.Fatalf("Search(binary-key) = %v, expected [%v]", got, rid)
	}
}
