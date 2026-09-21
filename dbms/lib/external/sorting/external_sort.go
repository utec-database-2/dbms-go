// Package sorting implementa External Sorting (k-way merge) para ORDER BY.
package sorting

import (
	"container/heap"
	"encoding/gob"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/dbms-go/v2/dbms/lib/external/iterator"
	"github.com/dbms-go/v2/dbms/lib/shared"
)

// maxFanIn acota cuántos runs se mergean juntos de una sola pasada (cuántos
// archivos se abren a la vez). Sin este límite, un dataset grande con un
// MemoryBufferSize chico genera un run por cada bloque y el merge final
// intentaría abrir todos los runs simultáneamente, pudiendo agotar los
// file descriptors del proceso. Cuando hay más runs que maxFanIn, se
// mergean en rondas: cada ronda combina grupos de hasta maxFanIn runs en
// uno nuevo, hasta que sobran pocos como para mergear de una.
const maxFanIn = 64

// Sorter es el contrato de External Sorting.
type Sorter interface {
	// Sort debe funcionar aunque el input no quepa en memoria
	// (k-way merge sobre runs temporales en disco).
	Sort(input iterator.RecordIterator, keyFn iterator.KeyFunc) (iterator.RecordIterator, error)
}

// KWayMergeSorter implementa Sorter con external merge sort (k-way merge).
type KWayMergeSorter struct {
	MemoryBufferSize int // registros por run inicial en memoria
	TempDir          string
}

func New(memoryBufferSize int, tempDir string) *KWayMergeSorter {
	return &KWayMergeSorter{MemoryBufferSize: memoryBufferSize, TempDir: tempDir}
}

// Compile-time check: *KWayMergeSorter debe satisfacer Sorter.
var _ Sorter = (*KWayMergeSorter)(nil)

