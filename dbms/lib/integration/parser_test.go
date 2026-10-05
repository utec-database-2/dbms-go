package integration

import (
	"testing"

	"github.com/dbms-go/v2/dbms/lib/dsl/ast"
	"github.com/dbms-go/v2/dbms/lib/dsl/lexer"
	"github.com/dbms-go/v2/dbms/lib/dsl/parser"
)

func parse(t *testing.T, src string) parser.ParserContext {
	t.Helper()
	return parser.Parse(lexer.Tokenize(src, "\n"))
}

func TestParseSelectAll(t *testing.T) {
	ctx := parse(t, "SELECT * FROM users;")
	if err := ctx.Err(); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sel, ok := ctx.Parent().(*ast.Select)
	if !ok {
		t.Fatalf("got %T, want *ast.Select", ctx.Parent())
	}
	if !sel.All {
		t.Fatalf("All = false, want true")
	}
	if sel.From.Name != "users" {
		t.Fatalf("From.Name = %q, want %q", sel.From.Name, "users")
	}
	if sel.Closure != nil {
		t.Fatalf("Closure = %v, want nil", sel.Closure)
	}
}

func TestParseSelectColumnsAsOrderBy(t *testing.T) {
	ctx := parse(t, "SELECT id, name FROM users AS u WHERE age >= 18 ORDER BY name DESC;")
	if err := ctx.Err(); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sel := ctx.Parent().(*ast.Select)

	if sel.All {
		t.Fatalf("All = true, want false")
	}
	if len(sel.Selected) != 2 {
		t.Fatalf("Selected len = %d, want 2", len(sel.Selected))
	}
	if sel.Selected[0].Name != "id" || sel.Selected[1].Name != "name" {
		t.Fatalf("Selected names = [%q %q], want [id name]", sel.Selected[0].Name, sel.Selected[1].Name)
	}
	if sel.From.Name != "users" || sel.From.Alias == nil || *sel.From.Alias != "u" {
		t.Fatalf("From = %+v, want users AS u", sel.From)
	}

	wh, ok := sel.Closure.(*ast.WhereExpr)
	if !ok {
		t.Fatalf("Closure = %T, want *ast.WhereExpr", sel.Closure)
	}
	bin := wh.Content
	if bin.Left.(*ast.IdExpr).Name != "age" || bin.Op != ast.OpGte || bin.Right.(*ast.IntExpr).Value != 18 {
		t.Fatalf("WHERE = %+v, want age >= 18", bin)
	}

	if sel.OrderBy == nil || sel.OrderBy.Expr.(*ast.IdExpr).Name != "name" || !sel.OrderBy.Descendent {
		t.Fatalf("OrderBy = %+v, want name DESC", sel.OrderBy)
	}
}

func TestParseConditionAndOrPrecedence(t *testing.T) {
	ctx := parse(t, "SELECT * FROM t WHERE x = 1 AND y = 2 OR z != 'k';")
	if err := ctx.Err(); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sel := ctx.Parent().(*ast.Select)
	wh := sel.Closure.(*ast.WhereExpr)

	root := wh.Content
	if root.Op != ast.OpOr {
		t.Fatalf("root op = %v, want OpOr", root.Op)
	}
	and, ok := root.Left.(*ast.BinaryExpr)
	if !ok || and.Op != ast.OpAnd {
		t.Fatalf("left = %+v, want BinaryExpr AND", root.Left)
	}
	neq, ok := root.Right.(*ast.BinaryExpr)
	if !ok || neq.Op != ast.OpNeq {
		t.Fatalf("right = %+v, want BinaryExpr NEQ", root.Right)
	}
	if and.Right.(*ast.BinaryExpr).Right.(*ast.IntExpr).Value != 2 || neq.Right.(*ast.StringExpr).Value != "'k'" {
		t.Fatalf("unexpected operands: %+v / %+v", and.Right, neq.Right)
	}
}

func TestParseInsertMultiRow(t *testing.T) {
	ctx := parse(t, "INSERT INTO users (id, name) VALUES (1, 'ana'), (2, 'bob');")
	if err := ctx.Err(); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ins := ctx.Parent().(*ast.Insert)
	if ins.Table.Name != "users" {
		t.Fatalf("Table = %q, want users", ins.Table.Name)
	}
	if len(ins.Columns) != 2 || ins.Columns[0].Name != "id" || ins.Columns[1].Name != "name" {
		t.Fatalf("Columns = %+v, want [id name]", ins.Columns)
	}
	if len(ins.Rows) != 2 {
		t.Fatalf("Rows len = %d, want 2", len(ins.Rows))
	}
	if ins.Rows[0][0].(*ast.IntExpr).Value != 1 || ins.Rows[0][1].(*ast.StringExpr).Value != "'ana'" {
		t.Fatalf("row 0 = %+v, want [1 'ana']", ins.Rows[0])
	}
	if ins.Rows[1][1].(*ast.StringExpr).Value != "'bob'" {
		t.Fatalf("row 1 = %+v, want [2 'bob']", ins.Rows[1])
	}
}

