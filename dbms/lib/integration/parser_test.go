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