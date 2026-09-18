package iterator

import (
	"testing"

	"github.com/dbms-go/v2/dbms/lib/shared"
)

func TestSliceIteratorAndDrain(t *testing.T) {
	records := []shared.Record{
		{Values: []any{1, "ana"}},
		{Values: []any{2, "beto"}},
	}
	it := NewSliceIterator(records)

	got, err := Drain(it)
	if err != nil {
		t.Fatalf("Drain falló: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("esperaba 2 registros, obtuve %d", len(got))
	}
	if got[0].Values[1] != "ana" || got[1].Values[1] != "beto" {
		t.Fatalf("orden o contenido inesperado: %+v", got)
	}
}

func TestSliceIteratorExhausted(t *testing.T) {
	it := NewSliceIterator(nil)
	_, ok, err := it.Next()
	if err != nil {
		t.Fatalf("no debería devolver error: %v", err)
	}
	if ok {
		t.Fatal("un iterador vacío debe devolver ok=false de inmediato")
	}
}
