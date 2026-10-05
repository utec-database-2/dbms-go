package sql

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dbms-go/v2/dbms/lib/dsl/ast"
	"github.com/dbms-go/v2/dbms/lib/external/iterator"
	"github.com/dbms-go/v2/dbms/lib/external/sorting"
	"github.com/dbms-go/v2/dbms/lib/index/rtree"
	"github.com/dbms-go/v2/dbms/lib/shared"
	"github.com/dbms-go/v2/dbms/lib/storage"
)

const rtreeFanout = 16

// spatialIndex es el R-Tree de una columna POINT. Se construye la primera vez
// que una consulta lo necesita y se mantiene al día en los INSERT; un DELETE
// lo descarta y se reconstruye en la siguiente consulta.
type spatialIndex struct {
	tree *rtree.RTree
	size int
}

func (t *table) rtreeFor(col int) (*spatialIndex, bool, error) {
	if si, ok := t.spatial[col]; ok {
		return si, false, nil
	}

	si := &spatialIndex{tree: rtree.New(rtreeFanout)}
	var scanErr error

	err := t.heap.Scan(func(rid storage.RID, payload []byte) bool {
		vals, err := decodeRow(payload)
		if err != nil {
			scanErr = fmt.Errorf("sql: decodificación de fila: %v", err)
			return false
		}
		if p, ok := vals[col].(shared.Point); ok {
			si.tree.Insert(rtree.Entry{Point: rtree.Point{Lat: p.Lat, Lon: p.Lon}, RID: rid})
			si.size++
		}
		return true
	})
	if err != nil {
		return nil, false, err
	}
	if scanErr != nil {
		return nil, false, scanErr
	}

	if t.spatial == nil {
		t.spatial = make(map[int]*spatialIndex)
	}
	t.spatial[col] = si
	return si, true, nil
}

func (t *table) indexInsertedPoints(vals []any, rid storage.RID) {
	for col, si := range t.spatial {
		if p, ok := vals[col].(shared.Point); ok {
			si.tree.Insert(rtree.Entry{Point: rtree.Point{Lat: p.Lat, Lon: p.Lon}, RID: rid})
			si.size++
		}
	}
}

func (t *table) dropSpatialIndexes() {
	t.spatial = nil
}

func isPointType(typeName string) bool {
	return strings.EqualFold(typeName, "POINT")
}

// pointOperand es un argumento de distancia(): una columna POINT o un literal.
type pointOperand struct {
	col  int
	name string
	lit  shared.Point
}

func (o pointOperand) isColumn() bool { return o.col >= 0 }

func (o pointOperand) value(vals []any) rtree.Point {
	p := o.lit
	if o.isColumn() {
		p, _ = vals[o.col].(shared.Point)
	}
	return rtree.Point{Lat: p.Lat, Lon: p.Lon}
}

func (o pointOperand) String() string {
	if o.isColumn() {
		return o.name
	}
	return o.lit.String()
}

// distanceSpec es una llamada a distancia(a, b[, 'metrica']) ya validada.
// Con haversine (por defecto) devuelve metros; con euclidean, grados.
type distanceSpec struct {
	a, b   pointOperand
	metric rtree.DistanceMetric
}

func (d *distanceSpec) eval(vals []any) float64 {
	dist := rtree.Distance(d.a.value(vals), d.b.value(vals), d.metric)
	if d.metric == rtree.Haversine {
		return dist * 1000
	}
	return dist
}

func (d *distanceSpec) unit() string {
	if d.metric == rtree.Haversine {
		return "m"
	}
	return "grados"
}

func (d *distanceSpec) metricName() string {
	if d.metric == rtree.Haversine {
		return "Haversine"
	}
	return "Euclidiana"
}

func (d *distanceSpec) String() string {
	return fmt.Sprintf("distancia(%s, %s)", d.a, d.b)
}

// toTreeUnits pasa un valor en las unidades de distancia() a las del R-Tree
// (km con Haversine, grados con Euclidiana).
func (d *distanceSpec) toTreeUnits(v float64) float64 {
	if d.metric == rtree.Haversine {
		return v / 1000
	}
	return v
}

