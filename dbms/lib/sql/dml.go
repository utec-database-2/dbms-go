package sql

import (
	"fmt"
	"strings"

	"github.com/dbms-go/v2/dbms/lib/dsl/ast"
	"github.com/dbms-go/v2/dbms/lib/external/sorting"
	"github.com/dbms-go/v2/dbms/lib/storage"
)

// ---------------------------------------------------------------------------
// INSERT
// ---------------------------------------------------------------------------

// execInsert inserta filas usando el índice que decide dónde acaba cada una:
//   - tabla agrupada: el secuencial define el orden físico y el B+ agrupado
//     valida la unicidad de la PK.
//   - tabla no agrupada: se busca un índice sobre la clave primaria para validar
//     unicidad y obtener el RID; si no hay, se inserta en el heap y se registra
//     en los índices secundarios.
func (e *Engine) execInsert(n *ast.Insert) (*Result, error) {
	tbl, err := e.requireTable(n.Table.Name)
	if err != nil {
		return nil, err
	}

	cols, err := e.insertColumns(tbl, n.Columns)
	if err != nil {
		return nil, err
	}

	ec := &evalContext{table: tbl}
	affected := 0
	var detail string

	for _, rowNodes := range n.Rows {
		values := make(storage.Tuple, len(tbl.Schema.Columns))
		for i, col := range cols {
			if i >= len(rowNodes) {
				return nil, fmt.Errorf("sql: la fila tiene %d valores para %d columnas", len(rowNodes), len(cols))
			}
			ec.row = values
			v, err := ec.eval(rowNodes[i])
			if err != nil {
				return nil, err
			}
			values[col] = coerceToColumn(v, tbl.Schema.Types[col])
		}
		// Las columnas no listadas reciben su valor cero para que la tupla
		// siempre tenga la longitud del esquema.
		if len(rowNodes) != len(cols) {
			return nil, fmt.Errorf("sql: la fila tiene %d valores y se listaron %d columnas", len(rowNodes), len(cols))
		}

		if err := tbl.validateTuple(values); err != nil {
			return nil, err
		}

		rid, usedIdx, err := tbl.insertRow(values)
		if err != nil {
			return nil, err
		}
		affected++
		detail = fmt.Sprintf("fila insertada en %s", ridLabel(tbl, rid, usedIdx))
	}

	storage := storageLabel(tbl)
	plan := []Step{
		{Kind: StepInsert, Detail: fmt.Sprintf("insert en %s sobre %s", tbl.Name(), storage), Rows: affected},
	}
	if tbl.Clustered {
		plan = append(plan, Step{
			Kind:   StepIndexScan,
			Detail: "índice agrupado B+ actualizado con la clave primaria",
		})
	} else {
		plan = append(plan, Step{
			Kind:   StepIndexScan,
			Detail: fmt.Sprintf("RID asignado por heap file; %d índice(s) secundario(s) actualizados", len(tbl.IndexNames())),
		})
	}

	return &Result{
		Affected: affected,
		Message:  fmt.Sprintf("%d fila(s) insertada(s) en %s. %s", affected, tbl.Name(), detail),
		Plan:     plan,
	}, nil
}

// insertColumns resuelve el mapeo columna-valor. Sin lista explícita se asume el
// orden del esquema.
func (e *Engine) insertColumns(tbl *Table, named []ast.NameExpr) ([]int, error) {
	if len(named) == 0 {
		out := make([]int, len(tbl.Schema.Columns))
		for i := range out {
			out[i] = i
		}
		return out, nil
	}
	out := make([]int, 0, len(named))
	seen := make(map[int]bool)
	for _, c := range named {
		pos, ok := tbl.ColumnIndex(c.Name)
		if !ok {
			return nil, fmt.Errorf("sql: la columna %s no existe en %s", c.Name, tbl.Name())
		}
		if seen[pos] {
			return nil, fmt.Errorf("sql: la columna %s aparece dos veces en el INSERT", c.Name)
		}
		seen[pos] = true
		out = append(out, pos)
	}
	// Las columnas omitidas se rellenan con el valor cero del tipo.
	full := make([]int, len(tbl.Schema.Columns))
	for i := range full {
		full[i] = -1
	}
	for i, pos := range out {
		full[pos] = i
	}
	order := make([]int, 0, len(out))
	for _, p := range full {
		order = append(order, p)
	}
	return order, nil
}

