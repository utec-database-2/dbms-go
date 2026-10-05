package sql

import (
	"fmt"
	"strings"

	"github.com/dbms-go/v2/dbms/lib/dsl/ast"
	"github.com/dbms-go/v2/dbms/lib/external/hashing"
	"github.com/dbms-go/v2/dbms/lib/storage"
)

// JOIN.
//
// El resultado de un join es una tabla virtual cuyas columnas son las de cada
// tabla, en orden, calificadas como alias.columna. El resto del pipeline del
// SELECT (WHERE, GROUP BY, ORDER BY, LIMIT, proyección) trabaja sobre esa tabla
// virtual igual que sobre una tabla real.
//
// Para cada JOIN el motor elige la estrategia:
//
//   - Index nested loop: si la tabla de la derecha tiene un índice (B+ o hash)
//     sobre su columna de unión, por cada fila de la izquierda se busca en el
//     índice. Es el "uso estratégico de índices".
//   - Grace hash join: si no hay índice, ambos lados se reparten en
//     particiones con external hashing (volcadas a disco si no caben en el
//     buffer) y cada partición se une con una tabla hash en memoria.
//   - Nested loop: si la condición no es una igualdad entre columnas (o es un
//     CROSS JOIN), se prueba cada par de filas.

// joinCol describe una columna de la tabla virtual de un join.
type joinCol struct {
	table string // nombre real de la tabla
	alias string // alias usado en la consulta (o el nombre)
	name  string // nombre de la columna
}

// resolveJoined busca una columna en una tabla virtual de join.
func (t *Table) resolveJoined(name string, qualifier *string) (int, error) {
	found := -1
	for i, c := range t.joinCols {
		if !strings.EqualFold(c.name, name) {
			continue
		}
		if qualifier != nil && !strings.EqualFold(*qualifier, c.alias) && !strings.EqualFold(*qualifier, c.table) {
			continue
		}
		if found >= 0 {
			return 0, fmt.Errorf("sql: la columna %s es ambigua; califícala con la tabla (t.%s)", name, name)
		}
		found = i
	}
	if found < 0 {
		if qualifier != nil {
			return 0, fmt.Errorf("sql: la columna %s.%s no existe en el join", *qualifier, name)
		}
		return 0, fmt.Errorf("sql: la columna %s no existe en el join", name)
	}
	return found, nil
}

// joinSource es una tabla que participa en el join con su alias.
type joinSource struct {
	tbl   *Table
	alias string
}

func sourceOf(e *Engine, te ast.TableExpr) (joinSource, error) {
	tbl, err := e.requireTable(te.Name)
	if err != nil {
		return joinSource{}, err
	}
	alias := tbl.Name()
	if te.Alias != nil && *te.Alias != "" {
		alias = *te.Alias
	}
	return joinSource{tbl: tbl, alias: alias}, nil
}

// virtualTable arma la tabla virtual con las columnas de las fuentes.
func virtualTable(sources []joinSource) *Table {
	vt := &Table{indexes: map[string]Index{}}
	names := make([]string, 0, len(sources))
	for _, s := range sources {
		names = append(names, s.alias)
		for i, c := range s.tbl.Schema.Columns {
			vt.Schema.Columns = append(vt.Schema.Columns, s.alias+"."+c)
			vt.Schema.Types = append(vt.Schema.Types, s.tbl.Schema.Types[i])
			vt.joinCols = append(vt.joinCols, joinCol{table: s.tbl.Name(), alias: s.alias, name: c})
		}
	}
	vt.Schema.Name = strings.Join(names, "⋈")
	return vt
}