// indexable indica si el R-Tree puede resolverla: una columna contra un literal.
func (d *distanceSpec) indexable() (col int, center rtree.Point, ok bool) {
	switch {
	case d.a.isColumn() && !d.b.isColumn():
		return d.a.col, d.b.value(nil), true
	case d.b.isColumn() && !d.a.isColumn():
		return d.b.col, d.a.value(nil), true
	}
	return 0, rtree.Point{}, false
}

func (d *distanceSpec) indexColumnName() string {
	if d.a.isColumn() && !d.b.isColumn() {
		return d.a.name
	}
	return d.b.name
}

func resolveOperand(node ast.ASTNode, schema *ast.CreateTable) (pointOperand, error) {
	switch n := node.(type) {
	case *ast.PointExpr:
		return pointOperand{col: -1, lit: shared.Point{Lat: n.Lat, Lon: n.Lon}}, nil
	case *ast.IdExpr:
		pos := columnPos(schema, n.Name)
		if pos < 0 {
			return pointOperand{}, fmt.Errorf("sql: columna desconocida %q", n.Name)
		}
		if !isPointType(schema.Columns[pos].Type.Name) {
			return pointOperand{}, fmt.Errorf("sql: distancia() requiere columnas POINT, y %q es %s",
				n.Name, strings.ToUpper(schema.Columns[pos].Type.Name))
		}
		return pointOperand{col: pos, name: n.Name}, nil
	}
	return pointOperand{}, fmt.Errorf("sql: los argumentos de distancia() deben ser columnas POINT o POINT(lat, lon)")
}

func resolveDistance(call *ast.CallExpr, schema *ast.CreateTable) (*distanceSpec, error) {
	if !strings.EqualFold(call.Name, "distancia") {
		return nil, fmt.Errorf("sql: función desconocida %q", call.Name)
	}
	if len(call.Args) != 2 && len(call.Args) != 3 {
		return nil, fmt.Errorf("sql: distancia() recibe 2 argumentos (y una métrica opcional), no %d", len(call.Args))
	}

	a, err := resolveOperand(call.Args[0], schema)
	if err != nil {
		return nil, err
	}
	b, err := resolveOperand(call.Args[1], schema)
	if err != nil {
		return nil, err
	}

	spec := &distanceSpec{a: a, b: b, metric: rtree.Haversine}

	if len(call.Args) == 3 {
		lit, ok := call.Args[2].(*ast.StringExpr)
		if !ok {
			return nil, fmt.Errorf("sql: la métrica de distancia() debe ser un texto: 'haversine' o 'euclidean'")
		}
		switch strings.ToLower(strings.Trim(lit.Value, "'\"")) {
		case "haversine":
			spec.metric = rtree.Haversine
		case "euclidean":
			spec.metric = rtree.Euclidean
		default:
			return nil, fmt.Errorf("sql: métrica desconocida %s: use 'haversine' o 'euclidean'", lit.Value)
		}
	}

	return spec, nil
}

// spatialWhere es un WHERE de la forma distancia(...) <op> numero.
type spatialWhere struct {
	dist      *distanceSpec
	op        ast.Operator
	threshold float64
}

func (w *spatialWhere) matches(d float64) bool {
	switch w.op {
	case ast.OpLt:
		return d < w.threshold
	case ast.OpLte:
		return d <= w.threshold
	case ast.OpGt:
		return d > w.threshold
	case ast.OpGte:
		return d >= w.threshold
	case ast.OpEq:
		return d == w.threshold
	case ast.OpNeq:
		return d != w.threshold
	}
	return false
}

func (w *spatialWhere) opText() string {
	switch w.op {
	case ast.OpLt:
		return "<"
	case ast.OpLte:
		return "<="
	case ast.OpGt:
		return ">"
	case ast.OpGte:
		return ">="
	case ast.OpEq:
		return "="
	}
	return "!="
}

func containsCall(node ast.ASTNode) bool {
	switch n := node.(type) {
	case *ast.CallExpr:
		return true
	case *ast.BinaryExpr:
		return containsCall(n.Left) || containsCall(n.Right)
	case *ast.WhereExpr:
		return containsCall(&n.Content)
	}
	return false
}

