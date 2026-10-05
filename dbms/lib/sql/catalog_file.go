package sql

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dbms-go/v2/dbms/lib/storage"
	"github.com/dbms-go/v2/dbms/lib/storage/sequential"
)

// catalogFile es el nombre del archivo donde vive el catálogo.
const catalogFile = "catalog.json"

// catalogFormatVersion permite detectar catálogos de otra versión del motor.
const catalogFormatVersion = 1

// catalogFile es la estructura que se persiste en disco. Contiene el esquema
// de cada tabla (lo necesario para reabrir sus archivos, que validan su hash) y
// los índices secundarios que hay que reconstruir al arrancar.
//
// Las filas y los índices B+ no se guardan aquí: viven en los archivos de cada
// tabla (heap o secuencial) y los índices se reconstruyen desde ellos.
type catalogDisk struct {
	Version int         `json:"version"`
	Tables  []tableDisk `json:"tables"`
}

type tableDisk struct {
	Name      string      `json:"name"`
	Columns   []string    `json:"columns"`
	Types     []string    `json:"types"`
	MaxLen    []int       `json:"maxlen"`
	KeyCols   []int       `json:"keycols"`
	Clustered bool        `json:"clustered"`
	Indexes   []indexDisk `json:"indexes"`
}

type indexDisk struct {
	Name   string `json:"name"`
	Column string `json:"column"`
	Unique bool   `json:"unique"`
}

// catalogPath devuelve la ruta del archivo de catálogo del motor.
func (e *Engine) catalogPath() string {
	return filepath.Join(e.dbDir(), catalogFile)
}

// saveCatalog escribe el catálogo completo. Se llama después de cada cambio de
// esquema (CREATE TABLE, CREATE INDEX, DROP TABLE) para que un reinicio no
// pierda la estructura aunque el proceso se corte.
func (e *Engine) saveCatalog() error {
	e.catalogMu.Lock()
	defer e.catalogMu.Unlock()

	disk := catalogDisk{Version: catalogFormatVersion}
	for _, t := range e.cat.all() {
		spec := tableDisk{
			Indexes:   []indexDisk{},
			Name:      t.Name(),
			Columns:   append([]string(nil), t.Schema.Columns...),
			MaxLen:    append([]int(nil), t.Schema.MaxLen...),
			KeyCols:   append([]int(nil), t.Schema.KeyCols...),
			Clustered: t.Clustered,
		}
		for _, ty := range t.Schema.Types {
			spec.Types = append(spec.Types, typeName(ty))
		}
		for _, name := range t.IndexNames() {
			idx := t.indexes[name]
			// El índice agrupado se reconstruye solo al reabrir el secuencial.
			if idx.Kind() == ClusteredIndexKind {
				continue
			}
			spec.Indexes = append(spec.Indexes, indexDisk{
				Name:   idx.Name(),
				Column: idx.Column(),
				Unique: idx.Unique(),
			})
		}
		disk.Tables = append(disk.Tables, spec)
	}

	buf, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return fmt.Errorf("sql: no se pudo serializar el catálogo: %w", err)
	}
	// Se escribe en un temporal y se renombra: un corte a mitad de escritura no
	// deja un catálogo corrupto.
	tmp := e.catalogPath() + ".tmp"
	buf = append(buf, '\n')
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		return fmt.Errorf("sql: no se pudo escribir el catálogo: %w", err)
	}
	if err := os.Rename(tmp, e.catalogPath()); err != nil {
		return fmt.Errorf("sql: no se pudo actualizar el catálogo: %w", err)
	}
	return nil
}

