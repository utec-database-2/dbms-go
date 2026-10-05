// Package sql es el motor del DBMS: une el DSL (lexer, parser y AST), la capa
// de almacenamiento (heap file y archivo secuencial paginado), los índices
// (B+ agrupado, B+ no agrupado y hash dinámico) y los algoritmos externos
// (external sorting con k-way merge) para ejecutar consultas SQL completas.
//
// Cada consulta devuelve, además del resultado, el Plan de ejecución que
// documenta qué acceso se eligió (índice o recorrido secuencial), en qué orden
// se aplicaron los operadores y por qué. Ese plan alimenta el panel de
// ejecución del frontend.
package sql

import (
	"fmt"
	"strings"

	"github.com/dbms-go/v2/dbms/lib/dsl/ast"
	"github.com/dbms-go/v2/dbms/lib/dsl/lexer"
	"github.com/dbms-go/v2/dbms/lib/dsl/parser"
	"github.com/dbms-go/v2/dbms/lib/storage"
)

// StepKind clasifica un paso del plan de ejecución.
type StepKind string

const (
	StepScan         StepKind = "seq-scan"
	StepIndexScan    StepKind = "index-scan"
	StepIndexSeek    StepKind = "index-seek"
	StepFilter       StepKind = "filter"
	StepProject      StepKind = "project"
	StepExternalSort StepKind = "external-sort"
	StepGroupBy      StepKind = "group-by"
	StepJoin         StepKind = "join"
	StepLimit        StepKind = "limit"
	StepInsert       StepKind = "insert"
	StepUpdate       StepKind = "update"
	StepDelete       StepKind = "delete"
)

// Step es un nodo del plan de ejecución.
type Step struct {
	Kind StepKind
	// Detail explica la decisión, por ejemplo "índice idx_users_email
	// (unclustered-bplus) para email = 'a@b.c'".
	Detail string
	// Rows es el número de filas afectadas o devueltas por el paso.
	Rows int
	// Cost es una estimación de E/S (lecturas de página) del paso.
	Cost int
}

// Result es la respuesta del motor a una consulta.
type Result struct {
	// Columns son los nombres de las columnas del resultado (vacío en DML).
	Columns []string
	// Rows contiene las filas resultantes.
	Rows [][]any
	// Affected es el número de filas modificadas por una sentencia DML.
	Affected int
	// Plan son los pasos del plan de ejecución, en orden de ejecución.
	Plan []Step
	// Message describe el efecto de sentencias sin conjunto de resultados.
	Message string
}

// Options configura el motor al abrirse.
type Options struct {
	// Dir es el directorio donde viven los archivos de las tablas.
	Dir string
	// PageSize del heap file (por defecto storage.DefaultPageSize).
	PageSize int
	// SlotSize del heap file (por defecto storage.DefaultSlotSize).
	SlotSize int
	// HeapStrategy elige la política de reutilización de espacio libre.
	HeapStrategy int
	// BPlusOrder es el orden (fanout) de los árboles B+.
	BPlusOrder int
	// HashBucketSize es la capacidad de una cubeta del hash extensible.
	HashBucketSize int
	// SortBufferSlots son los slots que caben en memoria durante un
	// external sort; al superarlos se generan runs y se hace k-way merge.
	SortBufferSlots int
	// Newline es el separador de líneas que el lexer usa para contar posiciones.
	Newline string
}

// Engine ejecuta consultas SQL sobre el catálogo de tablas.
type Engine struct {
	cat *catalog
	opt Options
}

// New crea el motor. No toca el disco: las tablas se crean con CREATE TABLE.
func New(dir string, opt Options) *Engine {
	if opt.PageSize == 0 {
		opt.PageSize = storage.DefaultPageSize
	}
	if opt.SlotSize == 0 {
		opt.SlotSize = storage.DefaultSlotSize
	}
	if opt.BPlusOrder < 3 {
		opt.BPlusOrder = 32
	}
	if opt.HashBucketSize < 2 {
		opt.HashBucketSize = 8
	}
	if opt.SortBufferSlots < 1 {
		opt.SortBufferSlots = 4096
	}
	if opt.Newline == "" {
		opt.Newline = "\n"
	}
	return &Engine{cat: newCatalog(dir), opt: opt}
}