// analyzeSpatialWhere devuelve nil (sin error) si el WHERE no usa funciones.
func analyzeSpatialWhere(closure ast.ASTNode, schema *ast.CreateTable) (*spatialWhere, error) {
	wh, ok := closure.(*ast.WhereExpr)
	if !ok || wh == nil || !containsCall(wh) {
		return nil, nil
	}

	bin := wh.Content
	call, isCall := bin.Left.(*ast.CallExpr)
	if !isCall {
		return nil, fmt.Errorf("sql: con funciones, el WHERE debe ser distancia(...) <operador> <número>")
	}

	spec, err := resolveDistance(call, schema)
	if err != nil {
		return nil, err
	}

	var threshold float64
	switch r := bin.Right.(type) {
	case *ast.IntExpr:
		threshold = float64(r.Value)
	case *ast.FloatExpr:
		threshold = r.Value
	default:
		return nil, fmt.Errorf("sql: distancia() solo se puede comparar contra un número")
	}

	switch bin.Op {
	case ast.OpLt, ast.OpLte, ast.OpGt, ast.OpGte, ast.OpEq, ast.OpNeq:
	default:
		return nil, fmt.Errorf("sql: operador %v no soportado en WHERE con distancia()", bin.Op)
	}

	return &spatialWhere{dist: spec, op: bin.Op, threshold: threshold}, nil
}

// spatialOrder es un ORDER BY distancia(...).
type spatialOrder struct {
	dist *distanceSpec
	desc bool
}

func analyzeSpatialOrder(order *ast.OrderBy, schema *ast.CreateTable) (*spatialOrder, error) {
	if order == nil {
		return nil, nil
	}
	call, ok := order.Expr.(*ast.CallExpr)
	if !ok {
		return nil, nil
	}
	spec, err := resolveDistance(call, schema)
	if err != nil {
		return nil, err
	}
	return &spatialOrder{dist: spec, desc: order.Descendent}, nil
}

// canUseKNN: el vecino más cercano solo sirve para orden ascendente con LIMIT
// y una distancia entre una columna y un punto fijo.
func (o *spatialOrder) canUseKNN(limit *int) bool {
	if o.desc || limit == nil {
		return false
	}
	_, _, ok := o.dist.indexable()
	return ok
}

func (db *Database) fetchRow(t *table, rid storage.RID) (shared.Record, bool, error) {
	payload, err := t.heap.Read(rid)
	if err != nil {
		return shared.Record{}, false, nil
	}
	vals, err := decodeRow(payload)
	if err != nil {
		return shared.Record{}, false, fmt.Errorf("sql: decodificación de fila: %v", err)
	}
	return shared.Record{Values: vals, RID: rid}, true, nil
}

func sortRowsByKey(rows []shared.Record) {
	sort.SliceStable(rows, func(i, j int) bool {
		ki, _ := rows[i].Values[0].(int)
		kj, _ := rows[j].Values[0].(int)
		return ki < kj
	})
}

// spatialWhereRows resuelve distancia(...) <op> numero. Con < o <= entre una
// columna y un punto fijo usa el R-Tree; en cualquier otro caso recorre la tabla.
func (db *Database) spatialWhereRows(t *table, w *spatialWhere) ([]shared.Record, []string, error) {
	var plan []string

	if col, center, ok := w.dist.indexable(); ok && (w.op == ast.OpLt || w.op == ast.OpLte) {
		si, built, err := t.rtreeFor(col)
		if err != nil {
			return nil, nil, err
		}
		if built {
			plan = append(plan, fmt.Sprintf("Construcción del R-Tree sobre %s (%d puntos)", w.dist.indexColumnName(), si.size))
		}

		// El radio se agranda un poco: el R-Tree solo propone candidatos y
		// la comparación exacta (estricta para <) se hace con el valor de distancia().
		radius := w.dist.toTreeUnits(w.threshold)*(1+1e-9) + 1e-12
		entries := si.tree.SearchRadius(center, radius, w.dist.metric)

		plan = append(plan, fmt.Sprintf("Búsqueda por radio en el R-Tree sobre %s: distancia %s %v %s (%s)",
			w.dist.indexColumnName(), w.opText(), w.threshold, w.dist.unit(), w.dist.metricName()))

		rows := make([]shared.Record, 0, len(entries))
		for _, e := range entries {
			rec, ok, err := db.fetchRow(t, e.RID)
			if err != nil {
				return nil, nil, err
			}
			if ok && w.matches(w.dist.eval(rec.Values)) {
				rows = append(rows, rec)
			}
		}
		sortRowsByKey(rows)

		plan = append(plan, fmt.Sprintf("%d registro(s) recuperados del Heap File vía el RID del R-Tree", len(rows)))
		return rows, plan, nil
	}

	recs, err := t.idx.OrderedScan()
	if err != nil {
		return nil, nil, err
	}

	rows := make([]shared.Record, 0)
	for _, r := range recs {
		vals, err := decodeRow(r.Payload)
		if err != nil {
			return nil, nil, fmt.Errorf("sql: decodificación de fila: %v", err)
		}
		if w.matches(w.dist.eval(vals)) {
			rows = append(rows, shared.Record{Values: vals, RID: r.RID})
		}
	}

	plan = append(plan,
		fmt.Sprintf("Recorrido completo del índice B+ sobre %q, sin R-Tree (este filtro no se puede indexar)", t.schema.Columns[0].Name.Name),
		fmt.Sprintf("Filtro fila a fila: %s %s %v %s -> %d registro(s)", w.dist, w.opText(), w.threshold, w.dist.unit(), len(rows)),
	)
	return rows, plan, nil
}

