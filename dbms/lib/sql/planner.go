package sql

import (
	"fmt"
	"math"
	"strings"

	"github.com/dbms-go/v2/dbms/lib/dsl/ast"
	"github.com/dbms-go/v2/dbms/lib/storage"
)

// fetchPlan es la decisión del planner: qué acceso usar para obtener las filas
// que pasan el WHERE.
type fetchPlan struct {
	rows   []storage.Tuple
	step   Step
	steps  []Step
	access string
}

// fetchPlanFor decide entre recorrido secuencial y acceso por índice. El índice
// se elige solo si cubre una comparación de igualdad o un rango sobre una
// columna indexada, porque en ese caso evita leer toda la tabla.
func (t *Table) planAccess(where ast.ASTNode) (*fetchPlan, error) {
	cond, err := flattenAnd(where)
	if err != nil {
		return nil, err
	}

	// Intento 1: usar un índice para acotar el conjunto de filas.
	if len(cond) > 0 {
		if idx, kind, bounds, ok := t.matchIndex(cond); ok {
			rows, err := t.readIndex(idx, kind, bounds)
			if err != nil {
				return nil, err
			}
			detail := fmt.Sprintf("índice %s (%s) sobre %s%s",
				idx.Name(), idx.Kind(), idx.Column(), describeBounds(kind, bounds))
			return &fetchPlan{
				rows:   rows,
				access: "index",
				step: Step{
					Kind:   StepIndexSeek,
					Detail: detail,
					Rows:   len(rows),
					Cost:   len(rows) + idx.Entries()/10,
				},
			}, nil
		}
	}

	// Intento 2: recorrido secuencial + filtro.
	all, err := t.scanTable()
	if err != nil {
		return nil, err
	}
	label := "heap file (orden físico)"
	if t.Clustered {
		label = "secuencial (orden por PK)"
	}
	step := Step{
		Kind:   StepScan,
		Detail: fmt.Sprintf("recorrido secuencial sobre %s: %d fila(s)", label, len(all)),
		Rows:   len(all),
		Cost:   len(all),
	}
	if len(cond) == 0 {
		return &fetchPlan{rows: all, access: "scan", step: step}, nil
	}
	ec := &evalContext{table: t}
	filtered, err := ec.filterRows(all, where)
	if err != nil {
		return nil, err
	}
	filter := Step{
		Kind:   StepFilter,
		Detail: fmt.Sprintf("filtro WHERE: %d de %d fila(s) leídas pasan la condición", len(filtered), len(all)),
		Rows:   len(filtered),
		Cost:   len(all),
	}
	return &fetchPlan{rows: filtered, access: "scan", steps: []Step{step, filter}}, nil
}

// fetch devuelve las filas del WHERE y los pasos del plan correspondientes.
func (t *Table) fetch(where ast.ASTNode) ([]storage.Tuple, []Step, error) {
	plan, err := t.planAccess(where)
	if err != nil {
		return nil, nil, err
	}
	// Si el planner ya filtró (acceso secuencial), no hay que volver a filtrar.
	// Si usó un índice, el WHERE completo se aplica encima porque un índice
	// solo cubre su columna.
	if plan.access == "index" && where != nil {
		ec := &evalContext{table: t}
		rows, err := ec.filterRows(plan.rows, where)
		if err != nil {
			return nil, nil, err
		}
		if len(rows) != len(plan.rows) {
			plan.steps = []Step{plan.step, {
				Kind:   StepFilter,
				Detail: fmt.Sprintf("filtro residual sobre %d fila(s) del índice", len(plan.rows)),
				Rows:   len(rows),
				Cost:   len(plan.rows),
			}}
		} else {
			plan.steps = []Step{plan.step}
		}
		return rows, plan.steps, nil
	}
	if len(plan.steps) > 0 {
		return plan.rows, plan.steps, nil
	}
	return plan.rows, []Step{plan.step}, nil
}

// indexBounds describe el intervalo que se le pide a un índice.
type indexBounds struct {
	eq    any
	hasEq bool
	low   any
	high  any
	// openLow/openHigh marcan extremos abiertos (>, <).
	openLow  bool
	openHigh bool
	hasLow   bool
	hasHigh  bool
}

func describeBounds(kind string, b indexBounds) string {
	switch kind {
	case "eq":
		return fmt.Sprintf(" = %v", b.eq)
	case "range":
		lo := "[" + fmt.Sprint(b.low)
		if b.openLow {
			lo = "(" + fmt.Sprint(b.low)
		}
		hi := fmt.Sprint(b.high) + "]"
		if b.openHigh {
			hi = fmt.Sprint(b.high) + ")"
		}
		return fmt.Sprintf(" en %s..%s", lo, hi)
	case "full":
		return " (todo el índice)"
	}
	return ""
}

