package sql

import (
	"fmt"
	"math"
	"strings"
	"unicode"

	"github.com/dbms-go/v2/dbms/lib/dsl/ast"
)

// isMatchFunc reconoce la función de búsqueda textual del dialecto del motor.
func isMatchFunc(name string) bool {
	switch strings.ToLower(name) {
	case "match", "matches", "match_against":
		return true
	}
	return false
}

// tokenize parte un texto en términos: cualquier carácter que no sea letra o
// dígito es separador y los términos se normalizan a minúsculas.
func tokenize(s string) []string {
	raw := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(raw))
	for _, w := range raw {
		out = append(out, strings.ToLower(w))
	}
	return out
}

// textCorpus es la estadística de una columna: cuántos documentos (filas) hay y
// en cuántos aparece cada término (document frequency), que alimenta el IDF.
type textCorpus struct {
	docs int
	df   map[string]int
}

// idf devuelve el inverso de la frecuencia de documento con suavizado, de modo
// que nunca vale cero y un término presente en todas las filas no anula el
// score.
func (c *textCorpus) idf(term string) float64 {
	return math.Log(float64(c.docs+1)/float64(c.df[term]+1)) + 1
}

// textStats devuelve la estadística del corpus de una columna, construyéndola
// la primera vez y cacheándola en la tabla. Una sola pasada por la tabla sirve
// para puntuar todas las filas de la consulta.
func (t *Table) textStats(col int) (*textCorpus, error) {
	t.mu.RLock()
	cached := t.text[col]
	t.mu.RUnlock()
	if cached != nil {
		return cached, nil
	}

	rows, err := t.scanTable()
	if err != nil {
		return nil, err
	}
	df := make(map[string]int)
	for _, row := range rows {
		if col >= len(row) {
			continue
		}
		s, _ := row[col].(string)
		seen := make(map[string]bool)
		for _, tok := range tokenize(s) {
			if seen[tok] {
				continue
			}
			seen[tok] = true
			df[tok]++
		}
	}

	c := &textCorpus{docs: len(rows), df: df}
	t.mu.Lock()
	if t.text == nil {
		t.text = make(map[int]*textCorpus)
	}
	t.text[col] = c
	t.mu.Unlock()
	return c, nil
}

// invalidateText descarta la estadística textual cacheada tras una escritura.
func (t *Table) invalidateText() {
	t.mu.Lock()
	t.text = nil
	t.mu.Unlock()
}

// scoreTFIDF mide la similitud entre un documento y una consulta: suma, para
// cada término de la consulta presente en el documento, su frecuencia relativa
// en el documento (TF) ponderada por el IDF del término. Sin IDF queda un
// score puramente léxico (TF).
func scoreTFIDF(doc, query string, c *textCorpus, useIDF bool) float64 {
	docTerms := tokenize(doc)
	if len(docTerms) == 0 {
		return 0
	}
	tf := make(map[string]int, len(docTerms))
	for _, w := range docTerms {
		tf[w]++
	}

	seen := make(map[string]bool)
	var score float64
	for _, q := range tokenize(query) {
		if seen[q] {
			continue
		}
		seen[q] = true
		n := tf[q]
		if n == 0 {
			continue
		}
		w := float64(n) / float64(len(docTerms))
		if useIDF {
			w *= c.idf(q)
		}
		score += w
	}
	return score
}

// matchColumn extrae la columna del primer argumento de MATCH.
func matchColumn(t *Table, node ast.ASTNode) (int, bool) {
	switch n := node.(type) {
	case *ast.IdExpr:
		return t.ColumnIndex(n.Name)
	case *ast.NameExpr:
		return t.ColumnIndex(n.Name)
	}
	return 0, false
}

// evalMatchFunc evalúa MATCH(columna, consulta[, modelo]). Devuelve el score
// TF-IDF del documento de la fila frente a la consulta; ok es false si el
// nombre no corresponde a la función de búsqueda.
func (ec *evalContext) evalMatchFunc(fn *ast.FuncCallExpr) (any, bool, error) {
	if !isMatchFunc(fn.Name) {
		return nil, false, nil
	}
	if len(fn.Args) < 2 || len(fn.Args) > 3 {
		return nil, true, fmt.Errorf("sql: MATCH espera (columna, consulta[, modelo])")
	}
	if ec.table == nil {
		return nil, true, fmt.Errorf("sql: MATCH necesita una tabla de la que leer la columna")
	}
	col, ok := matchColumn(ec.table, fn.Args[0])
	if !ok {
		return nil, true, fmt.Errorf("sql: MATCH espera una columna como primer argumento")
	}

	useIDF := true
	if len(fn.Args) == 3 {
		v, err := ec.eval(fn.Args[2])
		if err != nil {
			return nil, true, err
		}
		switch strings.ToLower(strings.TrimSpace(fmt.Sprint(v))) {
		case "tf-idf", "tfidf", "tf_idf":
			useIDF = true
		case "tf":
			useIDF = false
		default:
			return nil, true, fmt.Errorf("sql: modelo de MATCH no soportado %q (usa 'tf-idf' o 'tf')", v)
		}
	}

	qv, err := ec.eval(fn.Args[1])
	if err != nil {
		return nil, true, err
	}
	query, ok := qv.(string)
	if !ok {
		return nil, true, fmt.Errorf("sql: la consulta de MATCH debe ser texto, llegó %T", qv)
	}

	var doc string
	if col < len(ec.row) {
		doc, _ = ec.row[col].(string)
	}
	if doc == "" || query == "" {
		return float64(0), true, nil
	}

	stats, err := ec.table.textStats(col)
	if err != nil {
		return nil, true, err
	}
	return scoreTFIDF(doc, query, stats, useIDF), true, nil
}
