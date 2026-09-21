package integration

import (
	"os"
	"math/rand"
	"testing"

	"github.com/dbms-go/v2/dbms/lib/external/iterator"
	"github.com/dbms-go/v2/dbms/lib/external/sorting"
	"github.com/dbms-go/v2/dbms/lib/shared"
)

func TestKWayMergeSorter_SortsAscending(t *testing.T) {
	records := []shared.Record{
		{Values: []any{3}},
		{Values: []any{1}},
		{Values: []any{2}},
	}
	sorter := sorting.New(2, t.TempDir())
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

// Regresión: con MemoryBufferSize=1 cada registro genera su propio run, así
// que ordenar unos cientos de registros crea muchos más runs que el límite
// de fan-in del merge (antes se abrían TODOS los runs a la vez sin ningún
// tope, arriesgando agotar los file descriptors del proceso con datasets
// grandes). Este test fuerza varias rondas de merge y verifica que el
// resultado siga siendo correcto.
func TestKWayMergeSorter_ManyRunsForcesMultipleMergeRounds(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	const n = 300 // bastante más que maxFanIn para forzar >1 ronda de merge
	values := make([]int, n)
	records := make([]shared.Record, n)
	for i := range records {
		values[i] = rng.Intn(10000)
		records[i] = shared.Record{Values: []any{values[i]}}
	}

	sorter := sorting.New(1, t.TempDir()) // 1 registro por run
	keyFn := func(r shared.Record) any { return r.Values[0] }

	out, err := sorter.Sort(iterator.NewSliceIterator(records), keyFn)
	if err != nil {
		t.Fatalf("Sort: %v", err)
	}
	got, err := iterator.Drain(out)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(got) != n {
		t.Fatalf("got %d records, want %d", len(got), n)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Values[0].(int) > got[i].Values[0].(int) {
			t.Fatalf("no está ordenado en la posición %d: %v > %v", i, got[i-1].Values[0], got[i].Values[0])
		}
	}
}


func TestKWayMergeSorter_MultipleRunsWithRemainder(t *testing.T) {
	var records []shared.Record
	for _, v := range []int{9, 3, 7, 1, 8, 2, 6, 4, 5, 0} {
		records = append(records, shared.Record{Values: []any{v}})
	}
	sorter := sorting.New(3, t.TempDir()) // fuerza varios runs (10 registros / buffer 3)
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
	sorter := sorting.New(3, t.TempDir())
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
	sorter := sorting.New(100, t.TempDir()) // buffer mucho más grande que el input: un solo run
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
	sorter := sorting.New(2, dir)
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