// Close cierra todos los archivos abiertos de las tablas.
func (e *Engine) Close() {
	for _, t := range e.cat.all() {
		for _, idx := range t.indexes {
			_ = idx.Close()
		}
		if t.heap != nil {
			_ = t.heap.Close()
		}
		if t.seq != nil {
			_ = t.seq.Close()
		}
	}
}

// Exec parsea y ejecuta una sentencia SQL.
func (e *Engine) Exec(sql string) (*Result, error) {
	lex := lexer.Tokenize(sql, e.opt.Newline)
	if err := lex.Err(); err != nil {
		return nil, fmt.Errorf("sql: error léxico: %w", err)
	}
	p := parser.Parse(lex)
	if err := p.Err(); err != nil {
		return nil, fmt.Errorf("sql: error de sintaxis: %w", err)
	}
	switch node := p.Parent().(type) {
	case *ast.Select:
		return e.execSelect(node)
	case *ast.Insert:
		return e.execInsert(node)
	case *ast.Update:
		return e.execUpdate(node)
	case *ast.Delete:
		return e.execDelete(node)
	case *ast.CreateTable:
		return e.execCreateTable(node)
	case *ast.CreateIndex:
		return e.execCreateIndex(node)
	case *ast.DropTable:
		return e.execDropTable(node)
	case *ast.TruncateTable:
		return e.execTruncate(node)
	case *ast.BeginTransaction:
		return &Result{Message: "transacción iniciada"}, nil
	case *ast.Commit:
		return &Result{Message: "transacción confirmada"}, nil
	case *ast.Rollback:
		return &Result{Message: "transacción revertida"}, nil
	default:
		return nil, fmt.Errorf("sql: sentencia no soportada %T", p.Parent())
	}
}

// ---------------------------------------------------------------------------
// DDL
// ---------------------------------------------------------------------------

// execCreateTable crea la tabla, su almacenamiento y sus índices iniciales.
// La sintaxis acepta modificadores de tabla que el parser aún no expone, así que
// el tipo de almacenamiento se decide aquí:
//
//	CREATE TABLE t (a INT PRIMARY KEY, b VARCHAR(64), INDEX ...)
//	CREATE TABLE t CLUSTERED BY (a) (...)
//
// y, si no se indica nada, la tabla usa B+ no agrupado sobre heap file, que es
// el caso por defecto del proyecto.
func (e *Engine) execCreateTable(n *ast.CreateTable) (*Result, error) {
	if _, exists := e.cat.get(n.Name); exists {
		return nil, fmt.Errorf("sql: la tabla %s ya existe", n.Name)
	}
	schema, err := buildSchema(n)
	if err != nil {
		return nil, err
	}
	schema.Name = n.Name

	tbl := &Table{Schema: schema, indexes: make(map[string]Index)}
	path := e.tablePath(n.Name)

	// Clustered requiere clave: si no hay PRIMARY KEY se usa la primera columna.
	clustered := n.Clustered
	if clustered {
		seq, err := newSeqAdapter(path, schema, sequentialOptions())
		if err != nil {
			return nil, err
		}
		if err := seq.Open(); err != nil {
			return nil, fmt.Errorf("sql: no se pudo crear el secuencial de %s: %w", n.Name, err)
		}
		tbl.seq = seq
		tbl.Clustered = true
	} else {
		hp, err := newHeapAdapter(path, schema, heapOptions(e.opt))
		if err != nil {
			return nil, err
		}
		if err := hp.Open(); err != nil {
			return nil, fmt.Errorf("sql: no se pudo crear el heap de %s: %w", n.Name, err)
		}
		tbl.heap = hp
	}

	if err := e.cat.put(tbl); err != nil {
		_ = tbl.Close()
		return nil, err
	}

	plan := []Step{{
		Kind:   StepScan,
		Detail: fmt.Sprintf("create table %s sobre %s", n.Name, storageLabel(tbl)),
		Rows:   len(schema.Columns),
	}}

	// Índice agrupado: se crea siempre que la tabla sea agrupada.
	if tbl.Clustered {
		idx, err := newClusteredIndex("pk_"+strings.ToLower(n.Name), tbl, e.opt.BPlusOrder)
		if err != nil {
			return nil, fmt.Errorf("sql: no se pudo crear el índice agrupado: %w", err)
		}
		tbl.addIndex(idx)
		plan = append(plan, Step{
			Kind:   StepIndexScan,
			Detail: "índice agrupado pk_" + strings.ToLower(n.Name) + " (clustered-bplus) sobre el archivo secuencial",
		})
	}

	// Índices declarados en la definición de la tabla: PRIMARY KEY sobre
	// tablas no agrupadas (para hacer cumplible la unicidad) e INDEX/UNIQUE por
	// columna. Las columnas ya cubiertas por el índice agrupado se omiten.
	for _, col := range n.Columns {
		name := col.Name.Name
		pos, ok := tbl.ColumnIndex(name)
		if !ok {
			continue
		}
		wanted := col.PrimaryKey || col.Unique || col.Indexed
		if !wanted || tbl.IndexOnColumnNamed(name) {
			continue
		}
		// La clave primaria de una tabla agrupada ya la cubre el B+ agrupado.
		if tbl.Clustered && tbl.IsPrimaryKeyIndexColumn(name) {
			continue
		}
		idxName := "idx_" + strings.ToLower(name)
		kindPrefix := ""
		if col.PrimaryKey {
			idxName = "pk_" + strings.ToLower(tbl.Name())
		}
		if col.Unique && !col.PrimaryKey {
			kindPrefix = "unique "
		}
		idx, err := e.createIndex(tbl, idxName, name, col.Unique || col.PrimaryKey)
		if err != nil {
			return nil, err
		}
		_ = pos
		plan = append(plan, Step{
			Kind:   StepIndexScan,
			Detail: fmt.Sprintf("índice %s sobre %s (%s%s)", idx.Name(), name, kindPrefix, idx.Kind()),
		})
	}

	return &Result{
		Message: fmt.Sprintf("tabla %s creada (%s)", n.Name, storageLabel(tbl)),
		Columns: schema.Columns,
		Plan:    plan,
	}, nil
}

