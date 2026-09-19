// Package hashing implementa External Hashing (particionado por hash)
// para GROUP BY y JOIN.
package hashing

import (
	"encoding/gob"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"sort"

	"github.com/dbms-go/v2/dbms/lib/external/iterator"
	"github.com/dbms-go/v2/dbms/lib/shared"
)

// GroupResult es un grupo producido por GroupBy.
type GroupResult struct {
	Key     any
	Records []shared.Record
}

// JoinedPair es un par emparejado producido por HashJoin.
type JoinedPair struct {
	Left  shared.Record
	Right shared.Record
}

// Hasher es el contrato de External Hashing.
type Hasher interface {
	GroupBy(input iterator.RecordIterator, keyFn iterator.KeyFunc) ([]GroupResult, error)
	HashJoin(left, right iterator.RecordIterator, leftKeyFn, rightKeyFn iterator.KeyFunc) ([]JoinedPair, error)
}

// ExternalHashProcessor implementa Hasher con hashing externo particionado.
//
// TODO(Sergio):
//

type ExternalHashProcessor struct {
	NumPartitions int
	TempDir       string
}

func New(numPartitions int, tempDir string) *ExternalHashProcessor {
	return &ExternalHashProcessor{NumPartitions: numPartitions, TempDir: tempDir}
}

// Compile-time check: *ExternalHashProcessor debe satisfacer Hasher.
var _ Hasher = (*ExternalHashProcessor)(nil)

// hashKey combina el tipo y el valor de la clave para que claves de distinto
// tipo (p.ej. int 1 vs string "1") no colisionen en la misma partición.
func hashKey(k any) uint64 {
	h := fnv.New64a()
	h.Write([]byte(fmt.Sprintf("%T:%v", k, k)))
	return h.Sum64()
}

func (p *ExternalHashProcessor) partitionCount() int {
	if p.NumPartitions < 1 {
		return 1
	}
	return p.NumPartitions
}

// partitionToFiles vuelca un iterador en NumPartitions archivos temporales,
// usando hashKey(keyFn(rec)) % NumPartitions como función de partición.
// Al terminar cierra todos los archivos; las particiones vacías no crean archivo.
func (p *ExternalHashProcessor) partitionToFiles(
	input iterator.RecordIterator,
	keyFn iterator.KeyFunc,
	pattern string,
	paths *[]string,
) (err error) {
	parts := p.partitionCount()
	pathsArr := make([]string, parts)
	writers := make([]*os.File, parts)
	encoders := make([]*gob.Encoder, parts)
	defer func() {
		for _, w := range writers {
			if w != nil {
				w.Close()
			}
		}
		if err != nil {
			for _, path := range pathsArr {
				if path != "" {
					os.Remove(path)
				}
			}
		} else {
			*paths = pathsArr
		}
	}()

	for {
		rec, ok, err := input.Next()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}

		idx := int(hashKey(keyFn(rec)) % uint64(parts))
		if writers[idx] == nil {
			f, err := os.CreateTemp(p.TempDir, pattern)
			if err != nil {
				return err
			}
			writers[idx] = f
			pathsArr[idx] = f.Name()
			encoders[idx] = gob.NewEncoder(f)
		}
		if err := encoders[idx].Encode(rec); err != nil {
			return err
		}
	}
}

func (p *ExternalHashProcessor) GroupBy(input iterator.RecordIterator, keyFn iterator.KeyFunc) ([]GroupResult, error) {
	parts := p.partitionCount()

	var paths []string
	if err := p.partitionToFiles(input, keyFn, "groupby-*", &paths); err != nil {
		return nil, err
	}
	defer func() {
		for _, path := range paths {
			os.Remove(path)
		}
	}()

	// Cada partición cabe en memoria si el hash fue parejo: se agrupa con un
	// map por partición y se emite un GroupResult por clave.
	var groups []GroupResult
	for i := 0; i < parts; i++ {
		if paths[i] == "" {
			continue
		}
		f, err := os.Open(paths[i])
		if err != nil {
			return nil, err
		}
		dec := gob.NewDecoder(f)
		buckets := make(map[any][]shared.Record)
		for {
			var rec shared.Record
			if err := dec.Decode(&rec); err != nil {
				if err == io.EOF {
					break
				}
				f.Close()
				return nil, err
			}
			key := keyFn(rec)
			buckets[key] = append(buckets[key], rec)
		}
		f.Close()

		for key, recs := range buckets {
			groups = append(groups, GroupResult{Key: key, Records: recs})
		}
	}

	// Orden determinista para que el resultado no dependa de la iteración de map.
	sort.Slice(groups, func(i, j int) bool {
		return fmt.Sprintf("%T:%v", groups[i].Key, groups[i].Key) <
			fmt.Sprintf("%T:%v", groups[j].Key, groups[j].Key)
	})
	return groups, nil
}

func (p *ExternalHashProcessor) HashJoin(
	left, right iterator.RecordIterator,
	leftKeyFn, rightKeyFn iterator.KeyFunc,
) ([]JoinedPair, error) {
	parts := p.partitionCount()

	var leftPaths, rightPaths []string
	if err := p.partitionToFiles(left, leftKeyFn, "join-left-*", &leftPaths); err != nil {
		return nil, err
	}
	defer func() {
		for _, path := range leftPaths {
			os.Remove(path)
		}
	}()
	if err := p.partitionToFiles(right, rightKeyFn, "join-right-*", &rightPaths); err != nil {
		return nil, err
	}
	defer func() {
		for _, path := range rightPaths {
			os.Remove(path)
		}
	}()

	// Hash join clásico: por cada partición i, build con la partición i de left
	// (la idealmente más chica) y probe en streaming con la partición i de right.
	var pairs []JoinedPair
	for i := 0; i < parts; i++ {
		buckets := make(map[any][]shared.Record)
		if leftPaths[i] != "" {
			f, err := os.Open(leftPaths[i])
			if err != nil {
				return nil, err
			}
			dec := gob.NewDecoder(f)
			for {
				var rec shared.Record
				if err := dec.Decode(&rec); err != nil {
					if err == io.EOF {
						break
					}
					f.Close()
					return nil, err
				}
				key := leftKeyFn(rec)
				buckets[key] = append(buckets[key], rec)
			}
			f.Close()
		}

		if rightPaths[i] == "" {
			continue
		}
		f, err := os.Open(rightPaths[i])
		if err != nil {
			return nil, err
		}
		dec := gob.NewDecoder(f)
		for {
			var rec shared.Record
			if err := dec.Decode(&rec); err != nil {
				if err == io.EOF {
					break
				}
				f.Close()
				return nil, err
			}
			key := rightKeyFn(rec)
			for _, lrec := range buckets[key] {
				pairs = append(pairs, JoinedPair{Left: lrec, Right: rec})
			}
		}
		f.Close()
	}
	return pairs, nil
}