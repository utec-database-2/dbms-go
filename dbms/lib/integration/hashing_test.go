package integration

import (
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