// matchIndex busca una condición que un índice pueda resolver.
func (t *Table) matchIndex(conds []ast.BinaryExpr) (Index, string, indexBounds, bool) {
	// Primero igualdad: es la búsqueda más selectiva.
	for _, c := range conds {
		if c.Op != ast.OpEq {
			continue
		}
		col, lit, ok := columnEqualsLiteral(t, c)
		if !ok {
			continue
		}
		if idx, ok := t.IndexOnColumn(col); ok {
			val, err := literalValue(t, lit)
			if err != nil {
				continue
			}
			// La clave se busca ya convertida al tipo de la columna: los índices
			// comparan claves codificadas, no valores soltos.
			return idx, "eq", indexBounds{eq: coerceToColumn(val, t.Schema.Types[col]), hasEq: true}, true
		}
	}

	// luego rango: se combinan todas las comparaciones de la misma columna para
	// acotar el intervalo (id >= 2 AND id <= 3 -> [2, 3]).
	bounds := make(map[int]*indexBounds)
	order := make([]int, 0, len(conds))
	for _, c := range conds {
		var (
			colIdx int
			lit    ast.ASTNode
			open   bool
		)
		switch c.Op {
		case ast.OpLt, ast.OpLte:
			pos, node, ok := columnLessLiteral(t, c)
			if !ok {
				continue
			}
			colIdx, lit, open = pos, node, c.Op == ast.OpLt
		case ast.OpGt, ast.OpGte:
			pos, node, ok := columnGreaterLiteral(t, c)
			if !ok {
				continue
			}
			colIdx, lit, open = pos, node, c.Op == ast.OpGt
		default:
			continue
		}
		idx, ok := t.IndexOnColumn(colIdx)
		if !ok || !idx.SupportsRange() {
			continue
		}
		val, err := literalValue(t, lit)
		if err != nil {
			continue
		}
		val = coerceToColumn(val, t.Schema.Types[colIdx])

		b, ok := bounds[colIdx]
		if !ok {
			b = &indexBounds{low: minOfColumn(t, colIdx), hasLow: true, high: maxOfColumn(t, colIdx), hasHigh: true}
			bounds[colIdx] = b
			order = append(order, colIdx)
		}
		if c.Op == ast.OpLt || c.Op == ast.OpLte {
			// El extremo superior más estricto gana.
			if compareAny(val, b.high) <= 0 {
				b.high, b.hasHigh, b.openHigh = val, true, open
			}
		} else if compareAny(val, b.low) >= 0 {
			b.low, b.hasLow, b.openLow = val, true, open
		}
	}
	for _, colIdx := range order {
		b := bounds[colIdx]
		if compareAny(b.low, b.high) > 0 {
			continue // intervalo vacío: no hay filas que devolver
		}
		idx, _ := t.IndexOnColumn(colIdx)
		return idx, "range", *b, true
	}
	return nil, "", indexBounds{}, false
}

// readIndex ejecuta la lectura por índice combinando los límites disponibles.
func (t *Table) readIndex(idx Index, kind string, b indexBounds) ([]storage.Tuple, error) {
	switch kind {
	case "eq":
		return idx.SearchExact(b.eq)
	case "range":
		return idx.SearchRange(b.low, b.high)
	}
	return t.scanTable()
}

// columnEqualsLiteral detecta "col = literal".
func columnEqualsLiteral(t *Table, c ast.BinaryExpr) (int, ast.ASTNode, bool) {
	if id, ok := c.Left.(*ast.IdExpr); ok {
		if pos, found := t.ColumnIndex(id.Name); found {
			if isLiteral(c.Right) {
				return pos, c.Right, true
			}
		}
	}
	if id, ok := c.Right.(*ast.IdExpr); ok {
		if pos, found := t.ColumnIndex(id.Name); found {
			if isLiteral(c.Left) {
				return pos, c.Left, true
			}
		}
	}
	return 0, nil, false
}

func columnLessLiteral(t *Table, c ast.BinaryExpr) (int, ast.ASTNode, bool) {
	if id, ok := c.Left.(*ast.IdExpr); ok {
		if pos, found := t.ColumnIndex(id.Name); found && isLiteral(c.Right) {
			return pos, c.Right, true
		}
	}
	return 0, nil, false
}

func columnGreaterLiteral(t *Table, c ast.BinaryExpr) (int, ast.ASTNode, bool) {
	if id, ok := c.Left.(*ast.IdExpr); ok {
		if pos, found := t.ColumnIndex(id.Name); found && isLiteral(c.Right) {
			return pos, c.Right, true
		}
	}
	return 0, nil, false
}

