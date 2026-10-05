package bplus

import (
	"encoding/binary"
	"testing"
)

func k(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

func TestTreeInsertSearchRangeDeleteValidate(t *testing.T) {
	tr, err := New[int](4)
	if err != nil {
		t.Fatal(err)
	}
	for i := 50; i >= 1; i-- {
		if err := tr.Insert(k(uint64(i)), i); err != nil {
			t.Fatal(err)
		}
	}
	if err := tr.Insert(k(10), 1000); err != nil {
		t.Fatal(err)
	}
	if err := tr.Validate(); err != nil {
		t.Fatal(err)
	}

	got := tr.Search(k(10))
	if len(got) != 2 {
		t.Fatalf("expected 2 duplicate-key values, got %d", len(got))
	}

	r, err := tr.RangeSearch(k(20), k(25))
	if err != nil {
		t.Fatal(err)
	}
	if len(r) != 6 {
		t.Fatalf("expected 6 range entries, got %d", len(r))
	}

	if err := tr.Delete(k(10), 10); err != nil {
		t.Fatal(err)
	}
	if tr.Contains(k(10), 10) {
		t.Fatal("deleted pair still exists")
	}
	if !tr.Contains(k(10), 1000) {
		t.Fatal("other duplicate was removed")
	}
	if err := tr.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestBulkLoadAndStats(t *testing.T) {
	tr, _ := New[int](5)
	entries := make([]Entry[int], 0, 100)
	for i := 0; i < 100; i++ {
		entries = append(entries, Entry[int]{Key: k(uint64(i)), Value: i})
	}
	if err := tr.BulkLoad(entries); err != nil {
		t.Fatal(err)
	}
	if err := tr.Validate(); err != nil {
		t.Fatal(err)
	}
	s := tr.Stats()
	if s.Entries != 100 || s.Height < 2 {
		t.Fatalf("bad stats: %+v", s)
	}
}

func TestRandomizedOperations(t *testing.T) {
	tr, _ := New[int](4)
	for i := 0; i < 500; i++ {
		key := (i*37 + 11) % 97
		if err := tr.Insert(k(uint64(key)), i); err != nil {
			t.Fatal(err)
		}
		if err := tr.Validate(); err != nil {
			t.Fatalf("after insert %d key %d: %v", i, key, err)
		}
	}
	for key := 0; key < 97; key++ {
		vals := tr.Search(k(uint64(key)))
		if len(vals) == 0 {
			t.Fatalf("key %d missing", key)
		}
	}
}
