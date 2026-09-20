package integration

import (
	"fmt"
	"sort"
	"testing"

	"github.com/dbms-go/v2/dbms/lib/external/hashing"
	"github.com/dbms-go/v2/dbms/lib/external/iterator"
	"github.com/dbms-go/v2/dbms/lib/shared"
)

func TestExternalHashProcessor_GroupBy(t *testing.T) {
	records := []shared.Record{
		{Values: []any{"a", 1}},
		{Values: []any{"b", 2}},
		{Values: []any{"a", 3}},
	}
	proc := hashing.New(4, t.TempDir())
	keyFn := func(r shared.Record) any { return r.Values[0] }

	groups, err := proc.GroupBy(iterator.NewSliceIterator(records), keyFn)
	if err != nil {
		t.Fatalf("GroupBy falló: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("esperaba 2 grupos (a, b), obtuve %d", len(groups))
	}
}

func TestExternalHashProcessor_HashJoin(t *testing.T) {
	left := []shared.Record{{Values: []any{1, "ana"}}}
	right := []shared.Record{{Values: []any{1, "lima"}}}
	proc := hashing.New(4, t.TempDir())
	leftKeyFn := func(r shared.Record) any { return r.Values[0] }
	rightKeyFn := func(r shared.Record) any { return r.Values[0] }

	pairs, err := proc.HashJoin(iterator.NewSliceIterator(left), iterator.NewSliceIterator(right), leftKeyFn, rightKeyFn)
	if err != nil {
		t.Fatalf("HashJoin falló: %v", err)
	}
	if len(pairs) != 1 {
		t.Fatalf("esperaba 1 par emparejado, obtuve %d", len(pairs))
	}
}

// GroupBy con más registros y particiones que claves distintas: verifica
// contenido real (no solo cantidad de grupos), incluyendo que ningún
// registro se pierda o se duplique entre particiones.
func TestExternalHashProcessor_GroupByLargerDataset(t *testing.T) {
	const numKeys = 20
	const perKey = 15
	var records []shared.Record
	for k := 0; k < numKeys; k++ {
		for i := 0; i < perKey; i++ {
			records = append(records, shared.Record{Values: []any{fmt.Sprintf("k%d", k), i}})
		}
	}

	proc := hashing.New(7, t.TempDir()) // partitionCount que no divide numKeys parejo
	keyFn := func(r shared.Record) any { return r.Values[0] }

	groups, err := proc.GroupBy(iterator.NewSliceIterator(records), keyFn)
	if err != nil {
		t.Fatalf("GroupBy: %v", err)
	}
	if len(groups) != numKeys {
		t.Fatalf("esperaba %d grupos, obtuve %d", numKeys, len(groups))
	}
	total := 0
	for _, g := range groups {
		if len(g.Records) != perKey {
			t.Fatalf("grupo %v tiene %d registros, esperaba %d", g.Key, len(g.Records), perKey)
		}
		total += len(g.Records)
	}
	if total != numKeys*perKey {
		t.Fatalf("total de registros agrupados = %d, esperaba %d", total, numKeys*perKey)
	}
}

// HashJoin con múltiples coincidencias por clave y particiones de distinto
// tamaño; verifica el contenido de los pares, no solo la cantidad.
func TestExternalHashProcessor_HashJoinMultipleMatches(t *testing.T) {
	left := []shared.Record{
		{Values: []any{1, "ana"}},
		{Values: []any{1, "ana2"}},
		{Values: []any{2, "bob"}},
		{Values: []any{3, "sin-match"}},
	}
	right := []shared.Record{
		{Values: []any{1, "lima"}},
		{Values: []any{2, "quito"}},
		{Values: []any{2, "quito2"}},
		{Values: []any{4, "sin-match-derecha"}},
	}
	proc := hashing.New(3, t.TempDir())
	keyFn := func(r shared.Record) any { return r.Values[0] }

	pairs, err := proc.HashJoin(iterator.NewSliceIterator(left), iterator.NewSliceIterator(right), keyFn, keyFn)
	if err != nil {
		t.Fatalf("HashJoin: %v", err)
	}
	// 1x2 (key=1) + 1x2 (key=2, bob con quito y quito2) = 4 pares.
	if len(pairs) != 4 {
		t.Fatalf("esperaba 4 pares, obtuve %d: %+v", len(pairs), pairs)
	}
	got := make([]string, len(pairs))
	for i, p := range pairs {
		got[i] = fmt.Sprintf("%v-%v", p.Left.Values[1], p.Right.Values[1])
	}
	sort.Strings(got)
	want := []string{"ana-lima", "ana2-lima", "bob-quito", "bob-quito2"}
	sort.Strings(want)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pares = %v, esperaba %v", got, want)
		}
	}
}

// Caso borde: un input vacío no debe fallar ni devolver resultados falsos.
func TestExternalHashProcessor_EmptyInputs(t *testing.T) {
	proc := hashing.New(4, t.TempDir())
	keyFn := func(r shared.Record) any { return r.Values[0] }

	groups, err := proc.GroupBy(iterator.NewSliceIterator(nil), keyFn)
	if err != nil {
		t.Fatalf("GroupBy con input vacío: %v", err)
	}
	if len(groups) != 0 {
		t.Fatalf("esperaba 0 grupos, obtuve %d", len(groups))
	}

	pairs, err := proc.HashJoin(iterator.NewSliceIterator(nil), iterator.NewSliceIterator(nil), keyFn, keyFn)
	if err != nil {
		t.Fatalf("HashJoin con inputs vacíos: %v", err)
	}
	if len(pairs) != 0 {
		t.Fatalf("esperaba 0 pares, obtuve %d", len(pairs))
	}
}
