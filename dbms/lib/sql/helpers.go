package sql

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dbms-go/v2/dbms/lib/storage"
	"github.com/dbms-go/v2/dbms/lib/storage/heap"
	"github.com/dbms-go/v2/dbms/lib/storage/sequential"
)

// dbDir devuelve el directorio donde viven los archivos de datos.
func (e *Engine) dbDir() string {
	if e.opt.Dir == "" {
		return "."
	}
	return e.opt.Dir
}

// tablePath devuelve la ruta base de los archivos de una tabla.
func (e *Engine) tablePath(name string) string {
	return filepath.Join(e.dbDir(), strings.ToLower(name))
}

// heapOptions traduce las opciones del motor a las del heap file.
func heapOptions(opt Options) heap.Options {
	return heap.Options{
		PageSize: opt.PageSize,
		SlotSize: opt.SlotSize,
		Strategy: heap.Strategy(opt.HeapStrategy),
		FileID:   0,
	}
}

// sequentialOptions traduce las opciones del motor a las del secuencial.
// RebuildMin fija el mínimo de slots auxiliares o tombstones que dispara una
// reconstrucción: es el equivalente del 30% de desperdicio de la rúbrica,
// calculado sobre el total de slots del archivo principal.
func sequentialOptions() sequential.Options {
	return sequential.Options{RebuildMin: 128}
}

// joinErrors combina varios errores en uno.
func joinErrors(errs []error) error {
	switch len(errs) {
	case 0:
		return nil
	case 1:
		return errs[0]
	default:
		return errors.Join(errs...)
	}
}

// createIndex construye un índice del tipo pedido sobre una columna y lo registra
// en la tabla. El tipo se decide por el nombre, siguiendo la convención del
// proyecto: pk_<tabla> es el B+ agrupado, hash_<col> el hash dinámico y cualquier
// otro nombre un B+ no agrupado.
func (e *Engine) createIndex(tbl *Table, name, col string, unique bool) (Index, error) {
	var (
		idx Index
		err error
	)
	onPK, hasPKCol := tbl.PrimaryKeyIndex()
	pkCol, hasPK := tbl.ColumnIndex(col)
	isPK := hasPK && hasPKCol && pkCol == onPK
	switch {
	case strings.HasPrefix(strings.ToLower(name), rtreePrefix):
		idx, err = newRTreeIndex(name, col, tbl)
	case tbl.Clustered && isPK:
		idx, err = newClusteredIndex(name, tbl, e.opt.BPlusOrder)
	case tbl.Clustered:
		// Índice secundario de una tabla agrupada: la referencia a la fila es
		// la clave primaria, no un RID.
		idx, err = newSecondaryClusteredIndex(name, col, tbl, unique, e.opt.BPlusOrder)
	case strings.HasPrefix(strings.ToLower(name), "hash"):
		idx, err = newHashIndex(name, col, tbl, unique, e.opt.HashBucketSize)
	default:
		idx, err = newUnclusteredIndex(name, col, tbl, unique, e.opt.BPlusOrder)
	}
	if err != nil {
		return nil, fmt.Errorf("sql: no se pudo crear el índice %s: %w", name, err)
	}
	tbl.addIndex(idx)
	return idx, nil
}

// openStorage abre (o crea) el almacenamiento físico de una tabla y reconstruye
// sus índices. La comparten CREATE TABLE y la reapertura del catálogo, de modo
// que una tabla recuperada queda exactamente igual que una recién creada.
func (e *Engine) openStorage(tbl *Table, schema storage.Table, clustered bool, indexes []indexDisk) error {
	path := e.tablePath(schema.Name)

	if clustered {
		if err := schema.Validate(); err != nil {
			return fmt.Errorf("sql: %s necesita una clave primaria para ser agrupada: %w", schema.Name, err)
		}
		adapter, err := newSeqAdapter(path, schema, sequentialOptions())
		if err != nil {
			return err
		}
		if err := adapter.Open(); err != nil {
			return fmt.Errorf("sql: no se pudo abrir el secuencial de %s: %w", schema.Name, err)
		}
		tbl.seq = adapter
		tbl.Clustered = true
		// El B+ agrupado se puebla con un barrido del secuencial.
		pk, err := newClusteredIndex("pk_"+strings.ToLower(schema.Name), tbl, e.opt.BPlusOrder)
		if err != nil {
			return fmt.Errorf("sql: no se pudo crear el índice agrupado: %w", err)
		}
		tbl.addIndex(pk)
	} else {
		adapter, err := newHeapAdapter(path, schema, heapOptions(e.opt))
		if err != nil {
			return err
		}
		if err := adapter.Open(); err != nil {
			return fmt.Errorf("sql: no se pudo abrir el heap de %s: %w", schema.Name, err)
		}
		tbl.heap = adapter
	}

	for _, idx := range indexes {
		if _, err := e.createIndex(tbl, idx.Name, idx.Column, idx.Unique); err != nil {
			return err
		}
	}
	return nil
}