// coerceToColumn ajusta el valor al tipo físico de la columna.
func coerceToColumn(v any, typ storage.Type) any {
	if v == nil {
		return nil
	}
	switch typ {
	case storage.TypeInt32:
		if i, ok := asInt64(v); ok {
			return int32(i)
		}
	case storage.TypeInt64:
		if i, ok := asInt64(v); ok {
			return i
		}
	case storage.TypeBool:
		if b, ok := v.(bool); ok {
			return b
		}
		if i, ok := asInt64(v); ok {
			return i != 0
		}
	case storage.TypeString:
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprint(v)
	case storage.TypeBytes:
		switch x := v.(type) {
		case []byte:
			return x
		case string:
			return []byte(x)
		}
	}
	return v
}

// ---------------------------------------------------------------------------
// UPDATE
// ---------------------------------------------------------------------------

func (e *Engine) execUpdate(n *ast.Update) (*Result, error) {
	tbl, err := e.requireTable(n.Table.Name)
	if err != nil {
		return nil, err
	}
	if len(n.Assignments) == 0 {
		return nil, fmt.Errorf("sql: UPDATE %s sin SET", tbl.Name())
	}

	// Resolver columnas destino una sola vez.
	targets := make([]int, len(n.Assignments))
	for i, a := range n.Assignments {
		pos, ok := tbl.ColumnIndex(a.Col.Name)
		if !ok {
			return nil, fmt.Errorf("sql: la columna %s no existe en %s", a.Col.Name, tbl.Name())
		}
		targets[i] = pos
	}

	ec := &evalContext{table: tbl}
	matches, access, err := tbl.fetch(n.Closure)
	if err != nil {
		return nil, err
	}

	affected := 0
	for _, row := range matches {
		next := make(storage.Tuple, len(row))
		copy(next, row)
		ec.row = next
		for i, a := range n.Assignments {
			v, err := ec.eval(a.Value)
			if err != nil {
				return nil, err
			}
			next[targets[i]] = coerceToColumn(v, tbl.Schema.Types[targets[i]])
		}
		if err := tbl.validateTuple(next); err != nil {
			return nil, err
		}
		if err := tbl.updateRow(row, next); err != nil {
			return nil, err
		}
		affected++
	}

	plan := append(append([]Step{}, access...), Step{
		Kind:   StepUpdate,
		Detail: fmt.Sprintf("update de %d fila(s) en %s", affected, tbl.Name()),
		Rows:   affected,
	})
	return &Result{
		Affected: affected,
		Message:  fmt.Sprintf("%d fila(s) actualizada(s) en %s", affected, tbl.Name()),
		Plan:     plan,
	}, nil
}

// ---------------------------------------------------------------------------
// DELETE
// ---------------------------------------------------------------------------

func (e *Engine) execDelete(n *ast.Delete) (*Result, error) {
	tbl, err := e.requireTable(n.From.Name)
	if err != nil {
		return nil, err
	}
	matches, access, err := tbl.fetch(n.Closure)
	if err != nil {
		return nil, err
	}
	for _, row := range matches {
		if err := tbl.deleteRow(row); err != nil {
			return nil, err
		}
	}
	plan := append(append([]Step{}, access...), Step{
		Kind:   StepDelete,
		Detail: fmt.Sprintf("delete de %d fila(s) de %s", len(matches), tbl.Name()),
		Rows:   len(matches),
	})
	return &Result{
		Affected: len(matches),
		Message:  fmt.Sprintf("%d fila(s) eliminada(s) de %s", len(matches), tbl.Name()),
		Plan:     plan,
	}, nil
}