// execJoin resuelve el FROM ... JOIN ... y devuelve la tabla virtual, sus filas
// y los pasos del plan.
func (e *Engine) execJoin(n *ast.Select) (*Table, []storage.Tuple, []Step, error) {
	base, err := sourceOf(e, n.From)
	if err != nil {
		return nil, nil, nil, err
	}
	sources := []joinSource{base}
	for _, j := range n.Join {
		src, err := sourceOf(e, j.Table)
		if err != nil {
			return nil, nil, nil, err
		}
		sources = append(sources, src)
	}
	for i := range sources {
		for k := 0; k < i; k++ {
			if strings.EqualFold(sources[i].alias, sources[k].alias) {
				return nil, nil, nil, fmt.Errorf("sql: la tabla %s aparece dos veces en el join; usa alias distintos", sources[i].alias)
			}
		}
	}

	// Las condiciones del WHERE que solo tocan la tabla base se empujan a su
	// acceso, para que el planner pueda usar sus índices antes del join.
	pushed := pushdownWhere(n.Closure, base, sources[1:])
	rows, plan, err := base.tbl.fetch(pushed)
	if err != nil {
		return nil, nil, nil, err
	}
	for i := range plan {
		plan[i].Detail = base.alias + ": " + plan[i].Detail
	}

	left := sources[:1]
	for i, j := range n.Join {
		right := sources[i+1]
		var step Step
		rows, step, err = e.joinStep(left, right, rows, j)
		if err != nil {
			return nil, nil, nil, err
		}
		plan = append(plan, step)
		left = sources[:i+2]
	}

	vt := virtualTable(sources)
	if n.Closure != nil {
		ec := &evalContext{table: vt}
		before := len(rows)
		rows, err = ec.filterRows(rows, n.Closure)
		if err != nil {
			return nil, nil, nil, err
		}
		plan = append(plan, Step{
			Kind:   StepFilter,
			Detail: fmt.Sprintf("filtro WHERE sobre el join: %d de %d fila(s)", len(rows), before),
			Rows:   len(rows),
			Cost:   before,
		})
	}
	return vt, rows, plan, nil
}

