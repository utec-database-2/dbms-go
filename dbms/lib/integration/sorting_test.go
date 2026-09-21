package integration

import (
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
