// Package sql conecta el DSL (parser/AST) con el almacenamiento y los índices:
// catálogo en memoria + archivos de datos en disco. El esquema (CREATE TABLE)
// vive solo en memoria; los datos persisten en el HeapFile de cada tabla.
package sql

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/dbms-go/v2/dbms/lib/dsl/ast"
	"github.com/dbms-go/v2/dbms/lib/dsl/lexer"
	"github.com/dbms-go/v2/dbms/lib/dsl/parser"
	"github.com/dbms-go/v2/dbms/lib/external/iterator"
	"github.com/dbms-go/v2/dbms/lib/external/sorting"
	"github.com/dbms-go/v2/dbms/lib/shared"
	"github.com/dbms-go/v2/dbms/lib/storage/heap"
	"github.com/dbms-go/v2/indexes/bplus"
)

// Result es el resultado de ejecutar una sentencia.
type Result struct {
	Columns  []string
	Rows     [][]any
	Affected int64
	Message  string
}

type table struct {
	schema *ast.CreateTable
	heap   *heap.HeapFile
	idx    *bplus.UnclusteredIndex[int] // clave = primera columna (INT)
}

// Database es un catálogo en memoria sobre un directorio de datos en disco.
type Database struct {
	dir    string
	tables map[string]*table
}

// Open prepara (o reutiliza) el directorio de datos.
func Open(dir string) (*Database, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Database{dir: dir, tables: make(map[string]*table)}, nil
}

