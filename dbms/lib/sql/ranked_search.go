package sql

// Búsqueda SQL sobre SPIMI real. El índice de una tabla/columna se construye
// una vez y se reutiliza entre sentencias; INSERT/UPDATE/DELETE lo invalidan.
// El bloqueo de sentencia de Engine serializa mutaciones con las búsquedas.

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/dbms-go/v2/dbms/lib/dsl/ast"
	"github.com/dbms-go/v2/dbms/lib/index/text/ranking"
	"github.com/dbms-go/v2/dbms/lib/index/text/spimi"
	"github.com/dbms-go/v2/dbms/lib/storage"
	textproc "github.com/dbms-go/v2/dbms/lib/text"
)

type sqlRankedIndex struct {
	dir    string
	reader *spimi.Reader
	rows   []storage.Tuple // docID -> fila, en el mismo orden de AddDocument
}

func (idx *sqlRankedIndex) close() {
	if idx == nil {
		return
	}
	if idx.reader != nil {
		_ = idx.reader.Close()
	}
	if idx.dir != "" {
		_ = os.RemoveAll(idx.dir)
	}
}

func (t *Table) clearRankedIndexes() {
	t.mu.Lock()
	old := t.textRanks
	t.textRanks = nil
	t.mu.Unlock()
	for _, idx := range old {
		idx.close()
	}
}

// rankedIndex reutiliza índices preparados por columna. No se expone un
// Reader sin estado: la consulta mantiene stmtMu hasta completar el resultado.
func (t *Table) rankedIndex(col int) (*sqlRankedIndex, error) {
	t.mu.RLock()
	existing := t.textRanks[col]
	t.mu.RUnlock()
	if existing != nil {
		return existing, nil
	}

	rows, err := t.scanTable()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "db2-sql-spimi-*")
	if err != nil {
		return nil, err
	}
	idx := &sqlRankedIndex{dir: dir, rows: make([]storage.Tuple, 0, len(rows))}
	completed := false
	defer func() {
		if !completed {
			idx.close()
		}
	}()
	builder, err := spimi.NewBuilder(spimi.Options{Dir: dir})
	if err != nil {
		return nil, err
	}
	defer builder.Close()
	for _, row := range rows {
		if col >= len(row) {
			return nil, fmt.Errorf("sql: columna textual fuera de la fila")
		}
		raw := ""
		if row[col] != nil {
			str, ok := row[col].(string)
			if !ok {
				return nil, fmt.Errorf("sql: MATCH requiere una columna de texto, recibio %T", row[col])
			}
			raw = str
		}
		terms, err := textproc.PreprocessText(raw)
		if err != nil {
			return nil, fmt.Errorf("sql: preprocesamiento SPIMI: %w", err)
		}
		docID, err := builder.AddDocument(terms)
		if err != nil {
			return nil, fmt.Errorf("sql: indexando fila: %w", err)
		}
		if int(docID) != len(idx.rows) {
			return nil, fmt.Errorf("sql: SPIMI asigno DocID inesperado: %d", docID)
		}
		idx.rows = append(idx.rows, append(storage.Tuple(nil), row...))
	}
	if _, err := builder.Finish(); err != nil {
		return nil, err
	}
	idx.reader, err = spimi.Open(dir)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	if t.textRanks == nil {
		t.textRanks = make(map[int]*sqlRankedIndex)
	}
	if prev := t.textRanks[col]; prev != nil {
		t.mu.Unlock()
		return prev, nil // en uso normal Engine.stmtMu evita este caso
	}
	t.textRanks[col] = idx
	t.mu.Unlock()
	completed = true
	return idx, nil
}

// Solo se activa para WHERE MATCH(columna,'texto') USING BM25/TF_IDF.
// Se rechazan explícitamente combinaciones aún no planificadas sobre SPIMI,
// en lugar de ignorar partes del WHERE o devolver scores inconsistentes.
func rankedMatchQuery(node ast.ASTNode) (*ast.FuncCallExpr, string, error) {
	fn, ok := node.(*ast.FuncCallExpr)
	if !ok || !isMatchFunc(fn.Name) {
		return nil, "", fmt.Errorf("sql: USING BM25/TF_IDF requiere WHERE MATCH(columna, 'consulta')")
	}
	if len(fn.Args) != 2 {
		return nil, "", fmt.Errorf("sql: MATCH con USING requiere exactamente dos argumentos")
	}
	s, ok := fn.Args[1].(*ast.StringExpr)
	if !ok {
		return nil, "", fmt.Errorf("sql: el argumento consulta de MATCH debe ser un literal de texto")
	}
	return fn, s.Value, nil
}

