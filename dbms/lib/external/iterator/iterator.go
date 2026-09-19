// Package iterator define cómo sorting y hashing consumen registros sin
// necesitar el dataset completo en memoria — que es justamente el punto
// de que esos algoritmos sean "externos".
package iterator

import "github.com/dbms-go/v2/dbms/lib/shared"

// KeyFunc extrae de un Record la clave por la que se ordena/agrupa/unetea.
type KeyFunc func(shared.Record) any

// RecordIterator entrega registros de a uno (por ejemplo, leyendo de
// disco). ok=false (con err=nil) significa que el iterador se agotó.
type RecordIterator interface {
	Next() (rec shared.Record, ok bool, err error)
}

// SliceIterator adapta un slice en memoria a RecordIterator. Úsalo en
// tests para no depender del storage real (que todavía no existe) — el
// día que exista, cambias el iterador que le pasas a Sort/GroupBy/
// HashJoin, pero la lógica interna de tus algoritmos no cambia.
type SliceIterator struct {
	records []shared.Record
	pos     int
}

func NewSliceIterator(records []shared.Record) *SliceIterator {
	return &SliceIterator{records: records}
}

func (it *SliceIterator) Next() (shared.Record, bool, error) {
	if it.pos >= len(it.records) {
		return shared.Record{}, false, nil
	}
	r := it.records[it.pos]
	it.pos++
	return r, true, nil
}

// Drain consume un RecordIterator completo y lo vuelca a un slice.
// Útil en tests para inspeccionar un resultado.
func Drain(it RecordIterator) ([]shared.Record, error) {
	var out []shared.Record
	for {
		rec, ok, err := it.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return out, nil
		}
		out = append(out, rec)
	}
}
