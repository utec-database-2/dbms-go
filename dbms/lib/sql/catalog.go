package sql

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/dbms-go/v2/dbms/lib/storage"
)

// IndexKind distingue los tres esquemas de indexación que expone el motor.
type IndexKind int

const (
	// ClusteredIndexKind: índice agrupado. El orden físico de las filas lo
	// impone el SequentialFile, así que el índice B+ solo marca las claves
	// presentes y sirve para validar unicidad y detectar rangos vacíos.
	ClusteredIndexKind IndexKind = iota
	// UnclusteredIndexKind: índice no agrupado sobre un HeapFile. El B+ guarda
	// clave -> RID y el heap aporta la fila.
	UnclusteredIndexKind
	// HashIndexKind: hash dinámico (extendible hashing) sobre un HeapFile.
	HashIndexKind
	// RTreeIndexKind: R-Tree sobre una columna POINT (consultas espaciales).
	RTreeIndexKind
)

func (k IndexKind) String() string {
	switch k {
	case ClusteredIndexKind:
		return "clustered-bplus"
	case UnclusteredIndexKind:
		return "unclustered-bplus"
	case HashIndexKind:
		return "extendible-hash"
	case RTreeIndexKind:
		return "r-tree"
	}
	return "unknown"
}

// Index es un indice secundario o primario asociado a una tabla.
// El motor solo depende de esta interfaz, por lo que la implementación
// concreta (B+ no agrupado o extendible hash) se elige al crear el índice.
type Index interface {
	// Name devuelve el nombre con el que se registró el índice.
	Name() string
	// Kind indica qué técnica de indexación se usó.
	Kind() IndexKind
	// Column es el nombre de la columna indexada (o la lista de la PK).
	Column() string
	// Unique indica si el índice rechaza claves repetidas.
	Unique() bool
	// SupportsRange es false para el hash dinámico.
	SupportsRange() bool
	// Insert registra en el índice una fila que la tabla ya escribió en su
	// almacenamiento. El índice no escribe la fila: solo mantiene el mapeo
	// entre la clave y la referencia física (RID en tablas no agrupadas,
	// clave primaria en las agrupadas).
	Insert(t storage.Tuple, rid storage.RID) error
	// SearchExact devuelve las filas con clave igual a key.
	SearchExact(key any) ([]storage.Tuple, error)
	// SearchRange devuelve las filas con clave en [low, high].
	SearchRange(low, high any) ([]storage.Tuple, error)
	// DeleteRID quita la fila identificada por rid de este índice.
	DeleteRID(rid storage.RID, t storage.Tuple) error
	// UpdateRID reubica la entrada de este índice tras un cambio en la fila.
	UpdateRID(rid storage.RID, prev, next storage.Tuple) error
	// Rebuild reconstruye el índice desde el almacenamiento de la tabla.
	Rebuild() error
	// Entries devuelve el número de entradas del índice.
	Entries() int
	// Close libera los recursos del índice.
	Close() error
}

// Table es el catálogo de una tabla: esquema, archivo físico e índices.
type Table struct {
	Schema storage.Table

	// Clustered indica que las filas viven en un SequentialFile (ordenadas por
	// PK). Si es false viven en un HeapFile.
	Clustered bool

	heap *heapAdapter
	seq  *seqAdapter

	indexes map[string]Index
	// order conserva el orden de creación para que el plan sea estable.
	order []string

	mu sync.RWMutex
}

// Name devuelve el nombre de la tabla.
func (t *Table) Name() string { return t.Schema.Name }

// ColumnIndex busca el índice de una columna por nombre (sin distinguir mayúsculas).
func (t *Table) ColumnIndex(name string) (int, bool) {
	for i, c := range t.Schema.Columns {
		if strings.EqualFold(c, name) {
			return i, true
		}
	}
	return 0, false
}

// PrimaryKeyIndex devuelve el índice de la columna que forma la clave primaria.
func (t *Table) PrimaryKeyIndex() (int, bool) {
	if len(t.Schema.KeyCols) == 0 {
		return 0, false
	}
	return t.Schema.KeyCols[0], true
}

// IsPrimaryKeyColumn indica si la columna forma parte de la clave primaria.
func (t *Table) IsPrimaryKeyColumn(col int) bool {
	for _, c := range t.Schema.KeyCols {
		if c == col {
			return true
		}
	}
	return false
}

// Index devuelve el índice registrado con ese nombre.
func (t *Table) Index(name string) (Index, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	idx, ok := t.indexes[strings.ToLower(name)]
	return idx, ok
}

// IndexOnColumn devuelve el primer índice que cubre la columna, si existe.
func (t *Table) IndexOnColumn(col int) (Index, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, name := range t.order {
		idx := t.indexes[name]
		if bi, ok := idx.(interface{ columnIndex() int }); ok && bi.columnIndex() == col {
			return idx, true
		}
	}
	return nil, false
}

// IndexNames lista los índices en orden de creación.
func (t *Table) IndexNames() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return append([]string(nil), t.order...)
}

// dropIndex quita un índice del catálogo. Se usa para deshacer un CREATE INDEX
// que no llegó a registrarse en el catálogo persistido.
func (t *Table) dropIndex(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := strings.ToLower(name)
	if _, ok := t.indexes[key]; !ok {
		return
	}
	delete(t.indexes, key)
	for i, n := range t.order {
		if n == key {
			t.order = append(t.order[:i], t.order[i+1:]...)
			break
		}
	}
}

func (t *Table) addIndex(idx Index) {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := strings.ToLower(idx.Name())
	if _, exists := t.indexes[key]; exists {
		return
	}
	t.indexes[key] = idx
	t.order = append(t.order, key)
}

// columnIndex resuelve la columna indexada a partir del esquema.
func (b *baseIndex) columnIndex() int {
	if b.table == nil {
		return -1
	}
	if c, ok := b.table.ColumnIndex(b.col); ok {
		return c
	}
	return -1
}

// keyOf extrae de la fila el valor de la columna indexada.
func (idx *baseIndex) keyOf(t storage.Tuple) (any, error) {
	c := idx.columnIndex()
	if c < 0 || c >= len(t) {
		return nil, fmt.Errorf("sql: la columna %s no existe en la tabla %s", idx.col, idx.table.Name())
	}
	return t[c], nil
}

// catalog es el conjunto de tablas del motor, protegido por un mutex.
type catalog struct {
	mu     sync.RWMutex
	tables map[string]*Table
	dir    string
}

func newCatalog(dir string) *catalog {
	return &catalog{tables: make(map[string]*Table), dir: dir}
}

func (c *catalog) get(name string) (*Table, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.tables[strings.ToLower(name)]
	return t, ok
}

func (c *catalog) put(t *Table) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := strings.ToLower(t.Name())
	if _, exists := c.tables[key]; exists {
		return fmt.Errorf("sql: la tabla %s ya existe", t.Name())
	}
	c.tables[key] = t
	return nil
}

func (c *catalog) drop(name string) (*Table, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := strings.ToLower(name)
	t, ok := c.tables[key]
	if ok {
		delete(c.tables, key)
	}
	return t, ok
}

func (c *catalog) names() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.tables))
	for _, t := range c.tables {
		out = append(out, t.Name())
	}
	sort.Strings(out)
	return out
}

func (c *catalog) all() []*Table {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*Table, 0, len(c.tables))
	for _, t := range c.tables {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}