// spatialKNNRows resuelve ORDER BY distancia(columna, punto) LIMIT k con k-NN.
func (db *Database) spatialKNNRows(t *table, o *spatialOrder, k int) ([]shared.Record, []string, error) {
	col, center, _ := o.dist.indexable()

	var plan []string
	si, built, err := t.rtreeFor(col)
	if err != nil {
		return nil, nil, err
	}
	if built {
		plan = append(plan, fmt.Sprintf("Construcción del R-Tree sobre %s (%d puntos)", o.dist.indexColumnName(), si.size))
	}

	neighbors := si.tree.KNN(center, k, o.dist.metric)

	plan = append(plan, fmt.Sprintf("k-NN en el R-Tree sobre %s: k=%d (%s)",
		o.dist.indexColumnName(), k, o.dist.metricName()))

	rows := make([]shared.Record, 0, len(neighbors))
	for _, n := range neighbors {
		rec, ok, err := db.fetchRow(t, n.Entry.RID)
		if err != nil {
			return nil, nil, err
		}
		if ok {
			rows = append(rows, rec)
		}
	}

	plan = append(plan, fmt.Sprintf("%d registro(s) recuperados del Heap File vía el RID del R-Tree, ya ordenados por distancia", len(rows)))
	return rows, plan, nil
}

// orderByDistance ordena por distancia(...) con External Sort.
func (db *Database) orderByDistance(rows []shared.Record, o *spatialOrder) ([]shared.Record, string, error) {
	sorter := sorting.New(0, db.dir)
	out, err := sorter.Sort(iterator.NewSliceIterator(rows), func(r shared.Record) any {
		return o.dist.eval(r.Values)
	})
	if err != nil {
		return nil, "", err
	}
	sorted, err := iterator.Drain(out)
	if err != nil {
		return nil, "", err
	}

	dir := "ASC"
	if o.desc {
		dir = "DESC"
		for i, j := 0, len(sorted)-1; i < j; i, j = i+1, j-1 {
			sorted[i], sorted[j] = sorted[j], sorted[i]
		}
	}

	return sorted, fmt.Sprintf("ORDER BY %s %s vía External Sort (k-way merge)", o.dist, dir), nil
}

func (db *Database) execDeleteSpatial(del *ast.Delete, t *table, w *spatialWhere) (*Result, error) {
	rows, plan, err := db.spatialWhereRows(t, w)
	if err != nil {
		return nil, err
	}

	affected := int64(0)
	for _, r := range rows {
		key, ok := r.Values[0].(int)
		if !ok {
			return nil, fmt.Errorf("sql: la clave (primera columna) debe ser entera")
		}
		if err := t.idx.Delete(key, r.RID); err != nil {
			return nil, err
		}
		affected++
	}
	t.dropSpatialIndexes()

	plan = append(plan, fmt.Sprintf("DELETE FROM %s: %d fila(s) eliminadas (lazy delete) vía el índice B+", del.From.Name, affected))
	return &Result{Affected: affected, Plan: plan}, nil
}
