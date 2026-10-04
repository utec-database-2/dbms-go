package bplus

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/dbms-go/v2/dbms/lib/storage/heap"
)

// KeyExtractor obtiene del payload la clave secundaria que debe indexarse.
type KeyExtractor[K Ordered] func(payload []byte) (K, error)

// UnclusteredRecord es un registro resuelto a través de clave -> RID -> HeapFile.
type UnclusteredRecord[K Ordered] struct {
	Key     K
	RID     heap.RecordID
	Payload []byte
}

type UnclusteredStats struct {
	Tree    TreeStats
	Storage heap.Stats
}

type UnclusteredIndex[K Ordered] struct {
	mu      sync.RWMutex
	tree    *Tree[K, heap.RecordID]
	storage *heap.HeapFile
	keyOf   KeyExtractor[K]
	order   int
}

// CreateUnclusteredIndex crea/trunca indexPath y construye el índice inicial
// escaneando el HeapFile una única vez.
func CreateUnclusteredIndex[K Ordered](indexPath string, order int, storage *heap.HeapFile, keyOf KeyExtractor[K]) (*UnclusteredIndex[K], error) {
	if storage == nil {
		return nil, fmt.Errorf("bplus: nil heap storage")
	}
	if keyOf == nil {
		return nil, fmt.Errorf("bplus: nil key extractor")
	}
	tree, err := Create[K, heap.RecordID](indexPath, order)
	if err != nil {
		return nil, err
	}
	idx := &UnclusteredIndex[K]{tree: tree, storage: storage, keyOf: keyOf, order: order}
	if err := idx.Rebuild(); err != nil {
		tree.Close()
		return nil, err
	}
	return idx, nil
}

// OpenUnclusteredIndex reabre el archivo del B+ sin escanear ni reconstruir el
// HeapFile. Esa es la diferencia principal respecto de la versión anterior.
func OpenUnclusteredIndex[K Ordered](indexPath string, storage *heap.HeapFile, keyOf KeyExtractor[K]) (*UnclusteredIndex[K], error) {
	if storage == nil {
		return nil, fmt.Errorf("bplus: nil heap storage")
	}
	if keyOf == nil {
		return nil, fmt.Errorf("bplus: nil key extractor")
	}
	tree, err := Open[K, heap.RecordID](indexPath)
	if err != nil {
		return nil, err
	}
	return &UnclusteredIndex[K]{tree: tree, storage: storage, keyOf: keyOf, order: tree.Order()}, nil
}

func OpenOrCreateUnclusteredIndex[K Ordered](indexPath string, order int, storage *heap.HeapFile, keyOf KeyExtractor[K]) (*UnclusteredIndex[K], error) {
	if storage == nil {
		return nil, fmt.Errorf("bplus: nil heap storage")
	}
	if keyOf == nil {
		return nil, fmt.Errorf("bplus: nil key extractor")
	}
	_, statErr := os.Stat(indexPath)
	existed := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}

	tree, err := OpenOrCreate[K, heap.RecordID](indexPath, order)
	if err != nil {
		return nil, err
	}
	idx := &UnclusteredIndex[K]{tree: tree, storage: storage, keyOf: keyOf, order: tree.Order()}
	if !existed {
		if err := idx.Rebuild(); err != nil {
			tree.Close()
			return nil, err
		}
	}
	return idx, nil
}

// NewUnclusteredIndex conserva compatibilidad con llamadas antiguas.
func NewUnclusteredIndex[K Ordered](order int, storage *heap.HeapFile, keyOf KeyExtractor[K], indexPath ...string) (*UnclusteredIndex[K], error) {
	if len(indexPath) > 1 {
		return nil, fmt.Errorf("bplus: expected at most one index path")
	}
	if len(indexPath) == 1 {
		return OpenOrCreateUnclusteredIndex(indexPath[0], order, storage, keyOf)
	}
	if storage == nil {
		return nil, fmt.Errorf("bplus: nil heap storage")
	}
	if keyOf == nil {
		return nil, fmt.Errorf("bplus: nil key extractor")
	}
	tree, err := New[K, heap.RecordID](order)
	if err != nil {
		return nil, err
	}
	idx := &UnclusteredIndex[K]{tree: tree, storage: storage, keyOf: keyOf, order: order}
	if err := idx.Rebuild(); err != nil {
		tree.Close()
		return nil, err
	}
	return idx, nil
}

func (idx *UnclusteredIndex[K]) IndexPath() string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.tree.Path()
}

func (idx *UnclusteredIndex[K]) Close() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.tree.Close()
}

// Rebuild es REINDEX explícito
func (idx *UnclusteredIndex[K]) Rebuild() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	entries := make([]Entry[K, heap.RecordID], 0)
	var extractErr error
	err := idx.storage.Scan(func(rid heap.RecordID, payload []byte) bool {
		key, err := idx.keyOf(payload)
		if err != nil {
			extractErr = fmt.Errorf("bplus: key extraction for rid=%s: %w", rid, err)
			return false
		}
		entries = append(entries, Entry[K, heap.RecordID]{Key: key, Value: rid})
		return true
	})
	if err != nil {
		return err
	}
	if extractErr != nil {
		return extractErr
	}
	if err := idx.tree.BulkLoad(entries); err != nil {
		return err
	}
	return idx.tree.Validate()
}

// Insert extrae la clave desde payload, persiste primero el registro en
// HeapFile y después agrega clave -> RID al archivo B+.
func (idx *UnclusteredIndex[K]) Insert(payload []byte) (heap.RecordID, error) {
	key, err := idx.keyOf(payload)
	if err != nil {
		return heap.RecordID{}, err
	}
	return idx.InsertWithKey(key, payload)
}