// ---------------------------------------------------------------------------
// SELECT
// ---------------------------------------------------------------------------

// execSelect ejecuta una consulta completa: elección de acceso (índice o
// recorrido), filtro, GROUP BY, ORDER BY con external sorting, y LIMIT.
func (e *Engine) execSelect(n *ast.Select) (*Result, error) {
	if n.From.Name == "" {
		return nil, fmt.Errorf("sql: SELECT sin FROM (no se soporta SELECT de constantes)")
	}
	tbl, err := e.requireTable(n.From.Name)
	if err != nil {
		return nil, err
	}

	// 1. Acceso: el planner decide si puede usar un índice para el WHERE.
	matches, access, err := tbl.fetch(n.Closure)
	if err != nil {
		return nil, err
	}
	plan := access

	// 2. Cada fila es un grupo de una fila; con GROUP BY se fusionan las que
	// comparten los valores de sus claves.
	groups := make([]rowGroup, 0, len(matches))
	for _, r := range matches {
		groups = append(groups, rowGroup{rows: []storage.Tuple{r}})
	}
	if len(n.GroupBy) > 0 {
		var step []Step
		groups, step, err = groupRows(tbl, matches, n.GroupBy)
		if err != nil {
			return nil, err
		}
		plan = append(plan, step...)
	} else if n.Having != nil {
		return nil, fmt.Errorf("sql: HAVING sin GROUP BY")
	} else if selectHasAggregate(n) {
		// Un agregado sin GROUP BY colapsa toda la tabla en un único grupo.
		groups = []rowGroup{{rows: matches}}
	}

	// 3. HAVING sobre los grupos, ya con los agregados disponibles.
	if n.Having != nil {
		ec := &evalContext{table: tbl}
		groups, err = ec.keepGroups(groups, n.Having)
		if err != nil {
			return nil, err
		}
		plan = append(plan, Step{
			Kind:   StepFilter,
			Detail: fmt.Sprintf("having: %d grupo(s) superan la condición", len(groups)),
			Rows:   len(groups),
		})
	}

	// 4. ORDER BY: external sorting con k-way merge cuando el conjunto no cabe
	// en el buffer configurado.
	if n.OrderBy != nil {
		sorted, step, err := e.orderBy(tbl, groups, n.OrderBy)
		if err != nil {
			return nil, err
		}
		groups = sorted
		plan = append(plan, step)
	}

	// 5. LIMIT / OFFSET sobre los grupos resultantes.
	if n.Limit != nil {
		limited, step, err := e.applyLimit(tbl, groups, n.Limit)
		if err != nil {
			return nil, err
		}
		groups = limited
		plan = append(plan, step)
	}

	// 6. Proyección: DISTINCT, columnas y expresiones evaluadas por grupo.
	cols, projected, step, err := e.project(tbl, groups, n)
	if err != nil {
		return nil, err
	}
	plan = append(plan, step)

	return &Result{
		Columns: cols,
		Rows:    projected,
		Message: fmt.Sprintf("%d fila(s)", len(projected)),
		Plan:    plan,
	}, nil
}