func isLiteral(n ast.ASTNode) bool {
	switch n.(type) {
	case *ast.IntExpr, *ast.FloatExpr, *ast.StringExpr, *ast.BoolExpr, *ast.NilExpr:
		return true
	}
	return false
}

// literalValue evalúa un literal del AST al tipo de la columna.
func literalValue(t *Table, n ast.ASTNode) (any, error) {
	ec := &evalContext{table: t}
	v, err := ec.eval(n)
	if err != nil {
		return nil, err
	}
	return v, nil
}

func minOfColumn(t *Table, col int) any {
	switch t.Schema.Types[col] {
	case storage.TypeString:
		return ""
	case storage.TypeBytes:
		return []byte{}
	case storage.TypeBool:
		return false
	case storage.TypeInt32:
		// Un int64 fuera de rango se trunca al convertirlo a int32: el límite
		// tiene que ser el del propio tipo.
		return int32(math.MinInt32)
	default:
		return coerceToColumn(int64(-1)<<62, t.Schema.Types[col])
	}
}

func maxOfColumn(t *Table, col int) any {
	switch t.Schema.Types[col] {
	case storage.TypeString:
		return strings.Repeat("\xff", t.Schema.MaxLen[col])
	case storage.TypeBytes:
		return bytesMax(t.Schema.MaxLen[col])
	case storage.TypeBool:
		return true
	case storage.TypeInt32:
		return int32(math.MaxInt32)
	default:
		return coerceToColumn(int64(1)<<62-1, t.Schema.Types[col])
	}
}

func bytesMax(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = 0xff
	}
	return b
}

// flattenAnd descompone un WHERE en la conjunción de sus términos, para que el
// planner pueda considerar cada comparación por separado.
func flattenAnd(where ast.ASTNode) ([]ast.BinaryExpr, error) {
	if where == nil {
		return nil, nil
	}
	var bin ast.BinaryExpr
	switch n := where.(type) {
	case *ast.BinaryExpr:
		bin = *n
	case *ast.WhereExpr:
		bin = n.Content
	default:
		return nil, fmt.Errorf("sql: WHERE no soportado (%T)", where)
	}

	if bin.Op == ast.OpAnd {
		l, err := flattenAnd(bin.Left)
		if err != nil {
			return nil, err
		}
		r, err := flattenAnd(bin.Right)
		if err != nil {
			return nil, err
		}
		return append(l, r...), nil
	}
	if bin.Op == ast.OpOr {
		// Una disyunción puede traer filas que ningún índice cubre: se deja
		// para el recorrido secuencial.
		return nil, nil
	}
	return []ast.BinaryExpr{bin}, nil
}

// ---------------------------------------------------------------------------
// Operaciones de fila
// ---------------------------------------------------------------------------

// insertRow escribe la fila en el almacenamiento y publica el RID en todos los
// índices. En una tabla agrupada el índice de clave primaria es quien escribe en
// el secuencial, porque es el único que puede resolver la fila por su clave.
func (t *Table) insertRow(row storage.Tuple) (storage.RID, string, error) {
	if err := t.validateTuple(row); err != nil {
		return storage.RID{}, "", err
	}

	if t.Clustered {
		pk, ok := t.IndexOnColumn(t.Schema.KeyCols[0])
		if !ok {
			return storage.RID{}, "", fmt.Errorf("sql: %s no tiene índice agrupado", t.Name())
		}
		if err := pk.Insert(row, storage.RID{}); err != nil {
			return storage.RID{}, "", err
		}
		pkCol := t.Schema.KeyCols[0]
		synthetic := storage.RID{File: -1, Page: -1, Slot: keyToSlot(row[pkCol])}
		for _, name := range t.IndexNames() {
			idx := t.indexes[name]
			if idx == pk {
				continue
			}
			if err := idx.Insert(row, synthetic); err != nil {
				// Reversión: la fila no queda viva sin sus índices.
				_ = pk.DeleteRID(synthetic, row)
				return storage.RID{}, "", err
			}
		}
		return synthetic, pk.Name(), nil
	}

	// No agrupada: el heap asigna el RID y los índices lo referencian.
	rid, err := t.heap.Insert(row)
	if err != nil {
		return storage.RID{}, "", err
	}
	first := ""
	for _, name := range t.IndexNames() {
		idx := t.indexes[name]
		if err := idx.Insert(row, rid); err != nil {
			// Reversión: se desindexa lo ya publicado y se borra la fila.
			for _, prev := range t.IndexNames() {
				if t.indexes[prev] == idx {
					break
				}
				_ = t.indexes[prev].DeleteRID(rid, row)
			}
			_ = t.heap.Delete(rid)
			return storage.RID{}, "", err
		}
		if first == "" {
			first = idx.Name()
		}
	}
	return rid, first, nil
}

