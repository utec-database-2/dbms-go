// Package hashing implementa External Hashing (particionado por hash)
// para GROUP BY y JOIN.
package hashing

import (
	"errors"

	"github.com/dbms-go/v2/dbms/lib/external/iterator"
	"github.com/dbms-go/v2/dbms/lib/shared"
)

var errNotImplemented = errors.New("hashing: not implemented")

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

func (p *ExternalHashProcessor) GroupBy(input iterator.RecordIterator, keyFn iterator.KeyFunc) ([]GroupResult, error) {
//	GroupBy:
//	  1. Particionar el input en NumPartitions archivos temporales según
//	     hash(keyFn(record)) % NumPartitions.
//	  2. Para cada partición (que ya cabe en memoria si el hash fue
//	     parejo), armar un map[key][]Record en memoria y emitirlo como
//	     GroupResult.
	return nil, errNotImplemented
}

func (p *ExternalHashProcessor) HashJoin(left, right iterator.RecordIterator, leftKeyFn, rightKeyFn iterator.KeyFunc) ([]JoinedPair, error) {
//	HashJoin:
//	  1. Particionar `left` y `right` con la MISMA función de hash sobre
//	     su respectiva key, en NumPartitions archivos cada uno.
//	  2. Para cada número de partición i, cargar la partición i de left
//	     (la más chica de las dos, idealmente) en un map en memoria, y
//	     hacer streaming sobre la partición i de right emparejando por
//	     key (el "probe" del hash join clásico).
	return nil, errNotImplemented
}
