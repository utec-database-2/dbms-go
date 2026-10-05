package extendible

import (
	"fmt"

	"github.com/dbms-go/v2/dbms/lib/storage"
	"github.com/dbms-go/v2/dbms/lib/storage/heap"
)

// Rebuild reconstruye explícitamente el índice desde el HeapFile. No se llama
// durante Open; queda disponible para CREATE INDEX/REINDEX o recuperación.
func (idx *Index) Rebuild(hf *heap.HeapFile, keyOf storage.KeyExtractor) error {
	if hf == nil {
		return fmt.Errorf("extendible: heap file is nil")
	}
	if keyOf == nil {
		return fmt.Errorf("extendible: key extractor is nil")
	}

	idx.mu.Lock()
	if err := idx.ensureOpenLocked(); err != nil {
		idx.mu.Unlock()
		return err
	}
	if err := idx.resetLocked(); err != nil {
		idx.mu.Unlock()
		return err
	}
	idx.mu.Unlock()

	it := hf.Scan()
	for it.Next() {
		key, err := keyOf(it.Tuple())
		if err != nil {
			return err
		}
		if err := idx.Insert(key, it.RID()); err != nil {
			return err
		}
	}
	if err := it.Err(); err != nil {
		return err
	}
	return idx.Sync()
}

// NewFromStorage conserva la firma anterior. El índice ahora también es
// file-backed, aunque el archivo es temporal y se elimina en Close.
func NewFromStorage(bucketSize int, hf *heap.HeapFile, keyOf storage.KeyExtractor) (*Index, error) {
	if hf == nil {
		return nil, fmt.Errorf("extendible: heap file is nil")
	}
	if keyOf == nil {
		return nil, fmt.Errorf("extendible: key extractor is nil")
	}
	idx := New(bucketSize)
	if err := idx.Rebuild(hf, keyOf); err != nil {
		_ = idx.Close()
		return nil, err
	}
	return idx, nil
}
