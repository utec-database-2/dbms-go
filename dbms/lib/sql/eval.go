package sql

import (
	"fmt"
	"github.com/dbms-go/v2/dbms/lib/external/hashing"
	"sort"
	"strings"

	"github.com/dbms-go/v2/dbms/lib/dsl/ast"
	"github.com/dbms-go/v2/dbms/lib/storage"
)

// compareAny ordena dos valores del mismo tipo SQL. Devuelve -1, 0 o 1.
// Si los tipos no coinciden se comparan por su representación textual para que
// ORDER BY nunca falle, aunque el orden no sea el semántico del tipo.
func compareAny(a, b any) int {
	if a == nil || b == nil {
		switch {
		case a == nil && b == nil:
			return 0
		case a == nil:
			return -1
		default:
			return 1
		}
	}

	switch x := a.(type) {
	case int32:
		if y, ok := asInt64(b); ok {
			return cmpInt64(int64(x), y)
		}
		if y, ok := asFloat64(b); ok {
			return cmpFloat(float64(x), y)
		}
	case int64:
		if y, ok := asInt64(b); ok {
			return cmpInt64(x, y)
		}
		if y, ok := asFloat64(b); ok {
			return cmpFloat(float64(x), y)
		}
	case int:
		if y, ok := asInt64(b); ok {
			return cmpInt64(int64(x), y)
		}
		if y, ok := asFloat64(b); ok {
			return cmpFloat(float64(x), y)
		}
	case float32:
		if y, ok := toFloat64(b); ok {
			return cmpFloat(float64(x), y)
		}
	case float64:
		if y, ok := toFloat64(b); ok {
			return cmpFloat(x, y)
		}
	case bool:
		if y, ok := b.(bool); ok {
			switch {
			case x == y:
				return 0
			case !x:
				return -1
			default:
				return 1
			}
		}
	case string:
		if y, ok := b.(string); ok {
			return strings.Compare(x, y)
		}
	case []byte:
		if y, ok := b.([]byte); ok {
			return strings.Compare(string(x), string(y))
		}
	}
	return strings.Compare(fmt.Sprint(a), fmt.Sprint(b))
}

func cmpInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func asInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int32:
		return int64(x), true
	case int64:
		return x, true
	case int:
		return int64(x), true
	}
	return 0, false
}

// toFloat64 convierte cualquier número (entero o flotante) a float64. Se usa
// cuando se mezclan tipos: 400.3 < 3000 tiene que compararse como números.
func toFloat64(v any) (float64, bool) {
	if f, ok := asFloat64(v); ok {
		return f, true
	}
	if i, ok := asInt64(v); ok {
		return float64(i), true
	}
	return 0, false
}

func asFloat64(v any) (float64, bool) {
	switch x := v.(type) {
	case float32:
		return float64(x), true
	case float64:
		return x, true
	}
	return 0, false
}

// evalContext resuelve nombres de columna durante la evaluación de una expresión.
// table es la tabla base; row es la fila actual. Si expr trae qualifier
// ("t"."col") se verifica que coincida con el alias o nombre de la tabla.
type evalContext struct {
	table *Table
	row   storage.Tuple
}

// resolve busca una columna por nombre, respetando el qualifier opcional.
// Devuelve el nombre canónico de la columna y su posición en la tupla.
func (ec *evalContext) resolve(name string, qualifier *string) (string, int, error) {
	if ec.table == nil {
		return "", 0, fmt.Errorf("sql: no hay tabla para resolver la columna %s", name)
	}
	if ec.table.joinCols != nil {
		pos, err := ec.table.resolveJoined(name, qualifier)
		if err != nil {
			return "", 0, err
		}
		return ec.table.Schema.Columns[pos], pos, nil
	}
	if qualifier != nil && !strings.EqualFold(*qualifier, ec.table.Name()) {
		return "", 0, fmt.Errorf("sql: %s no pertenece a %s", name, *qualifier)
	}
	pos, ok := ec.table.ColumnIndex(name)
	if !ok {
		return "", 0, fmt.Errorf("sql: la columna %s no existe en %s", name, ec.table.Name())
	}
	return ec.table.Schema.Columns[pos], pos, nil
}