// joinStep une las filas acumuladas (left) con la tabla right.
func (e *Engine) joinStep(left []joinSource, right joinSource, rows []storage.Tuple, j ast.JoinExpr) ([]storage.Tuple, Step, error) {
	kind := strings.ToUpper(j.Type)
	switch kind {
	case "", "INNER", "LEFT", "CROSS":
	default:
		return nil, Step{}, fmt.Errorf("sql: %s JOIN no está soportado (usa INNER o LEFT)", kind)
	}
	if kind == "" {
		kind = "INNER"
	}
	outer := kind == "LEFT"

	lt := virtualTable(left)
	all := virtualTable(append(append([]joinSource(nil), left...), right))
	width := len(lt.Schema.Columns)
	on := j.On
	if len(j.Using) > 0 {
		on = usingCondition(j.Using, left, right)
	}

	// Claves de igualdad entre una columna de la izquierda y una de la derecha.
	lpos, rpos, ok := equiKeys(on, lt, right)
	nulls := make(storage.Tuple, len(right.tbl.Schema.Columns))
	ec := &evalContext{table: all}
	emit := func(out []storage.Tuple, l storage.Tuple, matches []storage.Tuple) ([]storage.Tuple, error) {
		hit := false
		for _, r := range matches {
			row := append(append(make(storage.Tuple, 0, width+len(r)), l...), r...)
			if on != nil {
				ec.row = row
				v, err := ec.eval(on)
				if err != nil {
					return nil, err
				}
				if !truthy(v) {
					continue
				}
			}
			hit = true
			out = append(out, row)
		}
		if !hit && outer {
			out = append(out, append(append(make(storage.Tuple, 0, width+len(nulls)), l...), nulls...))
		}
		return out, nil
	}

	label := fmt.Sprintf("%s JOIN %s", kind, right.alias)
	if !ok || kind == "CROSS" {
		rrows, err := right.tbl.scanTable()
		if err != nil {
			return nil, Step{}, err
		}
		var out []storage.Tuple
		for _, l := range rows {
			if out, err = emit(out, l, rrows); err != nil {
				return nil, Step{}, err
			}
		}
		return out, Step{
			Kind:   StepJoin,
			Detail: fmt.Sprintf("%s por nested loop: %d × %d pares evaluados", label, len(rows), len(rrows)),
			Rows:   len(out),
			Cost:   len(rows) * len(rrows),
		}, nil
	}

	rcol := right.tbl.Schema.Columns[rpos]
	// Index nested loop: la derecha tiene un índice de búsqueda exacta.
	if idx, has := right.tbl.IndexOnColumn(rpos); has {
		if _, isRTree := idx.(*rtreeIndex); !isRTree {
			var out []storage.Tuple
			lookups := 0
			for _, l := range rows {
				key := l[lpos]
				if key == nil {
					if outer {
						out = append(out, append(append(make(storage.Tuple, 0, width+len(nulls)), l...), nulls...))
					}
					continue
				}
				lookups++
				matches, err := idx.SearchExact(coerceToColumn(key, right.tbl.Schema.Types[rpos]))
				if err != nil {
					return nil, Step{}, err
				}
				if out, err = emit(out, l, matches); err != nil {
					return nil, Step{}, err
				}
			}
			return out, Step{
				Kind: StepJoin,
				Detail: fmt.Sprintf("%s por index nested loop: %d búsqueda(s) en el índice %s (%s) sobre %s.%s",
					label, lookups, idx.Name(), idx.Kind(), right.alias, rcol),
				Rows: len(out),
				Cost: lookups + len(out),
			}, nil
		}
	}

	// Grace hash join con external hashing.
	rrows, err := right.tbl.scanTable()
	if err != nil {
		return nil, Step{}, err
	}
	parts := e.hashPartitions(len(rows) + len(rrows))
	opt := hashing.Options{
		Partitions:  parts,
		BufferSlots: e.opt.SortBufferSlots,
		Dir:         e.dbDir(),
		Prefix:      "join",
		NoSpill:     e.opt.DisableSpill,
	}
	keyAt := func(pos int) func(storage.Tuple) ([]byte, error) {
		return func(t storage.Tuple) ([]byte, error) { return []byte(joinKey(t[pos])), nil }
	}
	lparts, err := hashing.Partition(rows, keyAt(lpos), storage.EncodeRow, storage.DecodeRowBytes, opt)
	if err != nil {
		return nil, Step{}, err
	}
	defer lparts.Close()
	rparts, err := hashing.Partition(rrows, keyAt(rpos), storage.EncodeRow, storage.DecodeRowBytes, opt)
	if err != nil {
		return nil, Step{}, err
	}
	defer rparts.Close()

	var out []storage.Tuple
	for p := 0; p < parts; p++ {
		build, err := rparts.Read(p)
		if err != nil {
			return nil, Step{}, err
		}
		table := make(map[string][]storage.Tuple, len(build))
		for _, r := range build {
			if r[rpos] == nil {
				continue // NULL nunca es igual a nada
			}
			k := joinKey(r[rpos])
			table[k] = append(table[k], r)
		}
		probe, err := lparts.Read(p)
		if err != nil {
			return nil, Step{}, err
		}
		for _, l := range probe {
			var matches []storage.Tuple
			if l[lpos] != nil {
				matches = table[joinKey(l[lpos])]
			}
			if out, err = emit(out, l, matches); err != nil {
				return nil, Step{}, err
			}
		}
	}
	where := "en memoria"
	if lparts.Spilled || rparts.Spilled {
		where = fmt.Sprintf("volcadas a disco (%d B escritos, %d B leídos)",
			lparts.BytesWritten+rparts.BytesWritten, lparts.BytesRead+rparts.BytesRead)
	}
	return out, Step{
		Kind: StepJoin,
		Detail: fmt.Sprintf("%s por hash join (external hashing): %d particiones %s, build sobre %s.%s (%d filas), probe con %d filas",
			label, parts, where, right.alias, rcol, len(rrows), len(rows)),
		Rows: len(out),
		Cost: len(rows) + len(rrows),
	}, nil
}

// joinKey normaliza una clave de unión para que 1 (int32) y 1 (int64) caigan
// juntos.
func joinKey(v any) string {
	if i, ok := asInt64(v); ok {
		return fmt.Sprintf("n:%d", i)
	}
	if f, ok := asFloat64(v); ok {
		if f == float64(int64(f)) {
			return fmt.Sprintf("n:%d", int64(f))
		}
		return fmt.Sprintf("f:%g", f)
	}
	return fmt.Sprintf("%T:%v", v, v)
}

