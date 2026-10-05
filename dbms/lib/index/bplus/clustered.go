package bplus

import (
	"bytes"
	"fmt"
	"sync"

	"github.com/dbms-go/v2/dbms/lib/storage"
)

type KeyedTupleStorage interface {
	storage.KeyedStorage
	Scan(func(storage.Tuple) bool) error
}

type ClusteredIndex struct {
	mu      sync.RWMutex
	tree    *Tree[uint8]
	storage KeyedTupleStorage
	table   storage.Table
	order   int
}

func NewClusteredIndex(order int, s KeyedTupleStorage, table storage.Table) (*ClusteredIndex, error) {
	if s == nil {
		return nil, fmt.Errorf("bplus: nil sequential storage")
	}
	if err := table.Validate(); err != nil {
		return nil, err
	}
	tree, err := New[uint8](order)
	if err != nil {
		return nil, err
	}
	idx := &ClusteredIndex{tree: tree, storage: s, table: table, order: order}
	if err := idx.Rebuild(); err != nil {
		return nil, err
	}
	return idx, nil
}

func (idx *ClusteredIndex) encodePK(key storage.Tuple) ([]byte, error) {
	return idx.table.EncodeKey(key)
}

func (idx *ClusteredIndex) Rebuild() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	entries := make([]Entry[uint8], 0)
	var scanErr error
	if err := idx.storage.Scan(func(tp storage.Tuple) bool {
		key := idx.table.KeyOf(tp)
		enc, err := idx.encodePK(key)
		if err != nil {
			scanErr = err
			return false
		}
		entries = append(entries, Entry[uint8]{Key: enc, Value: 1})
		return true
	}); err != nil {
		return err
	}
	if scanErr != nil {
		return scanErr
	}
	next, err := New[uint8](idx.order)
	if err != nil {
		return err
	}
	if err := next.BulkLoad(entries); err != nil {
		return err
	}
	if err := next.Validate(); err != nil {
		return err
	}
	idx.tree = next
	return nil
}

func (idx *ClusteredIndex) Insert(tp storage.Tuple) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if err := idx.table.CheckTuple(tp); err != nil {
		return err
	}
	key := idx.table.KeyOf(tp)
	enc, err := idx.encodePK(key)
	if err != nil {
		return err
	}
	if len(idx.tree.Search(enc)) != 0 {
		return ErrDuplicatePrimaryKey
	}
	if err := idx.storage.Insert(tp); err != nil {
		return err
	}
	if err := idx.tree.Insert(enc, 1); err != nil {
		if rb := idx.storage.Delete(key); rb != nil {
			return fmt.Errorf("bplus: tree insert failed: %v; sequential rollback failed: %v", err, rb)
		}
		return err
	}
	return nil
}

func (idx *ClusteredIndex) Search(key storage.Tuple) (storage.Tuple, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	enc, err := idx.encodePK(key)
	if err != nil {
		return nil, err
	}
	if len(idx.tree.Search(enc)) == 0 {
		return nil, ErrNotFound
	}
	tp, err := idx.storage.Search(key)
	if err != nil {
		return nil, fmt.Errorf("%w: key=%v exists in tree but storage search failed: %v", ErrStorageMismatch, key, err)
	}
	return tp, nil
}

func (idx *ClusteredIndex) Delete(key storage.Tuple) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	enc, err := idx.encodePK(key)
	if err != nil {
		return err
	}
	if len(idx.tree.Search(enc)) == 0 {
		return ErrNotFound
	}
	if _, err := idx.storage.Search(key); err != nil {
		return fmt.Errorf("%w: tree contains key=%v absent from sequential storage: %v", ErrStorageMismatch, key, err)
	}
	if err := idx.tree.Delete(enc, 1); err != nil {
		return err
	}
	if err := idx.storage.Delete(key); err != nil {
		if rb := idx.tree.Insert(enc, 1); rb != nil {
			return fmt.Errorf("bplus: sequential delete failed: %v; tree rollback failed: %v", err, rb)
		}
		return err
	}
	return nil
}

func (idx *ClusteredIndex) RangeSearch(low, high storage.Tuple) ([]storage.Tuple, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	lo, err := idx.encodePK(low)
	if err != nil {
		return nil, err
	}
	hi, err := idx.encodePK(high)
	if err != nil {
		return nil, err
	}
	entries, err := idx.tree.RangeSearch(lo, hi)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return []storage.Tuple{}, nil
	}

	out := make([]storage.Tuple, 0, len(entries))
	if err := idx.storage.Scan(func(tp storage.Tuple) bool {
		enc, e := idx.encodePK(idx.table.KeyOf(tp))
		if e != nil {
			err = e
			return false
		}
		if bytes.Compare(enc, lo) >= 0 && bytes.Compare(enc, hi) <= 0 {
			out = append(out, tp)
		}
		return true
	}); err != nil {
		return nil, err
	}
	return out, err
}

func (idx *ClusteredIndex) Validate() error {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if err := idx.tree.Validate(); err != nil {
		return err
	}
	expected := make(map[string]struct{})
	var scanErr error
	if err := idx.storage.Scan(func(tp storage.Tuple) bool {
		enc, err := idx.encodePK(idx.table.KeyOf(tp))
		if err != nil {
			scanErr = err
			return false
		}
		expected[string(enc)] = struct{}{}
		return true
	}); err != nil {
		return err
	}
	if scanErr != nil {
		return scanErr
	}
	for _, e := range idx.tree.Items() {
		if _, ok := expected[string(e.Key)]; !ok {
			return fmt.Errorf("%w: indexed key %x absent from sequential storage", ErrStorageMismatch, e.Key)
		}
		delete(expected, string(e.Key))
	}
	if len(expected) != 0 {
		return fmt.Errorf("%w: sequential storage has unindexed rows", ErrStorageMismatch)
	}
	return nil
}

func (idx *ClusteredIndex) TreeStats() TreeStats {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.tree.Stats()
}