// project aplica DISTINCT y la lista de expresiones del SELECT sobre los
// grupos. Cada elemento puede ser una columna simple (proyección directa) o
// cualquier expresión; si hay al menos una expresión, todas se evalúan grupo a
// grupo con el evaluador de agregados.
func (e *Engine) project(tbl *Table, groups []rowGroup, n *ast.Select) ([]string, [][]any, Step, error) {
	ec := &evalContext{table: tbl}

	// SELECT *
	if n.All || len(n.Selected) == 0 {
		cols := append([]string(nil), tbl.Schema.Columns...)
		out := make([][]any, 0, len(groups))
		for _, g := range groups {
			out = append(out, cloneAny(g.representative()))
		}
		if n.Distinct {
			cols, out = distinct(cols, out)
		}
		return cols, out, Step{Kind: StepProject, Detail: "proyección de todas las columnas", Rows: len(out)}, nil
	}

	// Primera pasada: cada elemento se resuelve a columna o a expresión.
	type item struct {
		expr ast.ASTNode
		col  int // -1 si es expresión
		name string
	}
	items := make([]item, 0, len(n.Selected))
	allColumns := true
	for _, sel := range n.Selected {
		expr, alias := unwrapSelectItem(sel)
		id, isCol := expr.(*ast.IdExpr)
		if !isCol {
			if name, ok := expr.(*ast.NameExpr); ok {
				id = &ast.IdExpr{Name: name.Name, Table: name.Table}
				isCol = true
			}
		}
		if isCol {
			if _, pos, err := ec.resolve(id.Name, id.Table); err == nil {
				name := tbl.Schema.Columns[pos]
				if alias != nil {
					name = *alias
				}
				items = append(items, item{expr: expr, col: pos, name: name})
				continue
			}
		}
		allColumns = false
		name := exprLabel(expr)
		if alias != nil {
			name = *alias
		}
		items = append(items, item{expr: expr, col: -1, name: name})
	}

	// Segunda pasada: proyección directa si todo son columnas.
	if allColumns {
		cols := make([]string, 0, len(items))
		picks := make([]int, 0, len(items))
		for _, it := range items {
			cols = append(cols, it.name)
			picks = append(picks, it.col)
		}
		out := make([][]any, 0, len(groups))
		for _, g := range groups {
			rep := g.representative()
			line := make([]any, 0, len(picks))
			for _, p := range picks {
				if p >= 0 && p < len(rep) {
					line = append(line, rep[p])
				} else {
					line = append(line, nil)
				}
			}
			out = append(out, line)
		}
		if n.Distinct {
			cols, out = distinct(cols, out)
		}
		return cols, out, Step{
			Kind:   StepProject,
			Detail: fmt.Sprintf("proyección de %d columna(s)", len(cols)),
			Rows:   len(out),
		}, nil
	}

	cols := make([]string, 0, len(items))
	for _, it := range items {
		cols = append(cols, it.name)
	}
	out := make([][]any, 0, len(groups))
	for _, g := range groups {
		line := make([]any, 0, len(items))
		for _, it := range items {
			v, err := ec.evalGroup(it.expr, g)
			if err != nil {
				return nil, nil, Step{}, err
			}
			line = append(line, v)
		}
		out = append(out, line)
	}
	if n.Distinct {
		cols, out = distinct(cols, out)
	}
	return cols, out, Step{
		Kind:   StepProject,
		Detail: fmt.Sprintf("proyección evaluada de %d expresión(es)", len(cols)),
		Rows:   len(out),
	}, nil
}

// selectHasAggregate indica si la lista de proyección usa alguna función de
// agregado, que obliga a tratar el resultado como un único grupo.
func selectHasAggregate(n *ast.Select) bool {
	for _, sel := range n.Selected {
		expr, _ := unwrapSelectItem(sel)
		if exprHasAggregate(expr) {
			return true
		}
	}
	return n.OrderBy != nil && exprHasAggregate(n.OrderBy.Expr)
}

func exprHasAggregate(node ast.ASTNode) bool {
	switch n := node.(type) {
	case *ast.FuncCallExpr:
		if isAggregate(n.Name) {
			return true
		}
		for _, a := range n.Args {
			if exprHasAggregate(a) {
				return true
			}
		}
	case *ast.BinaryExpr:
		return exprHasAggregate(n.Left) || exprHasAggregate(n.Right)
	case *ast.AliasedExpr:
		return exprHasAggregate(n.Expr)
	case *ast.WhereExpr:
		return exprHasAggregate(&n.Content)
	}
	return false
}

// unwrapSelectItem separa la expresión del alias de un elemento del SELECT.
func unwrapSelectItem(node ast.ASTNode) (ast.ASTNode, *string) {
	if a, ok := node.(*ast.AliasedExpr); ok {
		return a.Expr, a.Alias
	}
	return node, nil
}