func TestParseCreateTable(t *testing.T) {
	ctx := parse(t, "CREATE TABLE users (id INT, name STRING);")
	if err := ctx.Err(); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ct := ctx.Parent().(*ast.CreateTable)
	if ct.Name != "users" {
		t.Fatalf("Name = %q, want users", ct.Name)
	}
	if len(ct.Columns) != 2 {
		t.Fatalf("Columns len = %d, want 2", len(ct.Columns))
	}
	if ct.Columns[0].Name.Name != "id" || ct.Columns[0].Type.Name != "INT" {
		t.Fatalf("col 0 = %+v, want id INT", ct.Columns[0])
	}
	if ct.Columns[1].Name.Name != "name" || ct.Columns[1].Type.Name != "STRING" {
		t.Fatalf("col 1 = %+v, want name STRING", ct.Columns[1])
	}
}

func TestParseDelete(t *testing.T) {
	ctx := parse(t, "DELETE FROM logs WHERE level = 3;")
	if err := ctx.Err(); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	del := ctx.Parent().(*ast.Delete)
	if del.From.Name != "logs" {
		t.Fatalf("From.Name = %q, want logs", del.From.Name)
	}
	wh := del.Closure.(*ast.WhereExpr)
	bin := wh.Content
	if bin.Left.(*ast.IdExpr).Name != "level" || bin.Op != ast.OpEq || bin.Right.(*ast.IntExpr).Value != 3 {
		t.Fatalf("WHERE = %+v, want level = 3", bin)
	}
}

func TestParseEmptyInputFails(t *testing.T) {
	ctx := parse(t, "")
	if ctx.Err() == nil {
		t.Fatalf("expected error for empty input")
	}
}

func TestParseUnknownStatementFails(t *testing.T) {
	ctx := parse(t, "FOOBAR x;")
	if ctx.Err() == nil {
		t.Fatalf("expected error for unknown statement")
	}
}

func TestParseTrailingTokensFail(t *testing.T) {
	ctx := parse(t, "SELECT * FROM t WHERE x; 1 + 2;")
	if ctx.Err() == nil {
		t.Fatalf("expected error for trailing tokens")
	}
}
func TestParseLimit(t *testing.T) {
	ctx := parse(t, "SELECT * FROM users ORDER BY id LIMIT 10;")
	if err := ctx.Err(); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sel := ctx.Parent().(*ast.Select)
	if sel.Limit == nil || *sel.Limit != 10 {
		t.Fatalf("Limit = %v, want 10", sel.Limit)
	}
	if sel.OrderBy == nil {
		t.Fatal("OrderBy = nil")
	}

	ctx = parse(t, "SELECT * FROM users LIMIT 3")
	if err := ctx.Err(); err != nil {
		t.Fatalf("Parse sin ORDER BY: %v", err)
	}
	if l := ctx.Parent().(*ast.Select).Limit; l == nil || *l != 3 {
		t.Fatalf("Limit sin ORDER BY = %v, want 3", l)
	}

	ctx = parse(t, "SELECT * FROM users;")
	if ctx.Parent().(*ast.Select).Limit != nil {
		t.Fatal("sin LIMIT, Limit debe ser nil")
	}
}

func TestParseLimitRequiresPositiveInteger(t *testing.T) {
	for _, src := range []string{
		"SELECT * FROM t LIMIT;",
		"SELECT * FROM t LIMIT -1;",
		"SELECT * FROM t LIMIT 2.5;",
		"SELECT * FROM t LIMIT abc;",
	} {
		ctx := parse(t, src)
		if err := ctx.Err(); err == nil {
			t.Errorf("%q debería fallar", src)
		}
	}
}

func TestParsePointLiteralKeepsFullPrecision(t *testing.T) {
	ctx := parse(t, "INSERT INTO t VALUES (1, POINT(-12.0464321987, -77.0428123456));")
	if err := ctx.Err(); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ins := ctx.Parent().(*ast.Insert)
	pt, ok := ins.Rows[0][1].(*ast.PointExpr)
	if !ok {
		t.Fatalf("valor = %T, want *ast.PointExpr", ins.Rows[0][1])
	}
	if pt.Lat != -12.0464321987 || pt.Lon != -77.0428123456 {
		t.Fatalf("POINT = (%v, %v), perdió precisión", pt.Lat, pt.Lon)
	}
}

func TestParsePointAcceptsIntegersAndIsCaseInsensitive(t *testing.T) {
	ctx := parse(t, "INSERT INTO t VALUES (1, point(0, -5));")
	if err := ctx.Err(); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	pt := ctx.Parent().(*ast.Insert).Rows[0][1].(*ast.PointExpr)
	if pt.Lat != 0 || pt.Lon != -5 {
		t.Fatalf("POINT = (%v, %v), want (0, -5)", pt.Lat, pt.Lon)
	}
}

func TestParseMalformedPointFails(t *testing.T) {
	for _, src := range []string{
		"INSERT INTO t VALUES (1, POINT(1));",
		"INSERT INTO t VALUES (1, POINT(1, 2, 3));",
		"INSERT INTO t VALUES (1, POINT(a, b));",
		"INSERT INTO t VALUES (1, POINT(1, 2);",
		"INSERT INTO t VALUES (1, POINT());",
	} {
		ctx := parse(t, src)
		if err := ctx.Err(); err == nil {
			t.Errorf("%q debería fallar", src)
		}
	}
}

