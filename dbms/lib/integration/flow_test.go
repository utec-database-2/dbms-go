// Package integration contiene tests end-to-end que cablean las capas del
// motor (DSL -> storage -> índice -> operadores externos) tal como las usaría
// un futuro executor. Mientras no exista planner/executor, el "plan" se arma
// en el propio test.
package integration

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dbms-go/v2/dbms/lib/dsl/ast"
	"github.com/dbms-go/v2/dbms/lib/dsl/lexer"
	"github.com/dbms-go/v2/dbms/lib/dsl/parser"
	"github.com/dbms-go/v2/dbms/lib/external/iterator"
	"github.com/dbms-go/v2/dbms/lib/external/sorting"
	"github.com/dbms-go/v2/dbms/lib/shared"
	"github.com/dbms-go/v2/dbms/lib/storage"
	"github.com/dbms-go/v2/dbms/lib/storage/heap"
	"github.com/dbms-go/v2/indexes/bplus"
)

// ---------- DSL ----------

func parseSQL(t *testing.T, src string) ast.ASTNode {
	t.Helper()
	ctx := parser.Parse(lexer.Tokenize(src, "\n"))
	if err := ctx.Err(); err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	return ctx.Parent()
}

// ---------- serialización de filas ----------

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

// keyOfID es el KeyExtractor[int] del índice B+ no agrupado: lee la columna id
// (primera del payload) para reconstruir el índice desde el HeapFile.
func keyOfID(payload []byte) (int, error) {
	vals, err := decodeRow(payload)
	if err != nil {
		return 0, err
	}
	id, ok := vals[0].(int)
	if !ok {
		return 0, fmt.Errorf("expected int id, got %T", vals[0])
	}
	return id, nil
}

func astValue(node ast.ASTNode) (any, error) {
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
		return nil, fmt.Errorf("unsupported literal %T", node)
	}
}

// ---------- INSERT ----------

func insertFromAST(t *testing.T, idx *bplus.UnclusteredIndex[int], insert *ast.Insert) {
	t.Helper()
	for r, row := range insert.Rows {
		vals := make([]any, len(row))
		for c, node := range row {
			v, err := astValue(node)
			if err != nil {
				t.Fatalf("row %d col %d: %v", r, c, err)
			}
			vals[c] = v
		}
		payload, err := encodeRow(vals)
		if err != nil {
			t.Fatalf("encode row %d: %v", r, err)
		}
		key, ok := vals[0].(int)
		if !ok {
			t.Fatalf("row %d: la primera columna debe ser el id int, got %T", r, vals[0])
		}
		if _, err := idx.InsertWithKey(key, payload); err != nil {
			t.Fatalf("insert row %d: %v", r, err)
		}
	}
}

// ---------- SELECT ----------

func toRecord(t *testing.T, rid storage.RID, payload []byte) shared.Record {
	t.Helper()
	vals, err := decodeRow(payload)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return shared.Record{Values: vals, RID: rid}
}

// whereKeyRange interpreta un WHERE simple `id <op> <int>` y lo traduce a un
// rango inclusivo [low, high] para el índice (point=true si es igualdad).
func whereKeyRange(sel *ast.Select) (low, high int, point, hasWhere bool, err error) {
	if sel.Closure == nil {
		return 0, 0, false, false, nil
	}
	wh, ok := sel.Closure.(*ast.WhereExpr)
	if !ok {
		return 0, 0, false, false, fmt.Errorf("cláusula %T no soportada", sel.Closure)
	}
	bin := wh.Content
	left, ok := bin.Left.(*ast.IdExpr)
	if !ok || left.Name != "id" {
		return 0, 0, false, false, fmt.Errorf("solo se soporta WHERE id <op> int")
	}
	v, ok := bin.Right.(*ast.IntExpr)
	if !ok {
		return 0, 0, false, false, fmt.Errorf("solo se soporta comparación contra int")
	}
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
		return 0, 0, false, false, fmt.Errorf("operador %v no soportado", bin.Op)
	}
}

func orderBy(t *testing.T, recs []shared.Record, ob *ast.OrderBy) []shared.Record {
	t.Helper()
	id, ok := ob.Expr.(*ast.IdExpr)
	if !ok || id.Name != "id" {
		t.Fatalf("solo ORDER BY id soportado")
	}
	// External sorting ascendente; DESC se resuelve invirtiendo.
	sorter := sorting.New(2, t.TempDir())
	out, err := sorter.Sort(iterator.NewSliceIterator(recs), func(r shared.Record) any {
		return r.Values[0]
	})
	if err != nil {
		t.Fatalf("Sort: %v", err)
	}
	sorted, err := iterator.Drain(out)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if ob.Descendent {
		for i, j := 0, len(sorted)-1; i < j; i, j = i+1, j-1 {
			sorted[i], sorted[j] = sorted[j], sorted[i]
		}
	}
	return sorted
}