func (db *Database) Close() error {
	var first error
	for _, t := range db.tables {
		if err := t.heap.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Execute parsea y ejecuta una sentencia SQL completa (terminada en ';').
func (db *Database) Execute(query string) (*Result, error) {
	ctx := parser.Parse(lexer.Tokenize(query, "\n"))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch n := ctx.Parent().(type) {
	case *ast.CreateTable:
		return db.execCreate(n)
	case *ast.Insert:
		return db.execInsert(n)
	case *ast.Select:
		return db.execSelect(n)
	case *ast.Delete:
		return db.execDelete(n)
	default:
		return nil, fmt.Errorf("sql: sentencia no soportada: %T", ctx.Parent())
	}
}

// Tables devuelve los nombres de las tablas conocidas.
func (db *Database) Tables() []string {
	names := make([]string, 0, len(db.tables))
	for name := range db.tables {
		names = append(names, name)
	}
	return names
}

func columnPos(schema *ast.CreateTable, name string) int {
	for i, c := range schema.Columns {
		if c.Name.Name == name {
			return i
		}
	}
	return -1
}

func (db *Database) execCreate(ct *ast.CreateTable) (*Result, error) {
	if _, ok := db.tables[ct.Name]; ok {
		return nil, fmt.Errorf("sql: la tabla %q ya existe", ct.Name)
	}
	if len(ct.Columns) == 0 {
		return nil, fmt.Errorf("sql: CREATE TABLE %q requiere al menos una columna", ct.Name)
	}
	if strings.ToUpper(ct.Columns[0].Type.Name) != "INT" {
		return nil, fmt.Errorf("sql: la primera columna (clave) debe ser INT, no %q", ct.Columns[0].Type.Name)
	}
	for _, c := range ct.Columns {
		if c.Type.Name != "INT" && c.Type.Name != "STRING" && c.Type.Name != "DECIMAL" && c.Type.Name != "BOOL" {
			return nil, fmt.Errorf("sql: tipo de columna no soportado: %q", c.Type.Name)
		}
	}

	path := filepath.Join(db.dir, ct.Name+".heap")
	h, err := openHeap(path)
	if err != nil {
		return nil, err
	}
	idx, err := bplus.NewUnclusteredIndex[int](4, h, keyOfRow)
	if err != nil {
		h.Close()
		return nil, err
	}
	db.tables[ct.Name] = &table{schema: ct, heap: h, idx: idx}
	return &Result{Message: "tabla " + ct.Name + " creada"}, nil
}

func (db *Database) execInsert(ins *ast.Insert) (*Result, error) {
	t, ok := db.tables[ins.Table.Name]
	if !ok {
		return nil, fmt.Errorf("sql: tabla desconocida %q", ins.Table.Name)
	}
	schema := t.schema

	var order []int
	if len(ins.Columns) == 0 {
		for i := range schema.Columns {
			order = append(order, i)
		}
	} else {
		for _, c := range ins.Columns {
			pos := columnPos(schema, c.Name)
			if pos < 0 {
				return nil, fmt.Errorf("sql: columna desconocida %q", c.Name)
			}
			order = append(order, pos)
		}
	}

	affected := int64(0)
	for _, row := range ins.Rows {
		if len(row) != len(order) {
			return nil, fmt.Errorf("sql: %d valores no coinciden con %d columnas", len(row), len(order))
		}
		vals := make([]any, len(schema.Columns))
		for i, node := range row {
			v, err := literalValue(node)
			if err != nil {
				return nil, fmt.Errorf("sql: valor %d: %v", i+1, err)
			}
			vals[order[i]] = v
		}
		for i := range vals {
			if vals[i] == nil {
				vals[i] = zeroValue(schema.Columns[i].Type.Name)
			}
		}
		key, ok := vals[0].(int)
		if !ok {
			return nil, fmt.Errorf("sql: la clave (primera columna) debe ser entera")
		}
		payload, err := encodeRow(vals)
		if err != nil {
			return nil, err
		}
		if _, err := t.idx.InsertWithKey(key, payload); err != nil {
			return nil, err
		}
		affected++
	}
	return &Result{Affected: affected}, nil
}

func (db *Database) execSelect(sel *ast.Select) (*Result, error) {
	t, ok := db.tables[sel.From.Name]
	if !ok {
		return nil, fmt.Errorf("sql: tabla desconocida %q", sel.From.Name)
	}
	schema := t.schema

	// Proyección: "*" o lista de columnas.
	var outCols []string
	var projPos []int
	if sel.All {
		for i, c := range schema.Columns {
			outCols = append(outCols, c.Name.Name)
			projPos = append(projPos, i)
		}
	} else {
		for _, c := range sel.Selected {
			pos := columnPos(schema, c.Name)
			if pos < 0 {
				return nil, fmt.Errorf("sql: columna desconocida %q", c.Name)
			}
			outCols = append(outCols, c.Name)
			projPos = append(projPos, pos)
		}
	}

	// WHERE sobre la clave -> punto/rango en el B+.
	var recs []bplus.UnclusteredRecord[int]
	low, high, point, hasWhere, err := whereRange(sel)
	if err != nil {
		return nil, err
	}
	switch {
	case !hasWhere:
		recs, err = t.idx.OrderedScan()
	case point:
		recs, err = t.idx.Search(low)
	default:
		recs, err = t.idx.RangeSearch(low, high)
	}
	if err != nil {
		return nil, err
	}

	rows := make([]shared.Record, 0, len(recs))
	for _, r := range recs {
		vals, err := decodeRow(r.Payload)
		if err != nil {
			return nil, fmt.Errorf("sql: decodificación de fila: %v", err)
		}
		rows = append(rows, shared.Record{Values: vals, RID: r.RID})
	}

	// ORDER BY <col> [ASC|DESC] mediante external sorting.
	if sel.OrderBy != nil {
		name, ok := sel.OrderBy.Expr.(*ast.IdExpr)
		if !ok {
			return nil, fmt.Errorf("sql: ORDER BY solo soporta columnas")
		}
		pos := columnPos(schema, name.Name)
		if pos < 0 {
			return nil, fmt.Errorf("sql: ORDER BY columna desconocida %q", name.Name)
		}
		rows, err = sortRows(db, rows, pos, sel.OrderBy.Descendent)
		if err != nil {
			return nil, err
		}
	}

	res := &Result{Columns: outCols}
	for _, r := range rows {
		proj := make([]any, len(projPos))
		for i, p := range projPos {
			proj[i] = r.Values[p]
		}
		res.Rows = append(res.Rows, proj)
	}
	return res, nil
}

func sortRows(db *Database, rows []shared.Record, pos int, desc bool) ([]shared.Record, error) {
	sorter := sorting.New(2, db.dir)
	out, err := sorter.Sort(iterator.NewSliceIterator(rows), func(r shared.Record) any {
		return r.Values[pos]
	})
	if err != nil {
		return nil, err
	}
	sorted, err := iterator.Drain(out)
	if err != nil {
		return nil, err
	}
	if desc {
		for i, j := 0, len(sorted)-1; i < j; i, j = i+1, j-1 {
			sorted[i], sorted[j] = sorted[j], sorted[i]
		}
	}
	return sorted, nil
}

func (db *Database) execDelete(del *ast.Delete) (*Result, error) {
	t, ok := db.tables[del.From.Name]
	if !ok {
		return nil, fmt.Errorf("sql: tabla desconocida %q", del.From.Name)
	}
	if del.Closure == nil {
		return nil, fmt.Errorf("sql: DELETE requiere WHERE")
	}
	low, high, point, _, err := whereRangeFromClosure(del.Closure)
	if err != nil {
		return nil, err
	}

	var recs []bplus.UnclusteredRecord[int]
	if point {
		recs, err = t.idx.Search(low)
	} else {
		recs, err = t.idx.RangeSearch(low, high)
	}
	if err != nil {
		return nil, err
	}

	affected := int64(0)
	for _, r := range recs {
		if err := t.idx.Delete(r.Key, r.RID); err != nil {
			return nil, err
		}
		affected++
	}
	return &Result{Affected: affected}, nil
}

// ---------- helpers ----------

// keyOfRow extrae la clave (primera columna INT) de un payload gob.
func keyOfRow(payload []byte) (int, error) {
	vals, err := decodeRow(payload)
	if err != nil {
		return 0, err
	}
	key, ok := vals[0].(int)
	if !ok {
		return 0, fmt.Errorf("sql: primera columna no es int: %T", vals[0])
	}
	return key, nil
}

// openHeap reutiliza el archivo de datos si existe (los datos persisten pese a
// que el catálogo viva solo en memoria); el índice se reconstruye al abrirlo.
func openHeap(path string) (*heap.HeapFile, error) {
	if _, err := os.Stat(path); err == nil {
		return heap.Open(path, heap.DefaultPageSize)
	}
	return heap.Create(path, heap.DefaultPageSize)
}

// whereRange traduce el WHERE de un SELECT a un rango inclusivo [low, high]
// sobre la clave. point=true cuando es igualdad.
func whereRange(sel *ast.Select) (low, high int, point, hasWhere bool, err error) {
	if sel.Closure == nil {
		return 0, 0, false, false, nil
	}
	return whereRangeFromClosure(sel.Closure)
}

func whereRangeFromClosure(closure ast.ASTNode) (low, high int, point, hasWhere bool, err error) {
	wh, ok := closure.(*ast.WhereExpr)
	if !ok {
		return 0, 0, false, false, fmt.Errorf("sql: cláusula %T no soportada", closure)
	}
	bin := wh.Content
	left, ok := bin.Left.(*ast.IdExpr)
	if !ok {
		return 0, 0, false, false, fmt.Errorf("sql: WHERE solo soporta la columna clave")
	}
	v, ok := bin.Right.(*ast.IntExpr)
	if !ok {
		return 0, 0, false, false, fmt.Errorf("sql: WHERE solo soporta comparación contra entero")
	}
	_ = left
	switch bin.Op {
	case ast.OpEq:
		return v.Value, v.Value, true, true, nil
	case ast.OpGte:
		return v.Value, math.MaxInt, false, true, nil
	case ast.OpGt:
		return v.Value + 1, math.MaxInt, false, true, nil
	case ast.OpLte:
		return math.MinInt, v.Value, false, true, nil
	case ast.OpLt:
		return math.MinInt, v.Value - 1, false, true, nil
	default:
		return 0, 0, false, false, fmt.Errorf("sql: operador %v no soportado en WHERE", bin.Op)
	}
}

func literalValue(node ast.ASTNode) (any, error) {
	switch n := node.(type) {
	case *ast.IntExpr:
		return n.Value, nil
	case *ast.StringExpr:
		return strings.Trim(n.Value, "'\""), nil
	case *ast.FloatExpr:
		return float64(n.Value), nil
	case *ast.BoolExpr:
		return n.Value, nil
	default:
		return nil, fmt.Errorf("literal no soportado %T", node)
	}
}

func zeroValue(typeName string) any {
	switch strings.ToUpper(typeName) {
	case "INT":
		return 0
	case "STRING":
		return ""
	case "DECIMAL":
		return float64(0)
	case "BOOL":
		return false
	default:
		return nil
	}
}

func encodeRow(vals []any) ([]byte, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(vals); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decodeRow(payload []byte) ([]any, error) {
	var vals []any
	if err := gob.NewDecoder(bytes.NewReader(payload)).Decode(&vals); err != nil {
		return nil, err
	}
	return vals, nil
}