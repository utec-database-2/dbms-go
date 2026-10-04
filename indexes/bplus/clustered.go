package bplus

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/dbms-go/v2/dbms/lib/concurrency/sequential"
)

var ErrIndexStorageMismatch = errors.New("bplus: index/storage mismatch")

type ClusteredRecord struct {
	Key     int64
	RID     sequential.RecordID
	Payload []byte
}

type ClusteredStats struct {
	Tree    TreeStats
	Storage sequential.Stats
}

// ClusteredIndex 
// El SeqFile conserva los payloads ordenados físicamente por la misma clave.
// El archivo *.idx conserva únicamente la estructura B+ y pares clave -> RID.
// Cerrar el proceso ya no destruye el árbol: OpenClusteredIndex lo reabre desde
// la raíz persistida sin ejecutar ScanRecords ni BulkLoad. esto era la observación del profesor
// ya que anteriormente solo se enontraba en el RAM
type ClusteredIndex struct {
	mu      sync.RWMutex
	tree    *Tree[int64, sequential.RecordID]
	storage *sequential.SeqFile
	order   int
}

// CreateClusteredIndex crea/trunca indexPath y construye el índice a partir de
// los registros actualmente vivos del SeqFile. Es la operación equivalente a
// CREATE INDEX inicial.
func CreateClusteredIndex(indexPath string, order int, storage *sequential.SeqFile) (*ClusteredIndex, error) {
	if storage == nil {
		return nil, fmt.Errorf("bplus: nil sequential storage")
	}
	tree, err := Create[int64, sequential.RecordID](indexPath, order)
	if err != nil {
		return nil, err
	}
	idx := &ClusteredIndex{tree: tree, storage: storage, order: order}
	if err := idx.Rebuild(); err != nil {
		tree.Close()
		return nil, err
	}
	return idx, nil
}

// OpenClusteredIndex reabre un índice persistido. // abre el archivo del índice y lee su metadata.
func OpenClusteredIndex(indexPath string, storage *sequential.SeqFile) (*ClusteredIndex, error) {
	if storage == nil {
		return nil, fmt.Errorf("bplus: nil sequential storage")
	}
	tree, err := Open[int64, sequential.RecordID](indexPath)
	if err != nil {
		return nil, err
	}
	return &ClusteredIndex{tree: tree, storage: storage, order: tree.Order()}, nil
}

// OpenOrCreateClusteredIndex abre el índice si ya existe. Si no existe, lo
// crea y hace un Rebuild inicial desde el SeqFile una sola vez.
func OpenOrCreateClusteredIndex(indexPath string, order int, storage *sequential.SeqFile) (*ClusteredIndex, error) {
	if storage == nil {
		return nil, fmt.Errorf("bplus: nil sequential storage")
	}
	_, statErr := os.Stat(indexPath)
	existed := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}

	tree, err := OpenOrCreate[int64, sequential.RecordID](indexPath, order)
	if err != nil {
		return nil, err
	}
	idx := &ClusteredIndex{tree: tree, storage: storage, order: tree.Order()}
	if !existed {
		if err := idx.Rebuild(); err != nil {
			tree.Close()
			return nil, err
		}
	}
	return idx, nil
}

//	NewClusteredIndex(order, storage, "employee_id_clustered.idx")
func NewClusteredIndex(order int, storage *sequential.SeqFile, indexPath ...string) (*ClusteredIndex, error) {
	if len(indexPath) > 1 {
		return nil, fmt.Errorf("bplus: expected at most one index path")
	}
	if len(indexPath) == 1 {
		return OpenOrCreateClusteredIndex(indexPath[0], order, storage)
	}
	if storage == nil {
		return nil, fmt.Errorf("bplus: nil sequential storage")
	}
	tree, err := New[int64, sequential.RecordID](order)
	if err != nil {
		return nil, err
	}
	idx := &ClusteredIndex{tree: tree, storage: storage, order: order}
	if err := idx.Rebuild(); err != nil {
		tree.Close()
		return nil, err
	}
	return idx, nil
}

func (idx *ClusteredIndex) IndexPath() string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.tree.Path()
}

func (idx *ClusteredIndex) Close() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.tree.Close()
}

// Rebuild reconstruye explícitamente el archivo B+ a partir del SeqFile.

func (idx *ClusteredIndex) Rebuild() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	records, err := idx.storage.ScanRecords()
	if err != nil {
		return err
	}
	entries := make([]Entry[int64, sequential.RecordID], 0, len(records))
	for _, r := range records {
		entries = append(entries, Entry[int64, sequential.RecordID]{Key: r.Key, Value: r.RID})
	}
	if err := idx.tree.BulkLoad(entries); err != nil {
		return err
	}
	return idx.tree.Validate()
}