// eval evalúa una expresión del AST contra la fila actual.
func (ec *evalContext) eval(node ast.ASTNode) (any, error) {
	switch n := node.(type) {
	case *ast.IdExpr:
		_, pos, err := ec.resolve(n.Name, n.Table)
		if err != nil {
			return nil, err
		}
		if pos >= len(ec.row) {
			return nil, fmt.Errorf("sql: la fila tiene %d columnas y se pidió la %d", len(ec.row), pos)
		}
		return ec.row[pos], nil

	case *ast.StringExpr:
		return n.Value, nil

	case *ast.IntExpr:
		return int64(n.Value), nil

	case *ast.FloatExpr:
		return float64(n.Value), nil

	case *ast.BoolExpr:
		return n.Value, nil

	case *ast.NilExpr:
		return nil, nil

	case *ast.BinaryExpr:
		return ec.evalBinary(n)

	case *ast.WhereExpr:
		// El WHERE envuelve la condición completa: evaluar solo el lado izquierdo
		// perdería las conjunciones (id >= 2 AND id <= 3).
		return ec.eval(&n.Content)

	case *ast.FuncCallExpr:
		if v, ok, err := ec.evalSpatialFunc(n); ok {
			return v, err
		}
		return nil, fmt.Errorf("sql: la función %s todavía no está implementada", n.Name)

	default:
		return nil, fmt.Errorf("sql: no se puede evaluar el nodo %T", node)
	}
}

func (ec *evalContext) evalBinary(n *ast.BinaryExpr) (any, error) {
	switch n.Op {
	case ast.OpAnd:
		l, err := ec.eval(n.Left)
		if err != nil {
			return nil, err
		}
		if !truthy(l) {
			return false, nil
		}
		r, err := ec.eval(n.Right)
		if err != nil {
			return nil, err
		}
		return truthy(r), nil

	case ast.OpOr:
		l, err := ec.eval(n.Left)
		if err != nil {
			return nil, err
		}
		if truthy(l) {
			return true, nil
		}
		r, err := ec.eval(n.Right)
		if err != nil {
			return nil, err
		}
		return truthy(r), nil
	}

	l, err := ec.eval(n.Left)
	if err != nil {
		return nil, err
	}
	r, err := ec.eval(n.Right)
	if err != nil {
		return nil, err
	}
	return applyOp(n.Op, l, r)
}

func applyOp(op ast.Operator, l, r any) (any, error) {
	switch op {
	case ast.OpEq:
		return compareAny(l, r) == 0, nil
	case ast.OpNeq:
		return compareAny(l, r) != 0, nil
	case ast.OpLt:
		return compareAny(l, r) < 0, nil
	case ast.OpLte:
		return compareAny(l, r) <= 0, nil
	case ast.OpGt:
		return compareAny(l, r) > 0, nil
	case ast.OpGte:
		return compareAny(l, r) >= 0, nil
	case ast.OpPlus:
		return arith(l, r, func(a, b float64) float64 { return a + b },
			func(a, b int64) int64 { return a + b })
	case ast.OpSub:
		return arith(l, r, func(a, b float64) float64 { return a - b },
			func(a, b int64) int64 { return a - b })
	case ast.OpMul:
		return arith(l, r, func(a, b float64) float64 { return a * b },
			func(a, b int64) int64 { return a * b })
	case ast.OpDiv:
		return arith(l, r, func(a, b float64) float64 {
			if b == 0 {
				return 0
			}
			return a / b
		}, func(a, b int64) int64 {
			if b == 0 {
				return 0
			}
			return a / b
		})
	}
	return nil, fmt.Errorf("sql: operador no soportado %d", op)
}

func arith(l, r any, ff func(a, b float64) float64, fi func(a, b int64) int64) (any, error) {
	if a, ok1 := asInt64(l); ok1 {
		if b, ok2 := asInt64(r); ok2 {
			return fi(a, b), nil
		}
	}
	a, ok1 := toFloat64(l)
	b, ok2 := toFloat64(r)
	if !ok1 || !ok2 {
		return nil, fmt.Errorf("sql: no se pueden operar %T y %T", l, r)
	}
	return ff(a, b), nil
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case []byte:
		return len(x) > 0
	}
	if i, ok := asInt64(v); ok {
		return i != 0
	}
	return true
}

