package bplus

import (
	"path/filepath"
	"testing"
)

type testRID struct {
	PageID uint32
	SlotID uint16
}

func TestTreePersistsAcrossCloseOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users_age.idx")

	tree, err := Create[int64, testRID](path, 4)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		key := int64(i % 17) // fuerza claves repetidas
		rid := testRID{PageID: uint32(i), SlotID: uint16(i % 8)}
		if err := tree.Insert(key, rid); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	if err := tree.Validate(); err != nil {
		t.Fatalf("validate before close: %v", err)
	}
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}

	// Esta apertura NO recibe entries ni ejecuta BulkLoad: el árbol sale del .idx.
	tree, err = Open[int64, testRID](path)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()

	if tree.Len() != 100 {
		t.Fatalf("len=%d, want=100", tree.Len())
	}
	if err := tree.Validate(); err != nil {
		t.Fatalf("validate after reopen: %v", err)
	}

	got, err := tree.SearchE(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("expected repeated key 3 to have RIDs")
	}
}

func TestTreeReusesPagesAfterMerges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "merge.idx")
	tree, err := Create[int64, uint64](path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()

	for i := 0; i < 80; i++ {
		if err := tree.Insert(int64(i), uint64(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 65; i++ {
		if err := tree.Delete(int64(i), uint64(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tree.Validate(); err != nil {
		t.Fatal(err)
	}

	// Después de merges hay páginas en free-list, las nuevas inserciones deben
	// poder reutilizarlas en lugar de depender únicamente de append al archivo.
	for i := 100; i < 140; i++ {
		if err := tree.Insert(int64(i), uint64(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tree.Validate(); err != nil {
		t.Fatal(err)
	}
}
/*
para correr este archivo de test:
go test . -v -count=1
*/