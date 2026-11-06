package parser

import (
	"strings"
	"testing"

	"github.com/dbms-go/v2/dbms/lib/dsl/ast"
	"github.com/dbms-go/v2/dbms/lib/dsl/lexer"
)

func TestRankedSelectBM25TFIDF(t *testing.T) {
	for _, tc := range []struct {
		q, method string
		all       bool
		selected  int
	}{
		{"SELECT * FROM docs WHERE MATCH(texto, 'base datos') USING TF_IDF LIMIT 10", "tf_idf", true, 0},
		{"SELECT *, SCORE() AS relevancia FROM docs WHERE MATCH(texto, 'machine learning') USING BM25 ORDER BY relevancia DESC", "bm25", true, 1},
		{"SELECT id, SCORE() AS relevancia FROM docs WHERE MATCH(texto,'datos') USING bm25 ORDER BY relevancia DESC", "bm25", false, 2},
	} {
		p := Parse(lexer.Tokenize(tc.q, "\n"))
		if p.Err() != nil {
			t.Fatalf("%q: %v", tc.q, p.Err())
		}
		n, ok := p.Parent().(*ast.Select)
		if !ok || n.TextRankMethod != tc.method || n.All != tc.all || len(n.Selected) != tc.selected {
			t.Fatalf("%q: %#v", tc.q, n)
		}
		if _, ok := n.Closure.(*ast.FuncCallExpr); !ok {
			t.Fatalf("WHERE MATCH perdido: %#v", n.Closure)
		}
	}
}

func TestRankedSelectUnknownMethodRejected(t *testing.T) {
	for _, q := range []string{
		"SELECT * FROM docs WHERE MATCH(texto,'datos') USING FAST",
		"SELECT * FROM docs WHERE MATCH(texto,'datos') USING",
	} {
		p := Parse(lexer.Tokenize(q, "\n"))
		if p.Err() == nil || !strings.Contains(strings.ToLower(p.Err().Error()), "using") {
			t.Fatalf("no rechazó %q: %v", q, p.Err())
		}
	}
}