func (idx *ClusteredIndex) Insert(key int64, payload []byte) (sequential.RecordID, error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	rid, err := idx.storage.InsertRecord(key, payload)
	if err != nil {
		return 0, err
	}
	if err := idx.tree.Insert(key, rid); err != nil {
		rollbackErr := idx.storage.DeleteRID(rid)
		if rollbackErr != nil {
			return 0, fmt.Errorf("bplus: tree insert failed: %v; storage rollback failed: %v", err, rollbackErr)
		}
		return 0, err
	}
	return rid, nil
}

func (idx *ClusteredIndex) Search(key int64) ([]ClusteredRecord, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	rids, err := idx.tree.SearchE(key)
	if err != nil {
		return nil, err
	}
	out := make([]ClusteredRecord, 0, len(rids))
	for _, rid := range rids {
		payload, err := idx.storage.Read(rid)
		if err != nil {
			return nil, fmt.Errorf("%w: key=%d rid=%s: %v", ErrIndexStorageMismatch, key, rid, err)
		}
		out = append(out, ClusteredRecord{Key: key, RID: rid, Payload: payload})
	}
	return out, nil
}

func (idx *ClusteredIndex) RangeSearch(low, high int64) ([]ClusteredRecord, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	entries, err := idx.tree.RangeSearch(low, high)
	if err != nil {
		return nil, err
	}
	out := make([]ClusteredRecord, 0, len(entries))
	for _, e := range entries {
		payload, err := idx.storage.Read(e.Value)
		if err != nil {
			return nil, fmt.Errorf("%w: key=%d rid=%s: %v", ErrIndexStorageMismatch, e.Key, e.Value, err)
		}
		out = append(out, ClusteredRecord{Key: e.Key, RID: e.Value, Payload: payload})
	}
	return out, nil
}

// OrderedScan recorre las hojas enlazadas del B+ persistente y resuelve cada
// RID contra el SeqFile.
func (idx *ClusteredIndex) OrderedScan() ([]ClusteredRecord, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	entries, err := idx.tree.ItemsE()
	if err != nil {
		return nil, err
	}
	out := make([]ClusteredRecord, 0, len(entries))
	for _, e := range entries {
		payload, err := idx.storage.Read(e.Value)
		if err != nil {
			return nil, fmt.Errorf("%w: key=%d rid=%s: %v", ErrIndexStorageMismatch, e.Key, e.Value, err)
		}
		out = append(out, ClusteredRecord{Key: e.Key, RID: e.Value, Payload: payload})
	}
	return out, nil
}

// Delete elimina exactamente el registro identificado por (key, rid).
func (idx *ClusteredIndex) Delete(key int64, rid sequential.RecordID) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	contains, err := idx.tree.ContainsE(key, rid)
	if err != nil {
		return err
	}
	if !contains {
		return ErrNotFound
	}
	if _, err := idx.storage.Read(rid); err != nil {
		return fmt.Errorf("%w: tree references missing rid=%s: %v", ErrIndexStorageMismatch, rid, err)
	}

	if err := idx.tree.Delete(key, rid); err != nil {
		return err
	}
	if err := idx.storage.DeleteRID(rid); err != nil {
		if rollbackErr := idx.tree.Insert(key, rid); rollbackErr != nil {
			return fmt.Errorf("bplus: storage delete failed: %v; tree rollback failed: %v", err, rollbackErr)
		}
		return err
	}
	return nil
}

func (idx *ClusteredIndex) TreeStats() TreeStats {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.tree.Stats()
}

func (idx *ClusteredIndex) Stats() ClusteredStats {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return ClusteredStats{Tree: idx.tree.Stats(), Storage: idx.storage.Stats()}
}

// Validate comprueba las invariantes on-disk del B+ y, de forma explícita,
// también la correspondencia uno-a-uno contra los registros vivos del SeqFile.
func (idx *ClusteredIndex) Validate() error {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if err := idx.tree.Validate(); err != nil {
		return err
	}
	records, err := idx.storage.ScanRecords()
	if err != nil {
		return err
	}
	items, err := idx.tree.ItemsE()
	if err != nil {
		return err
	}
	if len(records) != len(items) {
		return fmt.Errorf("%w: storage has %d records, tree has %d entries", ErrIndexStorageMismatch, len(records), len(items))
	}

	type pair struct {
		key int64
		rid sequential.RecordID
	}
	expected := make(map[pair]struct{}, len(records))
	for _, r := range records {
		expected[pair{key: r.Key, rid: r.RID}] = struct{}{}
	}
	for _, item := range items {
		p := pair{key: item.Key, rid: item.Value}
		if _, ok := expected[p]; !ok {
			return fmt.Errorf("%w: tree contains key=%d rid=%s not present in storage", ErrIndexStorageMismatch, item.Key, item.Value)
		}
		delete(expected, p)
	}
	if len(expected) != 0 {
		return fmt.Errorf("%w: %d storage records are not indexed", ErrIndexStorageMismatch, len(expected))
	}
	return nil
}