func sqlRankLimit(n *ast.Select, max int) (offset, length int, err error) {
	offset, length = 0, max
	if n.Limit == nil {
		return
	}
	count, ok := n.Limit.Count.(*ast.IntExpr)
	if !ok || count.Value < 0 {
		return 0, 0, fmt.Errorf("sql: LIMIT debe ser un entero no negativo")
	}
	length = count.Value
	if n.Limit.Offset != nil {
		o, ok := n.Limit.Offset.(*ast.IntExpr)
		if !ok || o.Value < 0 {
			return 0, 0, fmt.Errorf("sql: OFFSET debe ser un entero no negativo")
		}
		offset = o.Value
	}
	if offset > max {
		offset = max
	}
	if length > max-offset {
		length = max - offset
	}
	return
}

type sqlRankHit struct {
	docID uint32
	score float64
	row   storage.Tuple
}

// orderBy ya pasó por resolveOrderByAlias y acepta SCORE() o columnas base.
func sqlRankSort(tbl *Table, n *ast.Select, hits []sqlRankHit) error {
	if n.OrderBy == nil {
		return nil
	} // ranking.Search ya devuelve desc y desempate estable
	expr := n.OrderBy.Expr
	isScore := false
	var col int
	switch v := expr.(type) {
	case *ast.FuncCallExpr:
		if !strings.EqualFold(v.Name, "score") || len(v.Args) != 0 {
			return fmt.Errorf("sql: ORDER BY de MATCH solo admite SCORE() o columna")
		}
		isScore = true
	case *ast.IdExpr:
		i, ok := tbl.ColumnIndex(v.Name)
		if !ok {
			return fmt.Errorf("sql: ORDER BY columna desconocida %q", v.Name)
		}
		col = i
	case *ast.NameExpr:
		i, ok := tbl.ColumnIndex(v.Name)
		if !ok {
			return fmt.Errorf("sql: ORDER BY columna desconocida %q", v.Name)
		}
		col = i
	default:
		return fmt.Errorf("sql: ORDER BY de MATCH solo admite SCORE() o columna")
	}
	// sort.SliceStable retiene orden por score/DocID ante empates de claves.
	sort.SliceStable(hits, func(i, j int) bool {
		var c int
		if isScore {
			if hits[i].score < hits[j].score {
				c = -1
			} else if hits[i].score > hits[j].score {
				c = 1
			}
		} else {
			c = compareAny(hits[i].row[col], hits[j].row[col])
		}
		if n.OrderBy.Descendent {
			return c > 0
		}
		return c < 0
	})
	return nil
}

func sqlRankProject(tbl *Table, n *ast.Select, hits []sqlRankHit) ([]string, [][]any, error) {
	names := make([]string, 0)
	appendAll := func() { names = append(names, tbl.Schema.Columns...) }
	if n.All || len(n.Selected) == 0 {
		appendAll()
	}
	type projection struct {
		kind int
		col  int
		node ast.ASTNode
	}
	items := make([]projection, 0, len(n.Selected))
	// Si SELECT * precede SELECT ..., las columnas base se proyectan primero.
	for range names {
		items = append(items, projection{kind: 0, col: len(items)})
	}
	for _, sel := range n.Selected {
		expr, alias := unwrapSelectItem(sel)
		if _, ok := expr.(*ast.StarExpr); ok {
			if n.All {
				return nil, nil, fmt.Errorf("sql: SELECT * duplicado")
			}
			for c, name := range tbl.Schema.Columns {
				names = append(names, name)
				items = append(items, projection{kind: 0, col: c})
			}
			continue
		}
		name := exprLabel(expr)
		if alias != nil {
			name = *alias
		}
		switch v := expr.(type) {
		case *ast.FuncCallExpr:
			if !strings.EqualFold(v.Name, "score") || len(v.Args) != 0 {
				return nil, nil, fmt.Errorf("sql: en SELECT con MATCH solo se admite SCORE() como funcion")
			}
			items = append(items, projection{kind: 1})
		case *ast.IdExpr:
			c, ok := tbl.ColumnIndex(v.Name)
			if !ok {
				return nil, nil, fmt.Errorf("sql: columna %q no existe", v.Name)
			}
			items = append(items, projection{kind: 0, col: c})
		case *ast.NameExpr:
			c, ok := tbl.ColumnIndex(v.Name)
			if !ok {
				return nil, nil, fmt.Errorf("sql: columna %q no existe", v.Name)
			}
			items = append(items, projection{kind: 0, col: c})
		default:
			return nil, nil, fmt.Errorf("sql: proyeccion de MATCH solo admite columnas, * y SCORE()")
		}
		names = append(names, name)
	}
	out := make([][]any, 0, len(hits))
	for _, hit := range hits {
		line := make([]any, 0, len(items))
		for _, it := range items {
			if it.kind == 1 {
				line = append(line, hit.score)
			} else {
				line = append(line, hit.row[it.col])
			}
		}
		out = append(out, line)
	}
	if n.Distinct {
		names, out = distinct(names, out)
	}
	return names, out, nil
}