// equiKeys busca en el ON una igualdad columna = columna entre la izquierda y
// la derecha. Devuelve sus posiciones (en la tabla virtual izquierda y en la
// tabla derecha).
func equiKeys(on ast.ASTNode, lt *Table, right joinSource) (int, int, bool) {
	if on == nil {
		return 0, 0, false
	}
	conds, err := flattenAnd(on)
	if err != nil {
		return 0, 0, false
	}
	rt := virtualTable([]joinSource{right})
	for _, c := range conds {
		if c.Op != ast.OpEq {
			continue
		}
		a, ok1 := c.Left.(*ast.IdExpr)
		b, ok2 := c.Right.(*ast.IdExpr)
		if !ok1 || !ok2 {
			continue
		}
		for _, pair := range [][2]*ast.IdExpr{{a, b}, {b, a}} {
			lp, errL := lt.resolveJoined(pair[0].Name, pair[0].Table)
			rp, errR := rt.resolveJoined(pair[1].Name, pair[1].Table)
			if errL == nil && errR == nil {
				// Una columna sin calificar que exista en ambos lados es
				// ambigua: solo vale si la calificación la ubica.
				if pair[1].Table == nil {
					if _, err := lt.resolveJoined(pair[1].Name, nil); err == nil {
						continue
					}
				}
				return lp, rp, true
			}
		}
	}
	return 0, 0, false
}

// usingCondition traduce JOIN ... USING (a, b) a left.a = right.a AND ...
func usingCondition(cols []ast.NameExpr, left []joinSource, right joinSource) ast.ASTNode {
	var cond ast.ASTNode
	for _, c := range cols {
		// La columna de la izquierda es la de la primera tabla que la tiene.
		lalias := left[0].alias
		for _, s := range left {
			if _, ok := s.tbl.ColumnIndex(c.Name); ok {
				lalias = s.alias
				break
			}
		}
		la, ra := lalias, right.alias
		eq := &ast.BinaryExpr{
			Left:  &ast.IdExpr{Name: c.Name, Table: &la},
			Op:    ast.OpEq,
			Right: &ast.IdExpr{Name: c.Name, Table: &ra},
		}
		if cond == nil {
			cond = eq
		} else {
			cond = &ast.BinaryExpr{Left: cond, Op: ast.OpAnd, Right: eq}
		}
	}
	return cond
}

// pushdownWhere devuelve la parte del WHERE que solo usa columnas de la tabla
// base, sin calificar, para que el planner de esa tabla la pueda usar. Con un
// OR en el WHERE no se empuja nada.
func pushdownWhere(where ast.ASTNode, base joinSource, others []joinSource) ast.ASTNode {
	if where == nil {
		return nil
	}
	conds, err := flattenAnd(where)
	if err != nil || len(conds) == 0 {
		return nil
	}
	var out ast.ASTNode
	for _, c := range conds {
		c := c
		stripped, ok := onlyBase(&c, base, others)
		if !ok {
			continue
		}
		if out == nil {
			out = stripped
		} else {
			out = &ast.BinaryExpr{Left: out, Op: ast.OpAnd, Right: stripped}
		}
	}
	return out
}

// onlyBase copia la expresión quitando la calificación de la tabla base. ok es
// false si la expresión usa columnas de otra tabla o algo que no sabe copiar.
func onlyBase(node ast.ASTNode, base joinSource, others []joinSource) (ast.ASTNode, bool) {
	switch n := node.(type) {
	case *ast.IdExpr:
		if n.Table != nil {
			if !strings.EqualFold(*n.Table, base.alias) && !strings.EqualFold(*n.Table, base.tbl.Name()) {
				return nil, false
			}
		} else {
			for _, o := range others {
				if _, ok := o.tbl.ColumnIndex(n.Name); ok {
					return nil, false // ambigua: se deja para después del join
				}
			}
		}
		if _, ok := base.tbl.ColumnIndex(n.Name); !ok {
			return nil, false
		}
		return &ast.IdExpr{Name: n.Name}, true
	case *ast.BinaryExpr:
		l, ok1 := onlyBase(n.Left, base, others)
		r, ok2 := onlyBase(n.Right, base, others)
		if !ok1 || !ok2 {
			return nil, false
		}
		return &ast.BinaryExpr{Left: l, Op: n.Op, Right: r}, true
	case *ast.FuncCallExpr:
		args := make([]ast.ASTNode, len(n.Args))
		for i, a := range n.Args {
			v, ok := onlyBase(a, base, others)
			if !ok {
				return nil, false
			}
			args[i] = v
		}
		return &ast.FuncCallExpr{Name: n.Name, Args: args}, true
	case *ast.IntExpr, *ast.FloatExpr, *ast.StringExpr, *ast.BoolExpr, *ast.NilExpr:
		return n, true
	}
	return nil, false
}
