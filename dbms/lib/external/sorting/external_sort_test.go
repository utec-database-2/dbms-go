package sorting

import (
	"testing"

	"github.com/dbms-go/v2/dbms/lib/external/iterator"
	"github.com/dbms-go/v2/dbms/lib/shared"
)

func TestKWayMergeSorter_SortsAscending(t *testing.T) {
	//t.Skip("TODO: implementar KWayMergeSorter.Sort antes de habilitar este test")

	records := []shared.Record{
		{Values: []any{3}},
		{Values: []any{1}},
		{Values: []any{2}},
	}
	sorter := New(2, t.TempDir())
	keyFn := func(r shared.Record) any { return r.Values[0] }

	out, err := sorter.Sort(iterator.NewSliceIterator(records), keyFn)
	if err != nil {
		t.Fatalf("Sort falló: %v", err)
	}
	got, err := iterator.Drain(out)
	if err != nil {
		t.Fatalf("Drain falló: %v", err)
	}
	want := []int{1, 2, 3}
	for i, r := range got {
		if r.Values[0] != want[i] {
			t.Fatalf("posición %d: esperaba %d, obtuve %v", i, want[i], r.Values[0])
		}
	}
}
