package sql

import (
	"bytes"
	"fmt"
	"sort"
	"sync"

	"github.com/dbms-go/v2/dbms/lib/index/bplus"
	"github.com/dbms-go/v2/dbms/lib/index/common"
	"github.com/dbms-go/v2/dbms/lib/index/extendible"
	"github.com/dbms-go/v2/dbms/lib/storage"
)

// Los índices del motor nunca escriben la fila en el almacenamiento: eso es
// tarea de la tabla (heap file o archivo secuencial). Cada índice solo mantiene
// el mapeo entre la clave y la referencia física, que es un RID en las tablas
// no agrupadas y la clave primaria en las agrupadas.

// baseIndex es la parte común a las implementaciones de Index: el nombre, la
// columna indexada y el acceso a la tabla para extraer la clave.
type baseIndex struct {
	name   string
	kind   IndexKind
	col    string
	unique bool
	table  *Table
}

func (b *baseIndex) Name() string    { return b.name }
func (b *baseIndex) Kind() IndexKind { return b.kind }
func (b *baseIndex) Column() string  { return b.col }
func (b *baseIndex) Unique() bool    { return b.unique }

// ---------------------------------------------------------------------------
// Índice no agrupado: B+ Tree (clave -> RID) sobre un HeapFile
// ---------------------------------------------------------------------------

type unclusteredIndex struct {
	baseIndex
	tree *bplus.UnclusteredIndex
	heap *heapAdapter
}

// newUnclusteredIndex construye el B+ no agrupado y lo puebla con un BulkLoad
// desde el heap, que es la forma barata de construirlo (O(n) en vez de O(n log n)).
func newUnclusteredIndex(name, col string, tbl *Table, unique bool, order int) (Index, error) {
	pos, ok := tbl.ColumnIndex(col)
	if !ok {
		return nil, fmt.Errorf("sql: la columna %s no existe en %s", col, tbl.Name())
	}
	tree, err := bplus.NewUnclusteredIndex(order, tbl.heap, func(t storage.Tuple) (any, error) {
		if pos >= len(t) {
			return nil, fmt.Errorf("sql: fila con %d columnas, se esperaba la %d", len(t), pos)
		}
		return t[pos], nil
	})
	if err != nil {
		return nil, err
	}
	return &unclusteredIndex{
		baseIndex: baseIndex{name: name, kind: UnclusteredIndexKind, col: col, unique: unique, table: tbl},
		tree:      tree,
		heap:      tbl.heap,
	}, nil
}

func (idx *unclusteredIndex) SupportsRange() bool { return true }

// Insert registra la fila ya escrita en el heap bajo rid.
func (idx *unclusteredIndex) Insert(t storage.Tuple, rid storage.RID) error {
	key, err := idx.keyOf(t)
	if err != nil {
		return err
	}
	if idx.unique {
		dup, err := idx.tree.Search(key)
		if err != nil {
			return err
		}
		for _, d := range dup {
			if d.RID != rid {
				return fmt.Errorf("%w: %s=%v", storage.ErrDuplicateKey, idx.col, key)
			}
		}
	}
	return idx.tree.InsertRID(key, rid)
}

func (idx *unclusteredIndex) SearchExact(key any) ([]storage.Tuple, error) {
	recs, err := idx.tree.Search(key)
	if err != nil {
		return nil, err
	}
	out := make([]storage.Tuple, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.Tuple)
	}
	return out, nil
}

func (idx *unclusteredIndex) SearchRange(low, high any) ([]storage.Tuple, error) {
	recs, err := idx.tree.RangeSearch(low, high)
	if err != nil {
		return nil, err
	}
	out := make([]storage.Tuple, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.Tuple)
	}
	return out, nil
}

// DeleteRID desindexa la fila; el borrado en el heap lo hace la tabla.
func (idx *unclusteredIndex) DeleteRID(rid storage.RID, t storage.Tuple) error {
	return idx.tree.RemoveRID(rid)
}

func (idx *unclusteredIndex) UpdateRID(rid storage.RID, prev, next storage.Tuple) error {
	oldKey, err := idx.keyOf(prev)
	if err != nil {
		return err
	}
	newKey, err := idx.keyOf(next)
	if err != nil {
		return err
	}
	return idx.tree.ReindexRID(rid, oldKey, newKey)
}