// execCreateIndex crea un índice sobre una tabla existente. El tipo depende del
// nombre: pk_<tabla> agrupa (solo válido en tablas agrupadas), hash_<col> usa
// extendible hashing y cualquier otro nombre crea un B+ no agrupado.
func (e *Engine) execCreateIndex(n *ast.CreateIndex) (*Result, error) {
	tbl, err := e.requireTable(n.Table)
	if err != nil {
		return nil, err
	}
	if len(n.Columns) != 1 {
		return nil, fmt.Errorf("sql: CREATE INDEX requiere exactamente una columna (recibidas %d)", len(n.Columns))
	}
	col := n.Columns[0].Name
	if _, ok := tbl.ColumnIndex(col); !ok {
		return nil, fmt.Errorf("sql: la columna %s no existe en %s", col, tbl.Name())
	}
	if _, exists := tbl.Index(n.Name); exists {
		if n.IfNotExists {
			return &Result{Message: fmt.Sprintf("el índice %s ya existe", n.Name)}, nil
		}
		return nil, fmt.Errorf("sql: el índice %s ya existe en %s", n.Name, tbl.Name())
	}

	idx, err := e.createIndex(tbl, n.Name, col, n.Unique)
	if err != nil {
		return nil, err
	}
	return &Result{
		Message: fmt.Sprintf("índice %s creado sobre %s (%s)", idx.Name(), tbl.Name(), idx.Kind()),
		Plan: []Step{{
			Kind:   StepIndexScan,
			Detail: fmt.Sprintf("índice %s sobre %s (%s) con %d entrada(s)", idx.Name(), col, idx.Kind(), idx.Entries()),
			Rows:   idx.Entries(),
		}},
	}, nil
}

func (e *Engine) execDropTable(n *ast.DropTable) (*Result, error) {
	tbl, ok := e.cat.drop(n.Name)
	if !ok {
		if n.IfExists {
			return &Result{Message: fmt.Sprintf("la tabla %s no existe", n.Name)}, nil
		}
		return nil, fmt.Errorf("sql: la tabla %s no existe", n.Name)
	}
	if err := tbl.Close(); err != nil {
		return nil, err
	}
	return &Result{
		Message: fmt.Sprintf("tabla %s eliminada", n.Name),
		Plan:    []Step{{Kind: StepDelete, Detail: "drop table " + n.Name}},
	}, nil
}