// runSelect ejecuta un SELECT sobre el índice: WHERE por búsqueda/rango en el
// B+ y ORDER BY por external sorting.
func runSelect(t *testing.T, idx *bplus.UnclusteredIndex[int], sel *ast.Select) []shared.Record {
	t.Helper()
	low, high, point, hasWhere, err := whereKeyRange(sel)
	if err != nil {
		t.Fatalf("plan WHERE: %v", err)
	}

	var found []shared.Record
	if !hasWhere {
		recs, err := idx.OrderedScan()
		if err != nil {
			t.Fatalf("OrderedScan: %v", err)
		}
		for _, r := range recs {
			found = append(found, toRecord(t, r.RID, r.Payload))
		}
	} else if point {
		recs, err := idx.Search(low)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		for _, r := range recs {
			found = append(found, toRecord(t, r.RID, r.Payload))
		}
	} else {
		recs, err := idx.RangeSearch(low, high)
		if err != nil {
			t.Fatalf("RangeSearch: %v", err)
		}
		for _, r := range recs {
			found = append(found, toRecord(t, r.RID, r.Payload))
		}
	}

	if sel.OrderBy != nil {
		found = orderBy(t, found, sel.OrderBy)
	}
	return found
}

// ---------- tests ----------

func newUsersTable(t *testing.T, path string) (*heap.HeapFile, *bplus.UnclusteredIndex[int]) {
	t.Helper()
	h, err := heap.Create(path, heap.DefaultPageSize)
	if err != nil {
		t.Fatalf("heap.Create: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	idx, err := bplus.NewUnclusteredIndex[int](4, h, keyOfID)
	if err != nil {
		t.Fatalf("NewUnclusteredIndex: %v", err)
	}
	return h, idx
}

func TestEndToEndSQLInsertSelectWhereOrderBy(t *testing.T) {
	dir := t.TempDir()

	// CREATE TABLE -> AST
	create := parseSQL(t, "CREATE TABLE users (id INT, name STRING);").(*ast.CreateTable)
	if create.Name != "users" || len(create.Columns) != 2 {
		t.Fatalf("CREATE inesperado: %+v", create)
	}

	// Storage + índice B+ no agrupado sobre id
	_, idx := newUsersTable(t, filepath.Join(dir, "users.heap"))

	// INSERT -> AST -> HeapFile (vía índice, que extrae la clave del payload)
	insert := parseSQL(t, "INSERT INTO users (id, name) VALUES (1, 'ana'), (3, 'carol'), (2, 'bob');").(*ast.Insert)
	insertFromAST(t, idx, insert)

	if err := idx.Validate(); err != nil {
		t.Fatalf("Validate tras INSERT: %v", err)
	}

	// SELECT con WHERE (rango en el índice) y ORDER BY DESC (external sort)
	sel := parseSQL(t, "SELECT * FROM users WHERE id >= 2 ORDER BY id DESC;").(*ast.Select)
	got := runSelect(t, idx, sel)

	want := []struct {
		id   int
		name string
	}{{3, "carol"}, {2, "bob"}}
	if len(got) != len(want) {
		t.Fatalf("SELECT devolvió %d filas, esperaba %d: %v", len(got), len(want), got)
	}
	for i, r := range got {
		if r.Values[0].(int) != want[i].id || r.Values[1].(string) != want[i].name {
			t.Fatalf("fila %d = %v, esperaba {%d %s}", i, r.Values, want[i].id, want[i].name)
		}
	}
}

func TestEndToEndPointLookup(t *testing.T) {
	dir := t.TempDir()
	_, idx := newUsersTable(t, filepath.Join(dir, "users.heap"))

	insert := parseSQL(t, "INSERT INTO users (id, name) VALUES (1, 'ana'), (2, 'bob'), (3, 'carol');").(*ast.Insert)
	insertFromAST(t, idx, insert)

	sel := parseSQL(t, "SELECT * FROM users WHERE id = 2;").(*ast.Select)
	got := runSelect(t, idx, sel)
	if len(got) != 1 {
		t.Fatalf("SELECT devolvió %d filas, esperaba 1", len(got))
	}
	if got[0].Values[0].(int) != 2 || got[0].Values[1].(string) != "bob" {
		t.Fatalf("fila = %v, esperaba {2 bob}", got[0].Values)
	}
}

func TestEndToEndPersistenceRebuildIndex(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.heap")

	h, idx := newUsersTable(t, path)
	insert := parseSQL(t, "INSERT INTO users (id, name) VALUES (1, 'ana'), (3, 'carol'), (2, 'bob');").(*ast.Insert)
	insertFromAST(t, idx, insert)
	if err := h.Close(); err != nil {
		t.Fatalf("close heap: %v", err)
	}

	// Reabrir: el B+ se reconstruye escaneando el HeapFile persistido.
	reopened, err := heap.Open(path, heap.DefaultPageSize)
	if err != nil {
		t.Fatalf("heap.Open: %v", err)
	}
	defer reopened.Close()

	idx2, err := bplus.NewUnclusteredIndex[int](4, reopened, keyOfID)
	if err != nil {
		t.Fatalf("Rebuild index: %v", err)
	}
	if err := idx2.Validate(); err != nil {
		t.Fatalf("Validate tras reabrir: %v", err)
	}

	sel := parseSQL(t, "SELECT * FROM users WHERE id >= 2;").(*ast.Select)
	got := runSelect(t, idx2, sel)

	want := []struct {
		id   int
		name string
	}{{2, "bob"}, {3, "carol"}}
	if len(got) != len(want) {
		t.Fatalf("SELECT devolvió %d filas, esperaba %d", len(got), len(want))
	}
	for i, r := range got {
		if r.Values[0].(int) != want[i].id || r.Values[1].(string) != want[i].name {
			t.Fatalf("fila %d = %v, esperaba {%d %s}", i, r.Values, want[i].id, want[i].name)
		}
	}
}