// loadCatalog lee el catálogo del disco y reabre cada tabla con sus índices.
// Si el directorio no tiene catálogo, el motor arranca vacío.
func (e *Engine) loadCatalog() error {
	e.catalogMu.Lock()
	defer e.catalogMu.Unlock()

	buf, err := os.ReadFile(e.catalogPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("sql: no se pudo leer el catálogo: %w", err)
	}
	var disk catalogDisk
	if err := json.Unmarshal(buf, &disk); err != nil {
		return fmt.Errorf("sql: catálogo ilegible: %w", err)
	}
	if disk.Version != catalogFormatVersion {
		return fmt.Errorf("sql: el catálogo es de la versión %d y este motor usa la %d",
			disk.Version, catalogFormatVersion)
	}

	for _, spec := range disk.Tables {
		tbl, err := e.openTable(spec)
		if err != nil {
			return fmt.Errorf("sql: no se pudo reabrir la tabla %s: %w", spec.Name, err)
		}
		if err := e.cat.put(tbl); err != nil {
			_ = tbl.Close()
			return err
		}
	}
	return nil
}

// schemaFromDisk reconstruye el esquema físico a partir de la entrada del
// catálogo. Debe coincidir exactamente con el que creó los archivos, porque
// storage valida el hash del esquema al abrirlos.
func schemaFromDisk(spec tableDisk) (storage.Table, error) {
	schema := storage.Table{
		Name:    spec.Name,
		Columns: spec.Columns,
		KeyCols: spec.KeyCols,
	}
	for _, name := range spec.Types {
		ty, ok := parseTypeName(name)
		if !ok {
			return storage.Table{}, fmt.Errorf("tipo desconocido %q", name)
		}
		schema.Types = append(schema.Types, ty)
	}
	if len(spec.MaxLen) > 0 {
		schema.MaxLen = spec.MaxLen
	} else {
		// Los tipos de tamaño variable siempre guardan su máximo.
		for range schema.Types {
			schema.MaxLen = append(schema.MaxLen, 0)
		}
	}
	if len(schema.Columns) == 0 {
		return storage.Table{}, fmt.Errorf("la tabla no tiene columnas")
	}
	return schema, nil
}

// openTable crea la tabla en el catálogo a partir de su descripción persistida:
// abre el almacenamiento (heap o secuencial) y reconstruye los índices.
func (e *Engine) openTable(spec tableDisk) (*Table, error) {
	schema, err := schemaFromDisk(spec)
	if err != nil {
		return nil, err
	}
	tbl := &Table{Schema: schema, Clustered: spec.Clustered, indexes: make(map[string]Index)}
	if err := e.openStorage(tbl, schema, spec.Clustered, spec.Indexes); err != nil {
		_ = tbl.Close()
		return nil, err
	}
	return tbl, nil
}

// dropFiles borra los archivos físicos de una tabla. Se usa en DROP TABLE para
// no dejar datos huérfanos en el directorio del motor.
func dropFiles(dir, table string) error {
	base := filepath.Join(dir, strings.ToLower(table))
	candidates := []string{
		base + storage.SUFFIX_DATA,
		base + storage.SUFFIX_INDEX,
		base + sequential.SUFFIX_SEQ,
		base + sequential.SUFFIX_AUX,
		base + sequential.SUFFIX_SEQ + ".tmp",
	}
	var errs []error
	for _, path := range candidates {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return joinErrors(errs)
}

// typeName convierte un tipo de storage al nombre que va al catálogo.
func typeName(t storage.Type) string {
	switch t {
	case storage.TypeInt32:
		return "int"
	case storage.TypeInt64:
		return "bigint"
	case storage.TypeBool:
		return "bool"
	case storage.TypeString:
		return "text"
	case storage.TypeBytes:
		return "bytes"
	}
	return t.String()
}

// parseTypeName es el converso de typeName.
func parseTypeName(name string) (storage.Type, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "int", "int32", "integer":
		return storage.TypeInt32, true
	case "bigint", "int64", "long":
		return storage.TypeInt64, true
	case "bool", "boolean":
		return storage.TypeBool, true
	case "text", "string", "varchar", "char":
		return storage.TypeString, true
	case "bytes", "blob", "varbinary":
		return storage.TypeBytes, true
	}
	return 0, false
}