func (e *Engine) execTruncate(n *ast.TruncateTable) (*Result, error) {
	if _, ok := e.cat.get(n.Name); !ok {
		return nil, fmt.Errorf("sql: la tabla %s no existe", n.Name)
	}
	// TRUNCATE se implementa como un DELETE sin WHERE: se recorre el
	// almacenamiento y se borra fila por fila.
	del := &ast.Delete{From: ast.TableExpr{NameExpr: ast.NameExpr{Name: n.Name}}}
	res, err := e.execDelete(del)
	if err != nil {
		return nil, err
	}
	res.Message = fmt.Sprintf("tabla %s vaciada (%d filas)", n.Name, res.Affected)
	return res, nil
}

// buildSchema traduce las columnas del AST al esquema físico de storage.
func buildSchema(n *ast.CreateTable) (storage.Table, error) {
	schema := storage.Table{Name: n.Name}
	var keyCols []int
	seen := make(map[string]bool)

	for _, col := range n.Columns {
		name := col.Name.Name
		if name == "" {
			return storage.Table{}, fmt.Errorf("sql: columna sin nombre en %s", n.Name)
		}
		if seen[strings.ToLower(name)] {
			return storage.Table{}, fmt.Errorf("sql: columna duplicada %s", name)
		}
		seen[strings.ToLower(name)] = true

		typ, maxLen, err := sqlType(col)
		if err != nil {
			return storage.Table{}, err
		}
		pos := len(schema.Columns)
		schema.Columns = append(schema.Columns, name)
		schema.Types = append(schema.Types, typ)
		schema.MaxLen = append(schema.MaxLen, maxLen)

		if col.PrimaryKey {
			keyCols = append(keyCols, pos)
		}
	}
	if len(schema.Columns) == 0 {
		return storage.Table{}, fmt.Errorf("sql: la tabla %s no tiene columnas", n.Name)
	}

	// CLUSTERED BY (a, b) puede declarar la clave si no hay PRIMARY KEY.
	if len(keyCols) == 0 {
		for _, ck := range n.ClusterKeys {
			if pos, ok := columnPos(schema, ck.Name); ok {
				keyCols = append(keyCols, pos)
			}
		}
	}
	schema.KeyCols = keyCols
	if err := schema.Validate(); err != nil {
		return storage.Table{}, err
	}
	return schema, nil
}

// columnPos busca una columna del esquema por nombre.
func columnPos(schema storage.Table, name string) (int, bool) {
	for i, c := range schema.Columns {
		if strings.EqualFold(c, name) {
			return i, true
		}
	}
	return 0, false
}

// sqlType traduce la columna declarada al tipo físico de storage, usando el
// tamaño explícito de VARCHAR(n) cuando el parser lo capturó.
func sqlType(col ast.ColumnExpr) (storage.Type, int, error) {
	name := col.Type.Name
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "INT", "INTEGER", "INT32":
		return storage.TypeInt32, 0, nil
	case "BIGINT", "LONG", "INT64":
		return storage.TypeInt64, 0, nil
	case "BOOL", "BOOLEAN":
		return storage.TypeBool, 0, nil
	case "TEXT", "STRING":
		return storage.TypeString, 255, nil
	}
	// VARCHAR(n) / VARBINARY(n): el tamaño viene en col.Length.
	up := strings.ToUpper(strings.TrimSpace(name))
	if strings.HasPrefix(up, "VARCHAR") || strings.HasPrefix(up, "VARBINARY") || strings.HasPrefix(up, "CHAR") {
		n := col.Length
		if n <= 0 {
			n = 255
		}
		if strings.HasPrefix(up, "VARBINARY") {
			return storage.TypeBytes, n, nil
		}
		return storage.TypeString, n, nil
	}
	if strings.HasPrefix(up, "BLOB") || strings.HasPrefix(up, "BYTES") {
		return storage.TypeBytes, 4096, nil
	}
	return storage.TypeInt32, 0, fmt.Errorf("sql: tipo no soportado %q", name)
}

func storageLabel(t *Table) string {
	if t.Clustered {
		return "archivo secuencial paginado (B+ agrupado)"
	}
	return "heap file (B+ no agrupado)"
}
