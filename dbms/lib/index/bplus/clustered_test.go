package bplus

import (
	"testing"

	"github.com/dbms-go/v2/dbms/lib/storage"
)

var rangeTable = storage.Table{
	Name:    "r",
	Columns: []string{"id"},
	Types:   []storage.Type{storage.TypeInt32},
	MaxLen:  []int{0},
	KeyCols: []int{0},
}

// memSeq es un almacenamiento ordenado por PK en memoria que cuenta cuántas
// filas entrega, para comprobar que un rango no recorre todo el archivo.
type memSeq struct {
	rows    []storage.Tuple // ordenadas por id
	scanned int
}

func (m *memSeq) Insert(t storage.Tuple) error {
	i := 0
	for i < len(m.rows) && m.rows[i][0].(int32) < t[0].(int32) {
		i++
	}
	m.rows = append(m.rows[:i], append([]storage.Tuple{t}, m.rows[i:]...)...)
	return nil
}
func (m *memSeq) Search(k storage.Tuple) (storage.Tuple, error) {
	for _, r := range m.rows {
		if r[0] == k[0] {
			return r, nil
		}
	}
	return nil, storage.ErrRecordNotFound
}
func (m *memSeq) Update(k, t storage.Tuple) error { return nil }
func (m *memSeq) Delete(k storage.Tuple) error    { return nil }
func (m *memSeq) Open() error                     { return nil }
func (m *memSeq) Close() error                    { return nil }
func (m *memSeq) Scan(fn func(storage.Tuple) bool) error {
	for _, r := range m.rows {
		m.scanned++
		if !fn(r) {
			break
		}
	}
	return nil
}
func (m *memSeq) RangeScan(lo, hi storage.Tuple, fn func(storage.Tuple) bool) error {
	for _, r := range m.rows {
		if r[0].(int32) < lo[0].(int32) {
			continue // un secuencial real se posiciona con búsqueda binaria
		}
		if r[0].(int32) > hi[0].(int32) {
			break
		}
		m.scanned++
		if !fn(r) {
			break
		}
	}
	return nil
}

// TestClusteredRangeLeeSoloElRango: el B+ agrupado debe leer del archivo
// ordenado solo las filas del rango, no el archivo entero.
func TestClusteredRangeLeeSoloElRango(t *testing.T) {
	seq := &memSeq{}
	idx, err := NewClusteredIndex(8, seq, rangeTable)
	if err != nil {
		t.Fatal(err)
	}
	for i := int32(0); i < 1000; i++ {
		if err := idx.Insert(storage.Tuple{i}); err != nil {
			t.Fatal(err)
		}
	}
	seq.scanned = 0
	rows, err := idx.RangeSearch(storage.Tuple{int32(500)}, storage.Tuple{int32(509)})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 10 || rows[0][0] != int32(500) || rows[9][0] != int32(509) {
		t.Fatalf("rango [500, 509]: %v", rows)
	}
	if seq.scanned != 10 {
		t.Fatalf("el rango leyó %d filas del archivo; debía leer solo las 10 del rango", seq.scanned)
	}
}