func (idx *UnclusteredIndex[K]) InsertWithKey(key K, payload []byte) (heap.RecordID, error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	derived, err := idx.keyOf(payload)
	if err != nil {
		return heap.RecordID{}, err
	}
	if derived != key {
		return heap.RecordID{}, fmt.Errorf("bplus: supplied key does not match payload key")
	}

	rid, err := idx.storage.Insert(payload)
	if err != nil {
		return heap.RecordID{}, err
	}
	if err := idx.tree.Insert(key, rid); err != nil {
		rollbackErr := idx.storage.Delete(rid)
		if rollbackErr != nil {
			return heap.RecordID{}, fmt.Errorf("bplus: tree insert failed: %v; heap rollback failed: %v", err, rollbackErr)
		}
		return heap.RecordID{}, err
	}
	return rid, nil
}

func (idx *UnclusteredIndex[K]) Search(key K) ([]UnclusteredRecord[K], error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	rids, err := idx.tree.SearchE(key)
	if err != nil {
		return nil, err
	}
	out := make([]UnclusteredRecord[K], 0, len(rids))
	for _, rid := range rids {
		payload, err := idx.storage.Read(rid)
		if err != nil {
			return nil, fmt.Errorf("%w: key=%v rid=%s: %v", ErrIndexStorageMismatch, key, rid, err)
		}
		out = append(out, UnclusteredRecord[K]{Key: key, RID: rid, Payload: payload})
	}
	return out, nil
}

func (idx *UnclusteredIndex[K]) RangeSearch(low, high K) ([]UnclusteredRecord[K], error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	entries, err := idx.tree.RangeSearch(low, high)
	if err != nil {
		return nil, err
	}
	out := make([]UnclusteredRecord[K], 0, len(entries))
	for _, e := range entries {
		payload, err := idx.storage.Read(e.Value)
		if err != nil {
			return nil, fmt.Errorf("%w: key=%v rid=%s: %v", ErrIndexStorageMismatch, e.Key, e.Value, err)
		}
		out = append(out, UnclusteredRecord[K]{Key: e.Key, RID: e.Value, Payload: payload})
	}
	return out, nil
}

func (idx *UnclusteredIndex[K]) OrderedScan() ([]UnclusteredRecord[K], error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	entries, err := idx.tree.ItemsE()
	if err != nil {
		return nil, err
	}
	out := make([]UnclusteredRecord[K], 0, len(entries))
	for _, e := range entries {
		payload, err := idx.storage.Read(e.Value)
		if err != nil {
			return nil, fmt.Errorf("%w: key=%v rid=%s: %v", ErrIndexStorageMismatch, e.Key, e.Value, err)
		}
		out = append(out, UnclusteredRecord[K]{Key: e.Key, RID: e.Value, Payload: payload})
	}
	return out, nil
}

func (idx *UnclusteredIndex[K]) Delete(key K, rid heap.RecordID) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	contains, err := idx.tree.ContainsE(key, rid)
	if err != nil {
		return err
	}
	if !contains {
		return ErrNotFound
	}
	payload, err := idx.storage.Read(rid)
	if err != nil {
		return fmt.Errorf("%w: tree references missing rid=%s: %v", ErrIndexStorageMismatch, rid, err)
	}
	derived, err := idx.keyOf(payload)
	if err != nil {
		return err
	}
	if derived != key {
		return fmt.Errorf("%w: rid=%s payload key differs from tree key", ErrIndexStorageMismatch, rid)
	}

	if err := idx.tree.Delete(key, rid); err != nil {
		return err
	}
	if err := idx.storage.Delete(rid); err != nil {
		if rollbackErr := idx.tree.Insert(key, rid); rollbackErr != nil {
			return fmt.Errorf("bplus: heap delete failed: %v; tree rollback failed: %v", err, rollbackErr)
		}
		return err
	}
	return nil
}

func (idx *UnclusteredIndex[K]) TreeStats() TreeStats {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.tree.Stats()
}

func (idx *UnclusteredIndex[K]) Stats() (UnclusteredStats, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	hs, err := idx.storage.Stats()
	if err != nil {
		return UnclusteredStats{}, err
	}
	ts, err := idx.tree.StatsE()
	if err != nil {
		return UnclusteredStats{}, err
	}
	return UnclusteredStats{Tree: ts, Storage: hs}, nil
}

func (idx *UnclusteredIndex[K]) Validate() error {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if err := idx.tree.Validate(); err != nil {
		return err
	}

	type pair struct {
		key K
		rid heap.RecordID
	}
	expected := make(map[pair]struct{})
	var extractErr error
	err := idx.storage.Scan(func(rid heap.RecordID, payload []byte) bool {
		key, err := idx.keyOf(payload)
		if err != nil {
			extractErr = err
			return false
		}
		expected[pair{key: key, rid: rid}] = struct{}{}
		return true
	})
	if err != nil {
		return err
	}
	if extractErr != nil {
		return extractErr
	}

	items, err := idx.tree.ItemsE()
	if err != nil {
		return err
	}
	if len(items) != len(expected) {
		return fmt.Errorf("%w: heap has %d records, tree has %d entries", ErrIndexStorageMismatch, len(expected), len(items))
	}
	for _, item := range items {
		p := pair{key: item.Key, rid: item.Value}
		if _, ok := expected[p]; !ok {
			return fmt.Errorf("%w: tree contains key=%v rid=%s not present in heap", ErrIndexStorageMismatch, item.Key, item.Value)
		}
		delete(expected, p)
	}
	if len(expected) != 0 {
		return fmt.Errorf("%w: %d heap records are not indexed", ErrIndexStorageMismatch, len(expected))
	}
	return nil
}
