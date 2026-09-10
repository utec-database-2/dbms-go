package extendible

import (
	"errors"
	"fmt"
	"hash/fnv"

	"github.com/dbms-go/v2/dbms/lib/index/common"
	"github.com/dbms-go/v2/dbms/lib/shared"
)

// errNotImplemented marca los métodos que todavía son TODO.
var errNotImplemented = errors.New("extendible: not implemented")

// bucket es una cubeta del directorio de hashing extensible.
type bucket struct {
	localDepth int
	entries    map[any][]shared.RID // clave -> lista de RID (permite duplicados)
}

func newBucket(localDepth int) *bucket {
	return &bucket{
		localDepth: localDepth,
		entries:    make(map[any][]shared.RID),
	}
}

// Index implementa common.Index usando Hashing Extensible (Dinámico).
//
// TODO(Sergio):
//  1. Insert: calcular hash(key), tomar los `globalDepth` bits menos
//     significativos para indexar el directorio, insertar en la bucket.
//     Si la bucket se llena (más entradas que bucketSize):
//       - si localDepth == globalDepth: duplicar el directorio
//         (globalDepth++) antes de splittear
//       - splittear la bucket (localDepth++ en las dos nuevas),
//         re-repartir las entradas existentes, reintentar el insert
//  2. Search: hash(key) -> índice de directorio -> buscar en esa bucket.
//  3. RangeSearch: no soportado, devolver common.ErrRangeNotSupported.
//  4. Delete: ubicar la bucket, quitar el RID de esa key.
type Index struct {
	globalDepth int
	bucketSize  int // capacidad máxima de entradas por bucket
	directory   []*bucket
}

// New crea un índice con profundidad global inicial 1 (2 entradas de
// directorio apuntando a 2 buckets con localDepth 1).
func New(bucketSize int) *Index {
	b0 := newBucket(1)
	b1 := newBucket(1)
	return &Index{
		globalDepth: 1,
		bucketSize:  bucketSize,
		directory:   []*bucket{b0, b1},
	}
}

// Compile-time check: *Index debe satisfacer common.Index.
var _ common.Index = (*Index)(nil)

// hashKey convierte cualquier key comparable a un uint32 vía FNV-1a,
// usando fmt.Sprintf como serialización simple. Si el rendimiento importa,
// reemplázalo por un hash específico según el tipo real de la clave
// (int, string, etc.) una vez que el equipo defina los tipos de columna.
func hashKey(key any) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(fmt.Sprintf("%v", key)))
	return h.Sum32()
}

func (idx *Index) directoryIndex(key any) uint32 {
	mask := uint32(1)<<uint(idx.globalDepth) - 1
	return hashKey(key) & mask
}

func (bucket *bucket) recordCount() int {
	return recordCount(bucket.entries)
}

func recordCount(entries map[any][]shared.RID) int {
	count := 0
	for _, rids := range entries {
		count += len(rids)
	}
	return count
}


func (idx *Index) Insert(key any, rid shared.RID) error {
	hashedKey := hashKey(key)
	dirIndex := idx.directoryIndex(key)
	idx.directory[dirIndex].entries[key] = append(idx.directory[dirIndex].entries[key], rid)

	for{
		globalDepthLeastSignificantBits := hashedKey & ((1 << uint(idx.globalDepth)) - 1)
		bucket := idx.directory[globalDepthLeastSignificantBits]
		if bucket.recordCount() <= idx.bucketSize {
			return nil
		}

		// Split the bucket
		if bucket.localDepth == idx.globalDepth{
			// Duplicate the bucket directory
			newDirectory := make([]*bucket, len(idx.directory)*2)
			copy(newDirectory, idx.directory)
			copy(newDirectory[len(idx.directory):], idx.directory)
			idx.directory = newDirectory
			idx.globalDepth++
		}
		bucket1 := newBucket(bucket.localDepth+1)
		bucket2 := newBucket(bucket.localDepth+1)
		bucket1.entries = make(map[any][]shared.RID)
		bucket2.entries = make(map[any][]shared.RID)
		// Re-distribute entries
		for k, v := range bucket.entries {
			splitBit := uint32(1) << uint(bucket.localDepth)
			if hashKey(k)&splitBit == 0 {
				bucket1.entries[k] = v
			} else {
				bucket2.entries[k] = v
			}
		}
		localDepth := bucket.localDepth // capturar ANTES de que bucket1/bucket2 tomen localDepth+1
		splitBit := uint32(1) << uint(localDepth)
		lowMask := uint32(1)<<uint(localDepth) - 1
		lowBits := globalDepthLeastSignificantBits & lowMask

		for i := range idx.directory {
			if uint32(i)&lowMask != lowBits {
				continue
			}
			if uint32(i)&splitBit == 0 {
				idx.directory[i] = bucket1
			} else {
				idx.directory[i] = bucket2
			}
		}
	}
}

func (idx *Index) Search(key any) ([]shared.RID, error) {
	i := idx.directoryIndex(key)
	b := idx.directory[i]
	return b.entries[key], nil
}

func (idx *Index) RangeSearch(keyMin, keyMax any) ([]shared.RID, error) {
	return nil, common.ErrRangeNotSupported
}

func (idx *Index) Delete(key any, rid shared.RID) (bool, error) {
	i:= idx.directoryIndex(key)
	b := idx.directory[i]
	rids, exists := b.entries[key]
	if !exists {
		return false, nil
	}
	for j, r := range rids {
		if r == rid {
			b.entries[key] = append(rids[:j], rids[j+1:]...)
			if len(b.entries[key]) == 0 {
				delete(b.entries, key)
			}
			return true, nil
		}
	}
	return false, nil
}

func (idx *Index) SupportsRange() bool {
	return false
}
