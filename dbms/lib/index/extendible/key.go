package extendible

import (
	"fmt"

	"github.com/dbms-go/v2/dbms/lib/storage"
	"github.com/dbms-go/v2/dbms/lib/storage/heap"
)

// Rebuild reemplaza el contenido del índice por el de un escaneo completo del
// heap. Construye un índice nuevo aparte y lo intercambia al final, así que
// las búsquedas concurrentes ven siempre el contenido viejo o el nuevo
// completo, nunca uno vacío o a medias. Si falla, el índice no cambia.
func (idx *Index) Rebuild(hf *heap.HeapFile, keyOf storage.KeyExtractor) error {
	if hf == nil {
		return fmt.Errorf("%w: heap file", errNilArg)
	}
	if keyOf == nil {
		return fmt.Errorf("%w: key extractor", errNilArg)
	}
	if heap.Strategy(hf.Header().Strategy) == heap.MoveTheLast {
		return storage.ErrUnstableRIDs
	}

	fresh, err := NewWithOptions(idx.opts)
	if err != nil {
		return err
	}
	it := hf.Scan()
	for it.Next() {
		key, err := keyOf(it.Tuple())
		if err != nil {
			return err
		}
		if err := fresh.Insert(key, it.RID()); err != nil {
			return err
		}
	}
	if err := it.Err(); err != nil {
		return err
	}

	idx.mu.Lock()
	idx.globalDepth, idx.directory = fresh.globalDepth, fresh.directory
	idx.keys, idx.records, idx.depthCount = fresh.keys, fresh.records, fresh.depthCount
	idx.mu.Unlock()
	return nil
}

// NewFromStorage crea un índice y lo reconstruye escaneando el heap.
func NewFromStorage(bucketSize int, hf *heap.HeapFile, keyOf storage.KeyExtractor) (*Index, error) {
	idx := New(bucketSize)
	if err := idx.Rebuild(hf, keyOf); err != nil {
		return nil, err
	}
	return idx, nil
}
