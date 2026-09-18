package integration

import (
	"testing"

	"github.com/dbms-go/v2/indexes/bplus"
)

func TestTreeStress100k(t *testing.T) {
	if testing.Short() {
		t.Skip("100k stress test")
	}
	tree := bplus.MustNew[int64, int64](64)
	const n = 100000
	for i := int64(0); i < n; i++ {
		key := (i * 7919) % 20003
		if err := tree.Insert(key, i); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	if err := tree.Validate(); err != nil {
		t.Fatalf("validate after insert: %v", err)
	}
	if tree.Len() != n {
		t.Fatalf("len=%d", tree.Len())
	}
	if _, err := tree.RangeSearch(500, 1500); err != nil {
		t.Fatal(err)
	}
	for i := int64(0); i < n; i += 2 {
		key := (i * 7919) % 20003
		if err := tree.Delete(key, i); err != nil {
			t.Fatalf("delete %d: %v", i, err)
		}
	}
	if err := tree.Validate(); err != nil {
		t.Fatalf("validate after deletes: %v", err)
	}
	if tree.Len() != n/2 {
		t.Fatalf("len after delete=%d", tree.Len())
	}
}