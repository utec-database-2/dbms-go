package parser

import (
	"strings"
	"testing"

	"github.com/dbms-go/v2/dbms/lib/dsl/ast"
	"github.com/dbms-go/v2/dbms/lib/dsl/lexer"
)

func parseAll(t *testing.T, src string) ([]ast.ASTNode, error) {
	t.Helper()
	return ParseAll(lexer.Tokenize(src, "\n"))
}

func kindOf(n ast.ASTNode) string {
	switch n.(type) {
	case *ast.CreateTable:
		return "create"
	case *ast.Insert:
		return "insert"
	case *ast.Select:
		return "select"
	case *ast.Update:
		return "update"
	case *ast.Delete:
		return "delete"
	default:
		return "otro"
	}
}

func TestParseAllVariasSentencias(t *testing.T) {
	script := "CREATE TABLE alumno (codigo INT PRIMARY KEY, nombre VARCHAR(40));\n" +
		"INSERT INTO alumno VALUES (1, 'Ana', 5), (2, 'Bob', 5);\n" +
		"\tSELECT * FROM alumno;;\n" +
		"UPDATE alumno SET ciclo = 6 WHERE codigo = 1;\n" +
		"DELETE FROM alumno WHERE codigo = 2;"

	stmts, err := parseAll(t, script)
	if err != nil {
		t.Fatalf("el script debía parsear: %v", err)
	}
	want := []string{"create", "insert", "select", "update", "delete"}
	if len(stmts) != len(want) {
		t.Fatalf("hay %d sentencias, se esperaban %d", len(stmts), len(want))
	}
	for i, w := range want {
		if got := kindOf(stmts[i]); got != w {
			t.Errorf("sentencia %d: es %s, se esperaba %s", i+1, got, w)
		}
	}
}

func TestParseAllPuntoYCommaDentroDeLiteral(t *testing.T) {
	// Un ";" dentro de un literal no separa sentencias: eso lo decide el
	// lexer, que empaqueta el literal completo en un solo token.
	stmts, err := parseAll(t, "INSERT INTO salas VALUES (1, 'Aula; B'); SELECT * FROM salas;")
	if err != nil {
		t.Fatalf("no debía fallar: %v", err)
	}
	if len(stmts) != 2 {
		t.Fatalf("hay %d sentencias, se esperaban 2", len(stmts))
	}
	if kindOf(stmts[0]) != "insert" {
		t.Errorf("la primera es %s, se esperaba insert", kindOf(stmts[0]))
	}
}

func TestParseAllEntradaVacia(t *testing.T) {
	for _, src := range []string{"", "   \n\t ", ";", " ; ;; \n ; "} {
		stmts, err := parseAll(t, src)
		if err == nil {
			t.Errorf("%q: se esperaba error de entrada vacía", src)
			continue
		}
		if !strings.Contains(err.Error(), "empty input") {
			t.Errorf("%q: el error fue %v, se esperaba empty input", src, err)
		}
		if stmts != nil {
			t.Errorf("%q: no debe devolver sentencias: %v", src, stmts)
		}
	}
}

func TestParseAllErrorIndicaSentencia(t *testing.T) {
	stmts, err := parseAll(t, "SELECT * FROM alumnos; FOO BAR; SELECT * FROM x;")
	if err == nil {
		t.Fatal("se esperaba error de sintaxis")
	}
	if !strings.Contains(err.Error(), "sentencia 2") {
		t.Errorf("el error debe decir cuál sentencia falló: %v", err)
	}
	if !strings.Contains(err.Error(), "FOO") {
		t.Errorf("el error debe señalar el token problemático: %v", err)
	}
	if stmts != nil {
		t.Errorf("con error de sintaxis no se devuelve ninguna sentencia: %v", stmts)
	}
}

func TestParseSigueExigiendoUnaSentencia(t *testing.T) {
	// Una sentencia con ";" final: válido, como hasta ahora.
	p := Parse(lexer.Tokenize("SELECT * FROM alumnos;", "\n"))
	if p.Err() != nil {
		t.Fatalf("una sentencia con ';' debía parsear: %v", p.Err())
	}
	if _, ok := p.Parent().(*ast.Select); !ok {
		t.Errorf("el nodo raíz es %T, se esperaba *ast.Select", p.Parent())
	}

	// Dos sentencias: Parse sigue rechazándolas (contrato de una sentencia).
	p = Parse(lexer.Tokenize("SELECT * FROM a; SELECT * FROM b;", "\n"))
	if p.Err() == nil {
		t.Fatal("Parse debe seguir fallando con dos sentencias")
	}
	if !strings.Contains(p.Err().Error(), "trailing") {
		t.Errorf("el error fue %v, se esperaba trailing token", p.Err())
	}

	// Entrada vacía: igual que antes.
	p = Parse(lexer.Tokenize("", "\n"))
	if p.Err() == nil || !strings.Contains(p.Err().Error(), "empty input") {
		t.Errorf("entrada vacía: el error fue %v, se esperaba empty input", p.Err())
	}
}