func (idx *unclusteredIndex) Rebuild() error { return idx.tree.Rebuild() }

func (idx *unclusteredIndex) Entries() int { return idx.tree.TreeStats().Entries }

func (idx *unclusteredIndex) Close() error { return nil }

// ---------------------------------------------------------------------------
// Índice agrupado: B+ Tree sobre el SequentialFile (ordenado por PK)
// ---------------------------------------------------------------------------

type clusteredIndex struct {
	baseIndex
	tree *bplus.ClusteredIndex
	seq  *seqAdapter
}

// newClusteredIndex crea el B+ agrupado, que sí escribe en el secuencial porque
// el índice es quien direcciona la fila por clave primaria.
func newClusteredIndex(name string, tbl *Table, order int) (Index, error) {
	tree, err := bplus.NewClusteredIndex(order, tbl.seq, tbl.Schema)
	if err != nil {
		return nil, err
	}
	col := ""
	if len(tbl.Schema.KeyCols) > 0 {
		if len(tbl.Schema.Columns) > tbl.Schema.KeyCols[0] {
			col = tbl.Schema.Columns[tbl.Schema.KeyCols[0]]
		}
	}
	return &clusteredIndex{
		baseIndex: baseIndex{name: name, kind: ClusteredIndexKind, col: col, unique: true, table: tbl},
		tree:      tree,
		seq:       tbl.seq,
	}, nil
}

func (idx *clusteredIndex) SupportsRange() bool { return true }

// Insert escribe la fila en el secuencial; el RID devuelto es sintético porque
// en una tabla agrupada la fila se direcciona por su clave primaria.
func (idx *clusteredIndex) Insert(t storage.Tuple, _ storage.RID) error {
	return idx.tree.Insert(t)
}

// keyToSlot colapsa la clave a un entero estable para el RID sintético.
func keyToSlot(key any) int32 {
	switch v := key.(type) {
	case int32:
		return v
	case int64:
		return int32(v)
	case bool:
		if v {
			return 1
		}
		return 0
	}
	return 0
}

func (idx *clusteredIndex) SearchExact(key any) ([]storage.Tuple, error) {
	if key == nil {
		return nil, fmt.Errorf("sql: clave nula en el índice agrupado %s", idx.name)
	}
	t, err := idx.seq.Search(storage.Tuple{key})
	if err != nil {
		return nil, nil // ausencia no es error: devuelve cero filas
	}
	return []storage.Tuple{t}, nil
}

