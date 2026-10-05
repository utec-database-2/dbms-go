package bplus

import (
	"bytes"
	"fmt"
	"sync"

	"github.com/dbms-go/v2/dbms/lib/storage"
)

// RIDTupleStorage is the small capability set required by an unclustered B+
// index. HeapFile should satisfy this interface directly or through a tiny
// adapter. It intentionally uses the project's storage.RID and Tuple types.
type RIDTupleStorage interface {
	storage.FileStorage[storage.Tuple]
	Scan(func(storage.RID, storage.Tuple) bool) error
}

// UnclusteredRecord is one row reached through secondary-key -> RID -> HeapFile.
type UnclusteredRecord struct {
	Key   any
	RID   storage.RID
	Tuple storage.Tuple
}

type UnclusteredIndex struct {
	mu      sync.RWMutex
	tree    *Tree[storage.RID]
	storage RIDTupleStorage
	keyOf   storage.KeyExtractor
	order   int
}

func NewUnclusteredIndex(order int, h RIDTupleStorage, keyOf storage.KeyExtractor) (*UnclusteredIndex, error) {
	if h == nil {
		return nil, fmt.Errorf("bplus: nil heap storage")
	}
	if keyOf == nil {
		return nil, fmt.Errorf("bplus: nil key extractor")
	}
	tree, err := New[storage.RID](order)
	if err != nil {
		return nil, err
	}
	idx := &UnclusteredIndex{tree: tree, storage: h, keyOf: keyOf, order: order}
	if err := idx.Rebuild(); err != nil {
		return nil, err
	}
	return idx, nil
}

func encodedKey(key any) ([]byte, error) { return storage.EncodeKey(key) }

func (idx *UnclusteredIndex) Rebuild() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	entries := make([]Entry[storage.RID], 0)
	var scanErr error
	if err := idx.storage.Scan(func(rid storage.RID, tp storage.Tuple) bool {
		key, err := idx.keyOf(tp)
		if err != nil {
			scanErr = fmt.Errorf("bplus: extract key from RID %+v: %w", rid, err)
			return false
		}
		enc, err := encodedKey(key)
		if err != nil {
			scanErr = err
			return false
		}
		entries = append(entries, Entry[storage.RID]{Key: enc, Value: rid})
		return true
	}); err != nil {
		return err
	}
	if scanErr != nil {
		return scanErr
	}

	next, err := New[storage.RID](idx.order)
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

func (idx *UnclusteredIndex) Insert(tp storage.Tuple) (storage.RID, error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	key, err := idx.keyOf(tp)
	if err != nil {
		return storage.RID{}, err
	}
	enc, err := encodedKey(key)
	if err != nil {
		return storage.RID{}, err
	}
	rid, err := idx.storage.Insert(tp)
	if err != nil {
		return storage.RID{}, err
	}
	if err := idx.tree.Insert(enc, rid); err != nil {
		if rb := idx.storage.Delete(rid); rb != nil {
			return storage.RID{}, fmt.Errorf("bplus: tree insert failed: %v; heap rollback failed: %v", err, rb)
		}
		return storage.RID{}, err
	}
	return rid, nil
}

func (idx *UnclusteredIndex) Search(key any) ([]UnclusteredRecord, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	enc, err := encodedKey(key)
	if err != nil {
		return nil, err
	}
	rids := idx.tree.Search(enc)
	out := make([]UnclusteredRecord, 0, len(rids))
	for _, rid := range rids {
		tp, err := idx.storage.Get(rid)
		if err != nil {
			return nil, fmt.Errorf("%w: key=%v rid=%+v: %v", ErrStorageMismatch, key, rid, err)
		}
		out = append(out, UnclusteredRecord{Key: key, RID: rid, Tuple: tp})
	}
	return out, nil
}

func (idx *UnclusteredIndex) RangeSearch(low, high any) ([]UnclusteredRecord, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	lo, err := encodedKey(low)
	if err != nil {
		return nil, err
	}
	hi, err := encodedKey(high)
	if err != nil {
		return nil, err
	}
	entries, err := idx.tree.RangeSearch(lo, hi)
	if err != nil {
		return nil, err
	}
	out := make([]UnclusteredRecord, 0, len(entries))
	for _, e := range entries {
		tp, err := idx.storage.Get(e.Value)
		if err != nil {
			return nil, fmt.Errorf("%w: rid=%+v: %v", ErrStorageMismatch, e.Value, err)
		}
		key, err := idx.keyOf(tp)
		if err != nil {
			return nil, err
		}
		out = append(out, UnclusteredRecord{Key: key, RID: e.Value, Tuple: tp})
	}
	return out, nil
}

func (idx *UnclusteredIndex) Delete(key any, rid storage.RID) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	enc, err := encodedKey(key)
	if err != nil {
		return err
	}
	if !idx.tree.Contains(enc, rid) {
		return ErrNotFound
	}
	tp, err := idx.storage.Get(rid)
	if err != nil {
		return fmt.Errorf("%w: tree references missing RID %+v: %v", ErrStorageMismatch, rid, err)
	}
	derived, err := idx.keyOf(tp)
	if err != nil {
		return err
	}
	derivedEnc, err := encodedKey(derived)
	if err != nil {
		return err
	}
	if !bytes.Equal(enc, derivedEnc) {
		return fmt.Errorf("%w: RID %+v tuple key differs from index key", ErrStorageMismatch, rid)
	}

	if err := idx.tree.Delete(enc, rid); err != nil {
		return err
	}
	if err := idx.storage.Delete(rid); err != nil {
		if rb := idx.tree.Insert(enc, rid); rb != nil {
			return fmt.Errorf("bplus: heap delete failed: %v; tree rollback failed: %v", err, rb)
		}
		return err
	}
	return nil
}