// updateRow aplica el cambio en el almacenamiento y reubica las entradas de
// los índices que dependan del valor modificado.
func (t *Table) updateRow(prev, next storage.Tuple) error {
	if err := t.validateTuple(next); err != nil {
		return err
	}
	if t.Clustered {
		pk, ok := t.IndexOnColumn(t.Schema.KeyCols[0])
		if !ok {
			return fmt.Errorf("sql: %s no tiene índice agrupado", t.Name())
		}
		// El secuencial se reordena con la fila nueva y los índices
		// secundarios se republican con la clave que resulta.
		if err := pk.UpdateRID(storage.RID{}, prev, next); err != nil {
			return err
		}
		for _, name := range t.IndexNames() {
			idx := t.indexes[name]
			if idx == pk {
				continue
			}
			if err := idx.UpdateRID(storage.RID{}, prev, next); err != nil {
				return err
			}
		}
		return nil
	}

	rid, err := t.locateRID(prev)
	if err != nil {
		return err
	}
	if err := t.heap.Update(rid, next); err != nil {
		return err
	}
	for _, name := range t.IndexNames() {
		idx := t.indexes[name]
		if err := t.reindexAfterUpdate(idx, rid, prev, next); err != nil {
			return err
		}
	}
	return nil
}

func (t *Table) reindexAfterUpdate(idx Index, rid storage.RID, prev, next storage.Tuple) error {
	switch v := idx.(type) {
	case *unclusteredIndex:
		return v.UpdateRID(rid, prev, next)
	case *hashIndex:
		return v.UpdateRID(rid, prev, next)
	case *clusteredIndex:
		return v.UpdateRID(rid, prev, next)
	}
	return fmt.Errorf("sql: no se puede reindexar %s", idx.Name())
}

// deleteRow borra la fila del almacenamiento y de todos los índices.
func (t *Table) deleteRow(row storage.Tuple) error {
	if t.Clustered {
		// El índice de clave primaria es quien borra del secuencial, así que
		// los índices secundarios solo se despublican.
		pk, ok := t.IndexOnColumn(t.Schema.KeyCols[0])
		if !ok {
			return fmt.Errorf("sql: %s no tiene índice agrupado", t.Name())
		}
		if err := pk.DeleteRID(storage.RID{}, row); err != nil {
			return err
		}
		for _, name := range t.IndexNames() {
			idx := t.indexes[name]
			if idx == pk {
				continue
			}
			if err := idx.DeleteRID(storage.RID{}, row); err != nil {
				return err
			}
		}
		return nil
	}

	rid, err := t.locateRID(row)
	if err != nil {
		return err
	}
	for _, name := range t.IndexNames() {
		idx := t.indexes[name]
		if err := idx.DeleteRID(rid, row); err != nil {
			return err
		}
	}
	return t.heap.Delete(rid)
}

// locateRID encuentra el RID de una fila en el heap recorriéndolo: necesario
// para UPDATE y DELETE sobre tablas no agrupadas cuando el índice no está en
// la clave primaria.
func (t *Table) locateRID(row storage.Tuple) (storage.RID, error) {
	var found storage.RID
	var ok bool
	err := t.heap.Scan(func(rid storage.RID, tp storage.Tuple) bool {
		if tupleEqual(tp, row) {
			found, ok = rid, true
			return false
		}
		return true
	})
	if err != nil {
		return storage.RID{}, err
	}
	if !ok {
		return storage.RID{}, fmt.Errorf("%w: la fila a modificar no está en %s", storage.ErrRecordNotFound, t.Name())
	}
	return found, nil
}

func tupleEqual(a, b storage.Tuple) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if compareAny(a[i], b[i]) != 0 {
			return false
		}
	}
	return true
}

// validateTuple comprueba aridad, tipos y que ninguna columna exceda su MaxLen.
func (t *Table) validateTuple(row storage.Tuple) error {
	return t.Schema.CheckTuple(row)
}

func (t *Table) Close() error {
	var errs []error
	for _, name := range t.IndexNames() {
		if err := t.indexes[name].Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if t.heap != nil {
		if err := t.heap.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if t.seq != nil {
		if err := t.seq.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return joinErrors(errs)
}

func idxName(idx Index) string {
	if idx == nil {
		return ""
	}
	return idx.Name()
}