func (e *Engine) execRankedSelect(tbl *Table, n *ast.Select) (*Result, error) {
	if len(n.Join) > 0 || len(n.GroupBy) > 0 || n.Having != nil || selectHasAggregate(n) {
		return nil, fmt.Errorf("sql: MATCH USING aún no soporta JOIN/GROUP BY/HAVING/agregados")
	}
	fn, query, err := rankedMatchQuery(n.Closure)
	if err != nil {
		return nil, err
	}
	col, ok := matchColumn(tbl, fn.Args[0])
	if !ok {
		return nil, fmt.Errorf("sql: MATCH necesita una columna textual válida")
	}
	idx, err := tbl.rankedIndex(col)
	if err != nil {
		return nil, err
	}
	terms, err := textproc.PreprocessText(query)
	if err != nil {
		return nil, err
	}
	method := ranking.MethodBM25
	if strings.EqualFold(n.TextRankMethod, "tf_idf") {
		method = ranking.MethodTFIDF
	}
	requested := idx.reader.NumDocs()
	if n.Limit != nil {
		off, count, err := sqlRankLimit(n, requested)
		if err != nil {
			return nil, err
		}
		byScore := n.OrderBy == nil
		if n.OrderBy != nil {
			if fn, ok := n.OrderBy.Expr.(*ast.FuncCallExpr); ok && strings.EqualFold(fn.Name, "score") && n.OrderBy.Descendent {
				byScore = true
			}
		}
		if byScore {
			requested = off + count
		}
	}
	opts := ranking.DefaultSearchOptions(requested)
	results, err := ranking.Search(idx.reader, method, terms, opts)
	if err != nil {
		return nil, err
	}
	hits := make([]sqlRankHit, 0, len(results))
	for _, r := range results {
		if int(r.DocID) >= len(idx.rows) {
			return nil, fmt.Errorf("sql: posting SPIMI referencia DocID fuera de rango")
		}
		hits = append(hits, sqlRankHit{docID: r.DocID, score: r.Score, row: idx.rows[r.DocID]})
	}
	if err = sqlRankSort(tbl, n, hits); err != nil {
		return nil, err
	}
	offset, count, err := sqlRankLimit(n, len(hits))
	if err != nil {
		return nil, err
	}
	if offset > len(hits) {
		offset = len(hits)
	}
	if count > len(hits)-offset {
		count = len(hits) - offset
	}
	hits = hits[offset : offset+count]
	tuples := make([]storage.Tuple, 0, len(hits))
	for _, h := range hits {
		tuples = append(tuples, h.row)
	}
	if err = e.lockRowsShared(tbl, tuples); err != nil {
		return nil, err
	}
	cols, rows, err := sqlRankProject(tbl, n, hits)
	if err != nil {
		return nil, err
	}
	return &Result{Columns: cols, Rows: rows, Message: fmt.Sprintf("%d fila(s)", len(rows)), Plan: []Step{
		{Kind: StepIndexScan, Detail: fmt.Sprintf("SPIMI columna %s; ranking %s; %d candidatos", tbl.Schema.Columns[col], method, len(results)), Rows: len(results)},
		{Kind: StepProject, Detail: "SELECT con MATCH y SCORE()", Rows: len(rows)},
	}}, nil
}