func (idx *clusteredIndex) SearchRange(low, high any) ([]storage.Tuple, error) {
	var out []storage.Tuple
	err := idx.seq.Scan(func(tp storage.Tuple) bool {
		key := idx.table.Schema.KeyOf(tp)
		if compareAny(key, low) < 0 {
			return true
		}
		if compareAny(key, high) > 0 {
			return false
		}
		out = append(out, tp)
		return true
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (idx *clusteredIndex) DeleteRID(_ storage.RID, t storage.Tuple) error {
	return idx.seq.Delete(idx.table.Schema.KeyOf(t))
}

func (idx *clusteredIndex) UpdateRID(_ storage.RID, prev, next storage.Tuple) error {
	return idx.seq.Update(idx.table.Schema.KeyOf(prev), next)
}

func (idx *clusteredIndex) Rebuild() error { return idx.tree.Rebuild() }

func (idx *clusteredIndex) Entries() int { return idx.tree.TreeStats().Entries }

func (idx *clusteredIndex) Close() error { return nil }

// ---------------------------------------------------------------------------
// Índices secundarios de una tabla agrupada: B+ (clave -> clave primaria)
// ---------------------------------------------------------------------------

// secondaryClusteredIndex indexa una columna que no es la clave primaria en una
// tabla agrupada. Como no hay RID, la referencia a la fila es la propia clave
// primaria: el índice guarda (columna -> PK) y resuelve la fila con una
// búsqueda en el secuencial.
//
// Las entradas viven en un slice ordenado por la clave codificada, así que la
// búsqueda es binaria y un rango es un barrido acotado. La construcción completa
// (Rebuild) parte de un recorrido del secuencial, que es el equivalente del
// BulkLoad de un B+.
type secondaryClusteredIndex struct {
	baseIndex
	seq     *seqAdapter
	mu      sync.RWMutex
	entries []secondaryEntry
}

type secondaryEntry struct {
	key []byte
	pk  storage.Tuple
}

func newSecondaryClusteredIndex(name, col string, tbl *Table, unique bool, order int) (Index, error) {
	if _, ok := tbl.ColumnIndex(col); !ok {
		return nil, fmt.Errorf("sql: la columna %s no existe en %s", col, tbl.Name())
	}
	idx := &secondaryClusteredIndex{
		baseIndex: baseIndex{name: name, kind: UnclusteredIndexKind, col: col, unique: unique, table: tbl},
		seq:       tbl.seq,
	}
	if err := idx.Rebuild(); err != nil {
		return nil, err
	}
	return idx, nil
}

func (idx *secondaryClusteredIndex) SupportsRange() bool { return true }

func (idx *secondaryClusteredIndex) Insert(t storage.Tuple, _ storage.RID) error {
	key, err := idx.keyOf(t)
	if err != nil {
		return err
	}
	enc := encodeIndexKey(key)
	pk := idx.table.Schema.KeyOf(t)

	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.unique {
		if i := idx.search(enc); i < len(idx.entries) && bytes.Equal(idx.entries[i].key, enc) {
			return fmt.Errorf("%w: %s=%v", storage.ErrDuplicateKey, idx.col, key)
		}
	}
	e := secondaryEntry{key: enc, pk: pk}
	pos := idx.search(enc)
	idx.entries = append(idx.entries, e)
	copy(idx.entries[pos+1:], idx.entries[pos:])
	idx.entries[pos] = e
	return nil
}

// search devuelve el índice de la primera entrada con clave >= enc.
func (idx *secondaryClusteredIndex) search(enc []byte) int {
	return sort.Search(len(idx.entries), func(i int) bool {
		return bytes.Compare(idx.entries[i].key, enc) >= 0
	})
}

func (idx *secondaryClusteredIndex) SearchExact(key any) ([]storage.Tuple, error) {
	enc := encodeIndexKey(key)
	idx.mu.RLock()
	i := idx.search(enc)
	var pks []storage.Tuple
	for i < len(idx.entries) && bytes.Equal(idx.entries[i].key, enc) {
		pks = append(pks, idx.entries[i].pk)
		i++
	}
	idx.mu.RUnlock()

	out := make([]storage.Tuple, 0, len(pks))
	for _, pk := range pks {
		t, err := idx.seq.Search(pk)
		if err != nil {
			// La fila ya no está: el índice quedó desactualizado, se ignora.
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

func (idx *secondaryClusteredIndex) SearchRange(low, high any) ([]storage.Tuple, error) {
	lo, hi := encodeIndexKey(low), encodeIndexKey(high)
	idx.mu.RLock()
	start := idx.search(lo)
	var pks []storage.Tuple
	for i := start; i < len(idx.entries); i++ {
		if bytes.Compare(idx.entries[i].key, hi) > 0 {
			break
		}
		pks = append(pks, idx.entries[i].pk)
	}
	idx.mu.RUnlock()

	out := make([]storage.Tuple, 0, len(pks))
	for _, pk := range pks {
		t, err := idx.seq.Search(pk)
		if err != nil {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

func (idx *secondaryClusteredIndex) DeleteRID(_ storage.RID, t storage.Tuple) error {
	key, err := idx.keyOf(t)
	if err != nil {
		return err
	}
	enc := encodeIndexKey(key)
	pk := idx.table.Schema.KeyOf(t)

	idx.mu.Lock()
	defer idx.mu.Unlock()
	for i, e := range idx.entries {
		if bytes.Equal(e.key, enc) && tupleEqualKey(e.pk, pk) {
			idx.entries = append(idx.entries[:i], idx.entries[i+1:]...)
			return nil
		}
	}
	return nil
}

func (idx *secondaryClusteredIndex) UpdateRID(_ storage.RID, prev, next storage.Tuple) error {
	if err := idx.DeleteRID(storage.RID{}, prev); err != nil {
		return err
	}
	return idx.Insert(next, storage.RID{})
}

func (idx *secondaryClusteredIndex) Rebuild() error {
	entries := make([]secondaryEntry, 0, 64)
	err := idx.seq.Scan(func(tp storage.Tuple) bool {
		key, err := idx.keyOf(tp)
		if err != nil {
			return false
		}
		entries = append(entries, secondaryEntry{key: encodeIndexKey(key), pk: idx.table.Schema.KeyOf(tp)})
		return true
	})
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool {
		return bytes.Compare(entries[i].key, entries[j].key) < 0
	})

	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.entries = entries
	return nil
}

func (idx *secondaryClusteredIndex) Entries() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.entries)
}

func (idx *secondaryClusteredIndex) Close() error { return nil }

func encodeIndexKey(key any) []byte {
	b, err := storage.EncodeKey(key)
	if err != nil {
		return nil
	}
	return b
}

func tupleEqualKey(a, b storage.Tuple) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if compareAny(a[i], b[i]) != 0 {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Índice hash dinámico: Extendible Hashing sobre un HeapFile
// ---------------------------------------------------------------------------

type hashIndex struct {
	baseIndex
	idx    *extendible.Index
	heap   *heapAdapter
	bucket int
}

func newHashIndex(name, col string, tbl *Table, unique bool, bucketSize int) (Index, error) {
	if _, ok := tbl.ColumnIndex(col); ok {
		// sigue
	} else {
		return nil, fmt.Errorf("sql: la columna %s no existe en %s", col, tbl.Name())
	}
	if bucketSize < 2 {
		bucketSize = 2
	}
	h := &hashIndex{
		baseIndex: baseIndex{name: name, kind: HashIndexKind, col: col, unique: unique, table: tbl},
		idx:       extendible.New(bucketSize),
		heap:      tbl.heap,
		bucket:    bucketSize,
	}
	if err := h.Rebuild(); err != nil {
		return nil, err
	}
	return h, nil
}

func (h *hashIndex) SupportsRange() bool { return false }

// Insert registra el RID de la fila ya escrita en el heap.
func (h *hashIndex) Insert(t storage.Tuple, rid storage.RID) error {
	key, err := h.keyOf(t)
	if err != nil {
		return err
	}
	if h.unique {
		rids, err := h.idx.Search(key)
		if err != nil {
			return err
		}
		for _, r := range rids {
			if r != rid {
				return fmt.Errorf("%w: %s=%v", storage.ErrDuplicateKey, h.col, key)
			}
		}
	}
	return h.idx.Insert(key, rid)
}

func (h *hashIndex) SearchExact(key any) ([]storage.Tuple, error) {
	rids, err := h.idx.Search(key)
	if err != nil {
		return nil, err
	}
	out := make([]storage.Tuple, 0, len(rids))
	for _, rid := range rids {
		t, err := h.heap.Get(rid)
		if err != nil {
			// Fila borrada con el índice sin actualizar: se ignora en vez de
			// abortar toda la consulta.
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

func (h *hashIndex) SearchRange(low, high any) ([]storage.Tuple, error) {
	return nil, common.ErrRangeNotSupported
}

func (h *hashIndex) DeleteRID(rid storage.RID, t storage.Tuple) error {
	key, err := h.keyOf(t)
	if err != nil {
		return err
	}
	_, err = h.idx.Delete(key, rid)
	return err
}

// UpdateRID reubica la entrada: el heap ya fue actualizado por la tabla.
func (h *hashIndex) UpdateRID(rid storage.RID, prev, next storage.Tuple) error {
	oldKey, err := h.keyOf(prev)
	if err != nil {
		return err
	}
	newKey, err := h.keyOf(next)
	if err != nil {
		return err
	}
	if compareAny(oldKey, newKey) == 0 {
		return nil
	}
	if _, err := h.idx.Delete(oldKey, rid); err != nil {
		return err
	}
	return h.idx.Insert(newKey, rid)
}

func (h *hashIndex) Rebuild() error {
	next := extendible.New(h.bucket)
	err := h.heap.Scan(func(rid storage.RID, t storage.Tuple) bool {
		key, err := h.keyOf(t)
		if err != nil {
			return false
		}
		_ = next.Insert(key, rid)
		return true
	})
	if err != nil {
		return err
	}
	h.idx = next
	return nil
}

func (h *hashIndex) Entries() int { return h.idx.Stats().Records }

func (h *hashIndex) Close() error { return nil }