// exprLabel derives el nombre de columna de una expresión proyectada.
func exprLabel(node ast.ASTNode) string {
	switch n := node.(type) {
	case *ast.IdExpr:
		return n.Name
	case *ast.NameExpr:
		return n.Name
	case *ast.FuncCallExpr:
		return n.Name
	case *ast.StringExpr:
		return "'" + n.Value + "'"
	case *ast.IntExpr:
		return fmt.Sprint(n.Value)
	case *ast.FloatExpr:
		return fmt.Sprint(n.Value)
	case *ast.BoolExpr:
		return fmt.Sprint(n.Value)
	case *ast.NilExpr:
		return "NULL"
	}
	return "expr"
}

func cloneAny(t storage.Tuple) []any {
	out := make([]any, len(t))
	copy(out, t)
	return out
}

func distinct(cols []string, rows [][]any) ([]string, [][]any) {
	seen := make(map[string]bool, len(rows))
	out := make([][]any, 0, len(rows))
	for _, r := range rows {
		k := rowKey(r)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, r)
	}
	return cols, out
}

func rowKey(r []any) string {
	var b strings.Builder
	for _, v := range r {
		fmt.Fprintf(&b, "%v\x00", v)
	}
	return b.String()
}

// orderBy ordena los grupos con external sorting (runs + k-way merge).
func (e *Engine) orderBy(tbl *Table, groups []rowGroup, o *ast.OrderBy) ([]rowGroup, Step, error) {
	ec := &evalContext{table: tbl}
	keyOf := func(g rowGroup) (any, error) {
		return ec.evalGroup(o.Expr, g)
	}

	sorted, runs, err := sorting.ExternalSort(groups, keyOf, o.Descendent, e.opt.SortBufferSlots)
	if err != nil {
		return nil, Step{}, err
	}
	detail := fmt.Sprintf("external sort por la clave de ORDER BY (buffer=%d slots)", e.opt.SortBufferSlots)
	if runs > 1 {
		detail = fmt.Sprintf("external sort: %d runs y k-way merge (buffer=%d slots)", runs, e.opt.SortBufferSlots)
	} else {
		detail = fmt.Sprintf("external sort en memoria: 1 run cabe en el buffer de %d slots", e.opt.SortBufferSlots)
	}
	return sorted, Step{
		Kind:   StepExternalSort,
		Detail: detail,
		Rows:   len(sorted),
	}, nil
}

// applyLimit recorta el resultado según LIMIT y OFFSET.
func (e *Engine) applyLimit(tbl *Table, groups []rowGroup, l *ast.Limit) ([]rowGroup, Step, error) {
	ec := &evalContext{table: tbl}
	out := groups
	if l.Offset != nil {
		ec.row = nil
		v, err := ec.eval(l.Offset)
		if err != nil {
			return nil, Step{}, err
		}
		off, ok := asInt64(v)
		if !ok || off < 0 {
			return nil, Step{}, fmt.Errorf("sql: OFFSET inválido")
		}
		if int(off) >= len(out) {
			out = nil
		} else {
			out = out[off:]
		}
	}
	if l.Count != nil {
		ec.row = nil
		v, err := ec.eval(l.Count)
		if err != nil {
			return nil, Step{}, err
		}
		c, ok := asInt64(v)
		if !ok || c < 0 {
			return nil, Step{}, fmt.Errorf("sql: LIMIT inválido")
		}
		if int(c) < len(out) {
			out = out[:c]
		}
	}
	return out, Step{
		Kind:   StepLimit,
		Detail: fmt.Sprintf("limit aplicado: %d fila(s)", len(out)),
		Rows:   len(out),
	}, nil
}

func (e *Engine) requireTable(name string) (*Table, error) {
	if name == "" {
		return nil, fmt.Errorf("sql: falta el nombre de la tabla")
	}
	tbl, ok := e.cat.get(name)
	if !ok {
		return nil, fmt.Errorf("sql: la tabla %s no existe", name)
	}
	return tbl, nil
}
