package bplus

import (
	"errors"
	"fmt"
	"sync"

	"github.com/dbms-go/v2/storage/sequential"
)

var ErrIndexStorageMismatch = errors.New("bplus: index/storage mismatch")

// ClusteredRecord es un registro resuelto desde el B+ agrupado.
type ClusteredRecord struct {
	Key     int64
	RID     sequential.RecordID
	Payload []byte
}

// ClusteredStats combina métricas del árbol y del archivo secuencial.
type ClusteredStats struct {
	Tree    TreeStats
	Storage sequential.Stats
}

// ClusteredIndex implementa el B+ agrupado del proyecto.
//
// La clave del B+ es la misma clave que determina el orden físico del SeqFile.
// El árbol guarda RIDs lógicos estables y los payloads viven únicamente en el
// archivo secuencial, evitando duplicar los datos dentro del índice.
//
// El árbol B+ se mantiene en memoria y se reconstruye al abrir usando
// SeqFile.ScanRecords(). Los datos y RIDs sí son persistentes.
type ClusteredIndex struct {
	mu      sync.RWMutex
	tree    *Tree[int64, sequential.RecordID]
	storage *sequential.SeqFile
	order   int
}

func NewClusteredIndex(order int, storage *sequential.SeqFile) (*ClusteredIndex, error) {
	if storage == nil {
		return nil, fmt.Errorf("bplus: nil sequential storage")
	}
	tree, err := New[int64, sequential.RecordID](order)
	if err != nil {
		return nil, err
	}
	idx := &ClusteredIndex{tree: tree, storage: storage, order: order}
	if err := idx.Rebuild(); err != nil {
		return nil, err
	}
	return idx, nil
}

// Rebuild reconstruye el árbol desde el almacenamiento persistente.
// Construye primero un árbol temporal y solo lo publica si todo termina bien.
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

	next, err := New[int64, sequential.RecordID](idx.order)
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

	rids := idx.tree.Search(key)
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

// OrderedScan devuelve todos los registros en orden de clave del B+.
// Sirve como base para planes ORDER BY que puedan aprovechar el índice.
func (idx *ClusteredIndex) OrderedScan() ([]ClusteredRecord, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	entries := idx.tree.Items()
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
// Se retira primero del árbol; si el storage falla, se reinsertará la entrada
// en el árbol para no dejar un registro vivo sin índice.
func (idx *ClusteredIndex) Delete(key int64, rid sequential.RecordID) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if !idx.tree.Contains(key, rid) {
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

// Validate comprueba tanto las invariantes del B+ como la correspondencia
// uno-a-uno entre registros vivos del SeqFile y entradas (key,RID) del índice.
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
	items := idx.tree.Items()
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