// Update handles both ordinary updates and changes of the indexed key.
func (idx *UnclusteredIndex) Update(rid storage.RID, next storage.Tuple) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	old, err := idx.storage.Get(rid)
	if err != nil {
		return err
	}
	oldKey, err := idx.keyOf(old)
	if err != nil {
		return err
	}
	newKey, err := idx.keyOf(next)
	if err != nil {
		return err
	}
	oldEnc, err := encodedKey(oldKey)
	if err != nil {
		return err
	}
	newEnc, err := encodedKey(newKey)
	if err != nil {
		return err
	}
	if !idx.tree.Contains(oldEnc, rid) {
		return fmt.Errorf("%w: RID %+v is not indexed under its current key", ErrStorageMismatch, rid)
	}

	if err := idx.storage.Update(rid, next); err != nil {
		return err
	}
	if bytes.Equal(oldEnc, newEnc) {
		return nil
	}
	if err := idx.tree.Delete(oldEnc, rid); err != nil {
		_ = idx.storage.Update(rid, old)
		return err
	}
	if err := idx.tree.Insert(newEnc, rid); err != nil {
		_ = idx.tree.Insert(oldEnc, rid)
		_ = idx.storage.Update(rid, old)
		return err
	}
	return nil
}

func (idx *UnclusteredIndex) TreeStats() TreeStats {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.tree.Stats()
}

func (idx *UnclusteredIndex) Validate() error {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if err := idx.tree.Validate(); err != nil {
		return err
	}

	expected := make(map[string]map[storage.RID]struct{})
	var scanErr error
	if err := idx.storage.Scan(func(rid storage.RID, tp storage.Tuple) bool {
		key, err := idx.keyOf(tp)
		if err != nil {
			scanErr = err
			return false
		}
		enc, err := encodedKey(key)
		if err != nil {
			scanErr = err
			return false
		}
		s := string(enc)
		if expected[s] == nil {
			expected[s] = make(map[storage.RID]struct{})
		}
		expected[s][rid] = struct{}{}
		return true
	}); err != nil {
		return err
	}
	if scanErr != nil {
		return scanErr
	}

	items := idx.tree.Items()
	for _, item := range items {
		bucket := expected[string(item.Key)]
		if _, ok := bucket[item.Value]; !ok {
			return fmt.Errorf("%w: tree contains key=%x RID=%+v absent from heap", ErrStorageMismatch, item.Key, item.Value)
		}
		delete(bucket, item.Value)
		if len(bucket) == 0 {
			delete(expected, string(item.Key))
		}
	}
	if len(expected) != 0 {
		return fmt.Errorf("%w: heap contains rows missing from B+ index", ErrStorageMismatch)
	}
	return nil
}

// InsertRID registra la entrada (key, rid) sin escribir la fila en el heap.
//
// A diferencia de Insert, que es dueño del almacenamiento, este método sirve
// para cuando la tabla ya escribió la fila y solo necesita publicarla en el
// índice: es el que usa el motor SQL, que hace responsable al heap de la
// escritura de la fila y a los índices únicamente del mapeo clave -> RID.
func (idx *UnclusteredIndex) InsertRID(key any, rid storage.RID) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	enc, err := encodedKey(key)
	if err != nil {
		return err
	}
	if idx.tree.Contains(enc, rid) {
		return fmt.Errorf("%w: RID %+v ya está indexado bajo la clave %v", ErrStorageMismatch, rid, key)
	}
	return idx.tree.Insert(enc, rid)
}

// RemoveRID quita la entrada del índice sin borrar la fila del heap. La clave
// vigente se deriva de la fila que el heap todavía conserva.
func (idx *UnclusteredIndex) RemoveRID(rid storage.RID) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	tp, err := idx.storage.Get(rid)
	if err != nil {
		return fmt.Errorf("%w: no se puede desindexar el RID %+v: %v", ErrStorageMismatch, rid, err)
	}
	key, err := idx.keyOf(tp)
	if err != nil {
		return err
	}
	enc, err := encodedKey(key)
	if err != nil {
		return err
	}
	if !idx.tree.Contains(enc, rid) {
		return ErrNotFound
	}
	return idx.tree.Delete(enc, rid)
}

// ReindexRID mueve la entrada de rid a la clave de next sin tocar el heap.
// oldKey y newKey se derivan de las versiones previa y nueva de la fila.
func (idx *UnclusteredIndex) ReindexRID(rid storage.RID, oldKey, newKey any) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	oldEnc, err := encodedKey(oldKey)
	if err != nil {
		return err
	}
	newEnc, err := encodedKey(newKey)
	if err != nil {
		return err
	}
	if bytes.Equal(oldEnc, newEnc) {
		return nil
	}
	if !idx.tree.Contains(oldEnc, rid) {
		return fmt.Errorf("%w: RID %+v no está indexado bajo su clave actual", ErrStorageMismatch, rid)
	}
	if err := idx.tree.Delete(oldEnc, rid); err != nil {
		return err
	}
	if err := idx.tree.Insert(newEnc, rid); err != nil {
		if rb := idx.tree.Insert(oldEnc, rid); rb != nil {
			return fmt.Errorf("bplus: reindex falló: %v; reversión falló: %v", err, rb)
		}
		return err
	}
	return nil
}