// lessKey compara claves de los tipos que el proyecto usa como columnas.
func lessKey(a, b any) bool {
	switch av := a.(type) {
	case int:
		return av < b.(int)
	case int64:
		return av < b.(int64)
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
	f   *os.File
}

func (it *fileIterator) Next() (shared.Record, bool, error) {
	var rec shared.Record
	if err := it.dec.Decode(&rec); err != nil {
		if err == io.EOF {
			it.f.Close()
			return shared.Record{}, false, nil
		}
		return shared.Record{}, false, err
	}
	return rec, true, nil
}

type heapItem struct {
	rec      shared.Record
	runIndex int // de qué decoder salió este record
}

type recordHeap struct {
	items []heapItem
	keyFn iterator.KeyFunc
}

func (h *recordHeap) Len() int { return len(h.items) }
func (h *recordHeap) Less(i, j int) bool {
	return lessKey(h.keyFn(h.items[i].rec), h.keyFn(h.items[j].rec))
}
func (h *recordHeap) Swap(i, j int) { h.items[i], h.items[j] = h.items[j], h.items[i] }
func (h *recordHeap) Push(x any)    { h.items = append(h.items, x.(heapItem)) }
func (h *recordHeap) Pop() any {
	old := h.items
	n := len(old)
	item := old[n-1]
	h.items = old[:n-1]
	return item
}

// removeFiles borra una lista de paths, ignorando los que no existan.
func removeFiles(paths []string) {
	for _, p := range paths {
		os.Remove(p)
	}
}

func (s *KWayMergeSorter) Sort(input iterator.RecordIterator, keyFn iterator.KeyFunc) (iterator.RecordIterator, error) {
	// 1. Fase de runs: leer el input de a MemoryBufferSize registros,
	// ordenar cada bloque en memoria y escribirlo como un run en disco.
	runPaths, err := s.writeInitialRuns(input, keyFn)
	if err != nil {
		removeFiles(runPaths)
		return nil, err
	}

	// 2. Fase de merge, en rondas de hasta maxFanIn runs por vez, hasta
	// quedar con uno solo: ese es el resultado final.
	for len(runPaths) > maxFanIn {
		nextRound := make([]string, 0, (len(runPaths)+maxFanIn-1)/maxFanIn)
		for i := 0; i < len(runPaths); i += maxFanIn {
			end := i + maxFanIn
			if end > len(runPaths) {
				end = len(runPaths)
			}
			merged, err := s.mergeRuns(runPaths[i:end], keyFn, "run-*")
			if err != nil {
				removeFiles(runPaths[i:])
				removeFiles(nextRound)
				return nil, err
			}
			nextRound = append(nextRound, merged)
		}
		runPaths = nextRound
	}

	finalPath, err := s.mergeRuns(runPaths, keyFn, "sorted-*")
	if err != nil {
		removeFiles(runPaths)
		return nil, err
	}

	// 3. Devolver un iterator.RecordIterator que lee el resultado final de
	// a un registro por vez, sin cargarlo todo en memoria.
	outFile, err := os.Open(finalPath)
	if err != nil {
		os.Remove(finalPath)
		return nil, err
	}
	return &fileIterator{dec: gob.NewDecoder(outFile), f: outFile}, nil
}

// writeInitialRuns parte input en bloques de MemoryBufferSize registros,
// ordena cada bloque en memoria y lo escribe como un run en un temp file.
func (s *KWayMergeSorter) writeInitialRuns(input iterator.RecordIterator, keyFn iterator.KeyFunc) ([]string, error) {
	var runPaths []string
	for {
		buffer := make([]shared.Record, 0, s.MemoryBufferSize)
		for len(buffer) < s.MemoryBufferSize {
			rec, ok, err := input.Next()
			if err != nil {
				return runPaths, err
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
			return runPaths, err
		}
		enc := gob.NewEncoder(f)
		for _, r := range buffer {
			if err := enc.Encode(r); err != nil {
				f.Close()
				runPaths = append(runPaths, f.Name())
				return runPaths, err
			}
		}
		f.Close()
		runPaths = append(runPaths, f.Name())
		if len(buffer) < s.MemoryBufferSize {
			break
		}
	}
	return runPaths, nil
}

// mergeRuns hace un k-way merge (min-heap) de paths en un nuevo temp file,
// que retorna. Cierra y borra los archivos de entrada antes de volver,
// tanto en el camino feliz como en caso de error.
func (s *KWayMergeSorter) mergeRuns(paths []string, keyFn iterator.KeyFunc, pattern string) (outPath string, err error) {
	decoders := make([]*gob.Decoder, len(paths))
	files := make([]*os.File, len(paths))
	defer func() {
		for _, f := range files {
			if f != nil {
				f.Close()
			}
		}
		removeFiles(paths)
	}()

	var h recordHeap
	h.keyFn = keyFn
	for i, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		files[i] = f
		decoders[i] = gob.NewDecoder(f)
		var rec shared.Record
		if err := decoders[i].Decode(&rec); err != nil {
			if err == io.EOF {
				continue // run vacío: no aporta al heap inicial
			}
			return "", err
		}
		h.items = append(h.items, heapItem{rec: rec, runIndex: i})
	}
	heap.Init(&h)

	outFile, err := os.CreateTemp(s.TempDir, pattern)
	if err != nil {
		return "", err
	}
	outEnc := gob.NewEncoder(outFile)

	for h.Len() > 0 {
		item := heap.Pop(&h).(heapItem)
		if err := outEnc.Encode(item.rec); err != nil {
			outFile.Close()
			os.Remove(outFile.Name())
			return "", err
		}
		var next shared.Record
		derr := decoders[item.runIndex].Decode(&next)
		if derr == nil {
			heap.Push(&h, heapItem{rec: next, runIndex: item.runIndex})
		} else if derr != io.EOF {
			outFile.Close()
			os.Remove(outFile.Name())
			return "", derr
		}
	}

	if err := outFile.Close(); err != nil {
		os.Remove(outFile.Name())
		return "", err
	}
	return outFile.Name(), nil
}
