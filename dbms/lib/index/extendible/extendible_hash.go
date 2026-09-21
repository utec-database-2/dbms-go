// Package extendible implementa un índice de Hashing Extensible (Dinámico):
// un directorio de punteros a cubetas que se duplica solo cuando hace falta,
// y cubetas que se dividen (localDepth++) en vez de reescribir todo el
// índice en cada crecimiento.
package extendible

import (
	"fmt"
	"hash/fnv"
	"sync"

	"github.com/dbms-go/v2/dbms/lib/index/common"
	"github.com/dbms-go/v2/dbms/lib/storage"
)

// MaxGlobalDepth acota cuánto puede crecer el directorio. hashKey produce
// un uint32, así que más allá de 32 bits ya no hay forma de distinguir
// claves por hash. En la práctica el límite útil se alcanza mucho antes:
// protege contra un directorio que se duplica sin parar (memoria) cuando
// muchas claves distintas comparten prefijo de hash, o directamente cuando
// una sola clave acumula más RIDs que bucketSize (splitear no ayuda ahí,
// porque todos esos RIDs comparten la misma clave y por lo tanto el mismo
// hash: siempre caen en la misma cubeta sin importar cuánto se profundice).
const MaxGlobalDepth = 24

// bucket es una cubeta del directorio de hashing extensible.
type bucket struct {
	localDepth int
	entries    map[any][]storage.RID // clave -> lista de RID (permite duplicados)
}

func newBucket(localDepth int) *bucket {
	return &bucket{
		localDepth: localDepth,
		entries:    make(map[any][]storage.RID),
	}
}

// Index implementa common.Index usando Hashing Extensible (Dinámico).
type Index struct {
	mu sync.RWMutex

	globalDepth int
	bucketSize  int // capacidad objetivo de entradas por bucket
	directory   []*bucket
}

// New crea un índice con profundidad global inicial 1 (2 entradas de
// directorio apuntando a 2 buckets con localDepth 1). bucketSize menor a 1
// se ajusta a 1: con bucketSize <= 0 cualquier insert dispararía splits
// infinitos hasta chocar con MaxGlobalDepth en vano.
func New(bucketSize int) *Index {
	if bucketSize < 1 {
		bucketSize = 1
	}
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

func (idx *Index) directoryIndexLocked(key any) uint32 {
	mask := uint32(1)<<uint(idx.globalDepth) - 1
	return hashKey(key) & mask
}

func (b *bucket) recordCount() int {
	count := 0
	for _, rids := range b.entries {
		count += len(rids)
	}
	return count
}

// distinctKeyCount cuenta cuántas claves distintas hay en la cubeta. Si es
// 1 y la cubeta sigue sobrepasando bucketSize, splitear nunca va a ayudar
// (todos los RIDs comparten la misma clave, y por lo tanto el mismo hash).
func (b *bucket) distinctKeyCount() int {
	return len(b.entries)
}

func (idx *Index) Insert(key any, rid storage.RID) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	hashedKey := hashKey(key)
	dirIndex := idx.directoryIndexLocked(key)
	idx.directory[dirIndex].entries[key] = append(idx.directory[dirIndex].entries[key], rid)

	for {
		globalDepthLeastSignificantBits := hashedKey & ((1 << uint(idx.globalDepth)) - 1)
		b := idx.directory[globalDepthLeastSignificantBits]
		if b.recordCount() <= idx.bucketSize {
			return nil
		}
		if b.distinctKeyCount() <= 1 {
			// Una sola clave con más RIDs que bucketSize: splitear no la
			// va a separar de sí misma. Se acepta que la cubeta exceda
			// bucketSize en vez de profundizar sin sentido.
			return nil
		}
		if b.localDepth >= MaxGlobalDepth {
			// Muchas claves distintas comparten los MaxGlobalDepth bits
			// menos significativos de su hash (extremadamente raro con un
			// hash real, pero posible). No hay más bits que mirar antes de
			// llegar al límite práctico: se acepta la cubeta sobrecargada.
			return nil
		}

		// Split the bucket
		if b.localDepth == idx.globalDepth {
			// Duplicate the bucket directory
			newDirectory := make([]*bucket, len(idx.directory)*2)
			copy(newDirectory, idx.directory)
			copy(newDirectory[len(idx.directory):], idx.directory)
			idx.directory = newDirectory
			idx.globalDepth++
		}
		bucket1 := newBucket(b.localDepth + 1)
		bucket2 := newBucket(b.localDepth + 1)
		// Re-distribute entries
		for k, v := range b.entries {
			splitBit := uint32(1) << uint(b.localDepth)
			if hashKey(k)&splitBit == 0 {
				bucket1.entries[k] = v
			} else {
				bucket2.entries[k] = v
			}
		}
		localDepth := b.localDepth // capturar ANTES de que bucket1/bucket2 tomen localDepth+1
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

func (idx *Index) Search(key any) ([]storage.RID, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	i := idx.directoryIndexLocked(key)
	b := idx.directory[i]
	rids := b.entries[key]
	out := make([]storage.RID, len(rids))
	copy(out, rids)
	return out, nil
}

func (idx *Index) RangeSearch(keyMin, keyMax any) ([]storage.RID, error) {
	return nil, common.ErrRangeNotSupported
}

func (idx *Index) Delete(key any, rid storage.RID) (bool, error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	i := idx.directoryIndexLocked(key)
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

// GlobalDepth expone la profundidad global actual del directorio, útil
// para inspección/depuración y para la comparación experimental del proyecto.
func (idx *Index) GlobalDepth() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.globalDepth
}