// columnIsIndexed indica si el tipo declarado de una columna pide un índice
// (INDEX, KEY o UNIQUE). El parser aún no captura modificadores de columna, así
// que se reconoce por el prefijo del nombre de tipo.
func columnIsIndexed(colType string) bool {
	up := strings.ToUpper(strings.TrimSpace(colType))
	for _, p := range []string{"INDEX", "KEY", "UNIQUE"} {
		if strings.HasPrefix(up, p) {
			return true
		}
	}
	return false
}

// IsPrimaryKeyIndexColumn indica si la columna forma parte de la clave primaria.
func (t *Table) IsPrimaryKeyIndexColumn(name string) bool {
	pos, ok := t.ColumnIndex(name)
	if !ok {
		return false
	}
	return t.IsPrimaryKeyColumn(pos)
}

// IndexOnColumnNamed busca un índice por nombre de columna.
func (t *Table) IndexOnColumnNamed(col string) bool {
	_, ok := t.IndexOnColumn(mustCol(t, col))
	return ok
}

func mustCol(t *Table, name string) int {
	if pos, ok := t.ColumnIndex(name); ok {
		return pos
	}
	return -1
}

// tables devuelve el catálogo completo; lo usa el panel de archivos.
func (e *Engine) tables() []*Table { return e.cat.all() }

// TableNames lista las tablas del catálogo.
func (e *Engine) TableNames() []string {
	e.stmtMu.Lock()
	defer e.stmtMu.Unlock()
	return e.cat.names()
}

// storageKind indica si una tabla es agrupada o no.
func storageKind(t *Table) string {
	if t.Clustered {
		return "clustered"
	}
	return "unclustered"
}

// TableFileInfo describe el almacenamiento de una tabla para el panel de archivos.
type TableFileInfo struct {
	Name      string
	Columns   []string
	Types     []string
	Clustered bool
	// PrimaryKey son las columnas de la clave primaria, en orden.
	PrimaryKey []string
	Indexes    []string
	// IndexKinds es la técnica de cada índice de Indexes (misma posición).
	IndexKinds  []string
	Rows        int64
	Pages       int32
	PageSize    int32
	SlotSize    int32
	TotalSize   int64
	Allocation  string
	DeadSlots   int64
	AuxSlots    int64
	WastedRatio float64
}

// FileInfo describe el almacenamiento de una tabla.
func (t *Table) FileInfo() TableFileInfo {
	info := TableFileInfo{
		Name:      t.Name(),
		Columns:   append([]string(nil), t.Schema.Columns...),
		Clustered: t.Clustered,
		Indexes:   t.IndexNames(),
	}
	for _, k := range t.Schema.KeyCols {
		info.PrimaryKey = append(info.PrimaryKey, t.Schema.Columns[k])
	}
	for _, name := range info.Indexes {
		kind := ""
		if idx, ok := t.Index(name); ok {
			kind = idx.Kind().String()
		}
		info.IndexKinds = append(info.IndexKinds, kind)
	}
	for i, ty := range t.Schema.Types {
		if _, ok := t.rtreeOn(i); ok {
			info.Types = append(info.Types, "point")
			continue
		}
		info.Types = append(info.Types, ty.String())
	}
	switch {
	case t.Clustered && t.seq != nil:
		st := t.seq.Stats()
		info.Rows = t.seq.file.Rows()
		info.DeadSlots = st.MainDead
		info.AuxSlots = st.AuxLive
		info.WastedRatio = st.WastedRatio
		info.PageSize = 0
		info.Allocation = "secuencial paginado + overflow"
	default:
		if t.heap != nil {
			st := t.heap.Stats()
			info.Rows = st.Rows
			info.Pages = st.Pages
			info.PageSize = st.PageSize
			info.SlotSize = st.SlotSize
			info.TotalSize = st.TotalSize
			info.Allocation = st.Allocation
		}
	}
	return info
}

// Files describe todas las tablas; lo usa el panel de archivos del frontend.
func (e *Engine) Files() []TableFileInfo {
	e.stmtMu.Lock()
	defer e.stmtMu.Unlock()
	tables := e.cat.all()
	out := make([]TableFileInfo, 0, len(tables))
	for _, t := range tables {
		out = append(out, t.FileInfo())
	}
	return out
}

var _ = storage.TypeInt32

// formatRID imprime un RID como (archivo, página, slot).
func formatRID(rid storage.RID) string {
	return fmt.Sprintf("(%d,%d,%d)", rid.File, rid.Page, rid.Slot)
}

// ridLabel describe dónde quedó una fila, para el detalle del plan.
func ridLabel(t *Table, rid storage.RID, viaIndex string) string {
	if t.Clustered {
		return fmt.Sprintf("el secuencial (índice agrupado, %s)", storageKind(t))
	}
	if viaIndex != "" {
		return fmt.Sprintf("el heap %s, referenciado por %s", formatRID(rid), viaIndex)
	}
	return fmt.Sprintf("el heap %s", formatRID(rid))
}
