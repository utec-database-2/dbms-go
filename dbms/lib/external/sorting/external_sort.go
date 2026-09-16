// Package sorting implementa External Sorting (k-way merge) para ORDER BY.
package sorting

import (
	"encoding/gob"
	"os"
	"errors"
	"fmt"
	"io"
	"sort"
	"github.com/dbms-go/v2/dbms/lib/external/iterator"
	"github.com/dbms-go/v2/dbms/lib/shared"
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

type KWayMergeSorter struct {
	MemoryBufferSize int // registros por run inicial en memoria
	TempDir          string
}

func New(memoryBufferSize int, tempDir string) *KWayMergeSorter {
	return &KWayMergeSorter{MemoryBufferSize: memoryBufferSize, TempDir: tempDir}
}

// Compile-time check: *KWayMergeSorter debe satisfacer Sorter.
var _ Sorter = (*KWayMergeSorter)(nil)

// Comparison Function for int, string, float64 types

func lessKey(a, b any) bool {
	switch av:=a.(type) {
	case int:
		return av < b.(int)
	case string:
		return av < b.(string)
	case float64:
		return av < b.(float64)
	default: 
		panic(fmt.Sprintf("sorting: key type not supported: %T", a))
	}
}

type fileIterator struct {
	dec *gob.Decoder
	f *os.File
}

func (it *fileIterator) Next() (shared.Record, bool, error) {
	var rec shared.Record
	if err := it.dec.Decode(&rec); err != nil {
		if err == io.EOF{
			return shared.Record{}, false, nil
		}
		return shared.Record{}, false, err
	}
	return rec, true, nil
}

func (s *KWayMergeSorter) Sort(input iterator.RecordIterator, keyFn iterator.KeyFunc) (iterator.RecordIterator, error) {

/*  1. Fase de runs: leer el input de a MemoryBufferSize registros,
//     ordenar cada bloque en memoria (sort.Slice con keyFn), y escribir
//     cada bloque ordenado como un "run" en un archivo temporal
//     (usar os.CreateTemp(TempDir, "run-*")).*/
	
	var runPaths []string

	for{
		buffer := make([]shared.Record, 0, s.MemoryBufferSize)
		for len(buffer) < s.MemoryBufferSize {
			rec, ok, err := input.Next()
			if err != nil {
				return nil, err
			}
			if !ok {
				break
			}
			buffer = append(buffer, rec)
		}
		if len(buffer) == 0 {
			break
		}
		sort.Slice(buffer, func(i, j int) bool {
			return lessKey(keyFn(buffer[i]), keyFn(buffer[j]))
		})
		f, err := os.CreateTemp(s.TempDir, "run-*")
		if err != nil {
			return nil, err
		}
		if err := gob.NewEncoder(f).Encode(buffer); err != nil {
			return nil, err
		}
		f.Close()
		runPaths = append(runPaths, f.Name())
		if len(buffer) < s.MemoryBufferSize {
			break
		}
	}

/*  2. Fase de merge: abrir todos los runs a la vez y hacer un k-way
//     merge con un min-heap (container/heap) comparando por keyFn,
//     escribiendo el resultado final en orden. Si hay demasiados runs
//     para abrir todos a la vez, mergear en rondas.
*/	

/*  3. Devolver un iterator.RecordIterator que lea el resultado final
//     de a un registro por vez — no cargarlo todo en un slice.
*/	
	tempiterator := &fileIterator{
		dec: nil, // pendiente inicializacion del decoder
		f: nil,   // pendiente inicializacion del archivo final
	}

	return tempiterator, errNotImplemented
}