// filterRows aplica el WHERE sobre un conjunto de filas.
func (ec *evalContext) filterRows(rows []storage.Tuple, cond ast.ASTNode) ([]storage.Tuple, error) {
	if cond == nil {
		return rows, nil
	}
	out := make([]storage.Tuple, 0, len(rows))
	for _, row := range rows {
		ec.row = row
		v, err := ec.eval(cond)
		if err != nil {
			return nil, err
		}
		if truthy(v) {
			out = append(out, row)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Agregados y evaluación por grupo
// ---------------------------------------------------------------------------

// rowGroup son las filas que pertenecen a una misma clave de GROUP BY. Una
// consulta sin GROUP BY produce grupos de una sola fila.
type rowGroup struct {
	rows []storage.Tuple
}

// representative devuelve la fila que representa al grupo: la primera, que es la
// que se usa para leer las columnas no agregadas.
func (g rowGroup) representative() storage.Tuple {
	if len(g.rows) == 0 {
		return nil
	}
	return g.rows[0]
}

// isAggregate indica si una llamada es a una función de agregado.
func isAggregate(name string) bool {
	switch strings.ToUpper(name) {
	case "COUNT", "SUM", "AVG", "MIN", "MAX":
		return true
	}
	return false
}

// evalGroup evalúa una expresión sobre un grupo completo. Las funciones de
// agregado recorren todas sus filas y el resto se evalúa sobre la fila
// representativa.
func (ec *evalContext) evalGroup(node ast.ASTNode, g rowGroup) (any, error) {
	switch n := node.(type) {
	case *ast.FuncCallExpr:
		if !isAggregate(n.Name) {
			// Función escalar (distancia, POINT, ...): se evalúa sobre la
			// fila representativa del grupo.
			ec.row = g.representative()
			return ec.eval(n)
		}
		return ec.aggregate(n, g)

	case *ast.BinaryExpr:
		switch n.Op {
		case ast.OpAnd, ast.OpOr:
			l, err := ec.evalGroup(n.Left, g)
			if err != nil {
				return nil, err
			}
			if n.Op == ast.OpAnd && !truthy(l) {
				return false, nil
			}
			if n.Op == ast.OpOr && truthy(l) {
				return true, nil
			}
			r, err := ec.evalGroup(n.Right, g)
			if err != nil {
				return nil, err
			}
			if n.Op == ast.OpAnd {
				return truthy(r), nil
			}
			return truthy(l) || truthy(r), nil
		}
		l, err := ec.evalGroup(n.Left, g)
		if err != nil {
			return nil, err
		}
		r, err := ec.evalGroup(n.Right, g)
		if err != nil {
			return nil, err
		}
		return applyOp(n.Op, l, r)

	case *ast.WhereExpr:
		return ec.evalGroup(&n.Content, g)
	}

	ec.row = g.representative()
	return ec.eval(node)
}

// aggregate calcula COUNT, SUM, AVG, MIN o MAX sobre las filas del grupo.
func (ec *evalContext) aggregate(fn *ast.FuncCallExpr, g rowGroup) (any, error) {
	name := strings.ToUpper(fn.Name)
	if len(fn.Args) != 1 {
		return nil, fmt.Errorf("sql: %s espera exactamente un argumento", name)
	}

	// COUNT(*) no evalúa la expresión: cuenta filas del grupo.
	if _, isStar := fn.Args[0].(*ast.StarExpr); isStar {
		if name != "COUNT" {
			return nil, fmt.Errorf("sql: %s no admite *", name)
		}
		return int64(len(g.rows)), nil
	}

	values := make([]any, 0, len(g.rows))
	for _, r := range g.rows {
		ec.row = r
		v, err := ec.eval(fn.Args[0])
		if err != nil {
			return nil, err
		}
		if v == nil {
			continue // NULL no participa en los agregados
		}
		values = append(values, v)
	}

	switch name {
	case "COUNT":
		return int64(len(values)), nil
	case "SUM":
		return sumValues(values)
	case "AVG":
		if len(values) == 0 {
			return nil, nil
		}
		sum, err := sumValues(values)
		if err != nil {
			return nil, err
		}
		s, _ := asFloat64(sum)
		if iv, ok := asInt64(sum); ok {
			return float64(iv) / float64(len(values)), nil
		}
		return s / float64(len(values)), nil
	case "MIN", "MAX":
		if len(values) == 0 {
			return nil, nil
		}
		best := values[0]
		for _, v := range values[1:] {
			c := compareAny(v, best)
			if (name == "MIN" && c < 0) || (name == "MAX" && c > 0) {
				best = v
			}
		}
		return best, nil
	}
	return nil, fmt.Errorf("sql: agregado no soportado %s", name)
}

// sumValues suma conservando el tipo: entero si todos son enteros.
func sumValues(values []any) (any, error) {
	if len(values) == 0 {
		return nil, nil
	}
	allInt := true
	for _, v := range values {
		if _, ok := asInt64(v); !ok {
			allInt = false
			break
		}
	}
	if allInt {
		var total int64
		for _, v := range values {
			i, _ := asInt64(v)
			total += i
		}
		return total, nil
	}
	var total float64
	for _, v := range values {
		f, ok := toFloat64(v)
		if !ok {
			return nil, fmt.Errorf("sql: no se puede sumar %T", v)
		}
		total += f
	}
	return total, nil
}

// groupRows agrupa las filas por los valores de las expresiones de GROUP BY con
// external hashing: las filas se reparten en particiones por el hash de su
// clave de grupo (volcándolas a disco si no caben en el buffer) y cada
// partición se agrupa con una tabla hash en memoria. Los grupos salen en el
// orden en que aparece su primera fila.
func (e *Engine) groupRows(tbl *Table, rows []storage.Tuple, exprs []ast.ASTNode) ([]rowGroup, []Step, error) {
	ec := &evalContext{table: tbl}
	type keyed struct {
		key string
		pos int
		row storage.Tuple
	}
	items := make([]keyed, 0, len(rows))
	for i, r := range rows {
		ec.row = r
		var b strings.Builder
		for _, x := range exprs {
			v, err := ec.eval(x)
			if err != nil {
				return nil, nil, err
			}
			fmt.Fprintf(&b, "%v\x00", v)
		}
		items = append(items, keyed{key: b.String(), pos: i, row: r})
	}

	parts, err := hashing.Partition(items,
		func(k keyed) ([]byte, error) { return []byte(k.key), nil },
		func(k keyed) ([]byte, error) {
			return storage.EncodeRow(append(storage.Tuple{k.key, int64(k.pos)}, k.row...))
		},
		func(buf []byte) (keyed, error) {
			t, err := storage.DecodeRowBytes(buf)
			if err != nil || len(t) < 2 {
				return keyed{}, fmt.Errorf("sql: fila de partición ilegible: %v", err)
			}
			key, _ := t[0].(string)
			pos, _ := asInt64(t[1])
			return keyed{key: key, pos: int(pos), row: t[2:]}, nil
		},
		hashing.Options{
			Partitions:  e.hashPartitions(len(rows)),
			BufferSlots: e.opt.SortBufferSlots,
			Dir:         e.dbDir(),
			Prefix:      "group",
			NoSpill:     e.opt.DisableSpill,
		})
	if err != nil {
		return nil, nil, err
	}
	defer parts.Close()

	type group struct {
		first int
		rows  []storage.Tuple
	}
	var groups []group
	for i := 0; i < parts.Len(); i++ {
		part, err := parts.Read(i)
		if err != nil {
			return nil, nil, err
		}
		byKey := make(map[string]int)
		for _, it := range part {
			gi, ok := byKey[it.key]
			if !ok {
				gi = len(groups)
				byKey[it.key] = gi
				groups = append(groups, group{first: it.pos})
			}
			groups[gi].rows = append(groups[gi].rows, it.row)
		}
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].first < groups[j].first })

	out := make([]rowGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, rowGroup{rows: g.rows})
	}
	where := "en memoria"
	if parts.Spilled {
		where = fmt.Sprintf("volcadas a disco (%d B escritos, %d B leídos)", parts.BytesWritten, parts.BytesRead)
	}
	return out, []Step{{
		Kind: StepGroupBy,
		Detail: fmt.Sprintf("group by con external hashing sobre %d columna(s): %d particiones %s, %d grupo(s)",
			len(exprs), parts.Len(), where, len(out)),
		Rows: len(out),
	}}, nil
}

// hashPartitions elige cuántas particiones usar: las justas para que cada una
// quepa en el buffer, con un mínimo de 4 para que el reparto se vea en el plan.
func (e *Engine) hashPartitions(rows int) int {
	n := 4
	if e.opt.SortBufferSlots > 0 {
		if need := rows/e.opt.SortBufferSlots + 1; need > n {
			n = need
		}
	}
	if n > 256 {
		n = 256
	}
	return n
}

// keepGroups aplica el HAVING sobre los grupos.
func (ec *evalContext) keepGroups(groups []rowGroup, cond ast.ASTNode) ([]rowGroup, error) {
	out := make([]rowGroup, 0, len(groups))
	for _, g := range groups {
		v, err := ec.evalGroup(cond, g)
		if err != nil {
			return nil, err
		}
		if truthy(v) {
			out = append(out, g)
		}
	}
	return out, nil
}
