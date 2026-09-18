package sorting

import (
	"os"
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

func TestKWayMergeSorter_MultipleRunsWithRemainder(t *testing.T) {
	var records []shared.Record
	for _, v := range []int{9, 3, 7, 1, 8, 2, 6, 4, 5, 0} {
		records = append(records, shared.Record{Values: []any{v}})
	}
	sorter := New(3, t.TempDir()) // fuerza varios runs (10 registros / buffer 3)
	keyFn := func(r shared.Record) any { return r.Values[0] }

	out, err := sorter.Sort(iterator.NewSliceIterator(records), keyFn)
	if err != nil {
		t.Fatalf("Sort falló: %v", err)
	}
	got, err := iterator.Drain(out)
	if err != nil {
		t.Fatalf("Drain falló: %v", err)
	}
	want := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	if len(got) != len(want) {
		t.Fatalf("esperaba %d registros, obtuve %d", len(want), len(got))
	}
	for i, r := range got {
		if r.Values[0] != want[i] {
			t.Fatalf("posición %d: esperaba %d, obtuve %v", i, want[i], r.Values[0])
		}
	}
}

func TestKWayMergeSorter_EmptyInput(t *testing.T) {
	sorter := New(3, t.TempDir())
	keyFn := func(r shared.Record) any { return r.Values[0] }

	out, err := sorter.Sort(iterator.NewSliceIterator(nil), keyFn)
	if err != nil {
		t.Fatalf("Sort con input vacío no debería fallar: %v", err)
	}
	got, err := iterator.Drain(out)
	if err != nil {
		t.Fatalf("Drain falló: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("esperaba 0 registros, obtuve %d", len(got))
	}
}

func TestKWayMergeSorter_SingleRun_InputSmallerThanBuffer(t *testing.T) {
	records := []shared.Record{
		{Values: []any{3}}, {Values: []any{1}}, {Values: []any{2}},
	}
	sorter := New(100, t.TempDir()) // buffer mucho más grande que el input: un solo run
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

func TestKWayMergeSorter_TemporaryRunFilesAreCleanedUp(t *testing.T) {
	dir := t.TempDir()
	var records []shared.Record
	for _, v := range []int{5, 3, 1, 4, 2} {
		records = append(records, shared.Record{Values: []any{v}})
	}
	sorter := New(2, dir)
	keyFn := func(r shared.Record) any { return r.Values[0] }

	if _, err := sorter.Sort(iterator.NewSliceIterator(records), keyFn); err != nil {
		t.Fatalf("Sort falló: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if len(e.Name()) >= 4 && e.Name()[:4] == "run-" {
			t.Errorf("el run temporal %s no se borró después del merge", e.Name())
		}
	}
}