func TestParseDistanceInWhere(t *testing.T) {
	ctx := parse(t, "SELECT * FROM tiendas WHERE distancia(ubicacion, POINT(-12.0464, -77.0428)) < 5000;")
	if err := ctx.Err(); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sel := ctx.Parent().(*ast.Select)
	bin := sel.Closure.(*ast.WhereExpr).Content

	call, ok := bin.Left.(*ast.CallExpr)
	if !ok {
		t.Fatalf("Left = %T, want *ast.CallExpr", bin.Left)
	}
	if call.Name != "distancia" || len(call.Args) != 2 {
		t.Fatalf("call = %s con %d argumentos, want distancia con 2", call.Name, len(call.Args))
	}
	if id, ok := call.Args[0].(*ast.IdExpr); !ok || id.Name != "ubicacion" {
		t.Fatalf("arg 0 = %#v, want columna ubicacion", call.Args[0])
	}
	if _, ok := call.Args[1].(*ast.PointExpr); !ok {
		t.Fatalf("arg 1 = %T, want *ast.PointExpr", call.Args[1])
	}
	if bin.Op != ast.OpLt {
		t.Fatalf("Op = %v, want OpLt", bin.Op)
	}
	if right, ok := bin.Right.(*ast.IntExpr); !ok || right.Value != 5000 {
		t.Fatalf("Right = %#v, want 5000", bin.Right)
	}
}

func TestParseOrderByDistanceWithLimit(t *testing.T) {
	ctx := parse(t, "SELECT * FROM restaurantes ORDER BY distancia(ubicacion, mi_ubicacion) LIMIT 10;")
	if err := ctx.Err(); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sel := ctx.Parent().(*ast.Select)

	call, ok := sel.OrderBy.Expr.(*ast.CallExpr)
	if !ok {
		t.Fatalf("OrderBy.Expr = %T, want *ast.CallExpr", sel.OrderBy.Expr)
	}
	if id, ok := call.Args[1].(*ast.IdExpr); !ok || id.Name != "mi_ubicacion" {
		t.Fatalf("arg 1 = %#v, want columna mi_ubicacion", call.Args[1])
	}
	if sel.OrderBy.Descendent {
		t.Fatal("sin DESC, el orden debe ser ascendente")
	}
	if sel.Limit == nil || *sel.Limit != 10 {
		t.Fatalf("Limit = %v, want 10", sel.Limit)
	}

	desc := parse(t, "SELECT * FROM r ORDER BY distancia(u, POINT(1, 2)) DESC LIMIT 5;")
	if err := desc.Err(); err != nil {
		t.Fatalf("Parse DESC: %v", err)
	}
	if !desc.Parent().(*ast.Select).OrderBy.Descendent {
		t.Fatal("DESC no se reconoció tras la llamada a función")
	}
}

func TestParseFunctionWithOptionalMetricArgument(t *testing.T) {
	ctx := parse(t, "SELECT * FROM t WHERE distancia(u, POINT(0, 0), 'euclidean') <= 2.5;")
	if err := ctx.Err(); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	call := ctx.Parent().(*ast.Select).Closure.(*ast.WhereExpr).Content.Left.(*ast.CallExpr)
	if len(call.Args) != 3 {
		t.Fatalf("args = %d, want 3", len(call.Args))
	}
	if _, ok := call.Args[2].(*ast.StringExpr); !ok {
		t.Fatalf("arg 2 = %T, want *ast.StringExpr", call.Args[2])
	}
}

func TestParseMalformedCallsFail(t *testing.T) {
	for _, src := range []string{
		"SELECT * FROM t WHERE distancia(u, POINT(1, 2) < 5;",
		"SELECT * FROM t ORDER BY distancia(;",
		"SELECT * FROM t WHERE distancia(, ) < 5;",
		"SELECT * FROM t ORDER BY 5;",
	} {
		ctx := parse(t, src)
		if err := ctx.Err(); err == nil {
			t.Errorf("%q debería fallar", src)
		}
	}
}

func TestParseDecimalKeepsDoublePrecision(t *testing.T) {
	ctx := parse(t, "INSERT INTO t VALUES (1, -12.0464321987);")
	if err := ctx.Err(); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	f := ctx.Parent().(*ast.Insert).Rows[0][1].(*ast.FloatExpr)
	if f.Value != -12.0464321987 {
		t.Fatalf("decimal = %v, perdió precisión", f.Value)
	}
}

func TestParseIncompleteStatementsFail(t *testing.T) {
	for _, src := range []string{
		"INSERT INTO t VALUES (1, 2;",
		"INSERT INTO t (id, name VALUES (1, 'a');",
		"INSERT INTO t VALUES 1, 2;",
		"CREATE TABLE t (id INT, name STRING;",
	} {
		ctx := parse(t, src)
		if err := ctx.Err(); err == nil {
			t.Errorf("%q debería fallar", src)
		}
	}
}
