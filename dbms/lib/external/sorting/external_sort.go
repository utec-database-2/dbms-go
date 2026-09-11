// Package sorting implementa External Sorting (k-way merge) para ORDER BY.
package sorting

import (
	"errors"

	"github.com/dbms-go/v2/dbms/lib/external/iterator"
)

var errNotImplemented = errors.New("sorting: not implemented")

// Sorter es el contrato de External Sorting.
type Sorter interface {
	// Sort debe funcionar aunque el input no quepa en memoria
	// (k-way merge sobre runs temporales en disco).
	Sort(input iterator.RecordIterator, keyFn iterator.KeyFunc) (iterator.RecordIterator, error)
}

// KWayMergeSorter implementa Sorter con external merge sort (k-way merge).
//
// TODO(Sergio):
//  1. Fase de runs: leer el input de a MemoryBufferSize registros,
//     ordenar cada bloque en memoria (sort.Slice con keyFn), y escribir
//     cada bloque ordenado como un "run" en un archivo temporal
//     (usar os.CreateTemp(TempDir, "run-*")).
//  2. Fase de merge: abrir todos los runs a la vez y hacer un k-way
//     merge con un min-heap (container/heap) comparando por keyFn,
//     escribiendo el resultado final en orden. Si hay demasiados runs
//     para abrir todos a la vez, mergear en rondas.
//  3. Devolver un iterator.RecordIterator que lea el resultado final
//     de a un registro por vez — no cargarlo todo en un slice.
type KWayMergeSorter struct {
	MemoryBufferSize int // registros por run inicial en memoria
	TempDir          string
}

func New(memoryBufferSize int, tempDir string) *KWayMergeSorter {
	return &KWayMergeSorter{MemoryBufferSize: memoryBufferSize, TempDir: tempDir}
}

// Compile-time check: *KWayMergeSorter debe satisfacer Sorter.
var _ Sorter = (*KWayMergeSorter)(nil)

func (s *KWayMergeSorter) Sort(input iterator.RecordIterator, keyFn iterator.KeyFunc) (iterator.RecordIterator, error) {
	return nil, errNotImplemented
}
