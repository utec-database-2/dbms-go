package sql

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/dbms-go/v2/dbms/lib/dsl/ast"
	"github.com/dbms-go/v2/dbms/lib/index/rtree"
	"github.com/dbms-go/v2/dbms/lib/storage"
)

// Extensión espacial del motor.
//
// Una columna declarada como POINT se guarda en el almacenamiento como texto
// canónico "POINT(lat lon)" (el storage no tiene tipo flotante) y lleva siempre
// un índice R-Tree, rtree_<columna>, que el planner usa para:
//
//	WHERE distancia(col, POINT(lat, lon)) < metros      -> búsqueda por radio
//	ORDER BY distancia(col, POINT(lat, lon)) LIMIT k   -> k vecinos (k-NN)
//	WHERE dentro(col, POLYGON(POINT(..), POINT(..), ..)) -> intersección con polígono
//
// distancia() usa Haversine y devuelve metros. Con un tercer argumento
// 'euclidean' usa la distancia euclidiana en grados.

const (
	pointTypeName = "POINT"
	rtreePrefix   = "rtree"
	// pointMaxLen es el tamaño reservado para el texto canónico de un punto.
	pointMaxLen = 64
)

// isPointType indica si el tipo declarado de una columna es POINT.
func isPointType(col ast.ColumnExpr) bool {
	return strings.EqualFold(strings.TrimSpace(col.Type.Name), pointTypeName)
}

// formatPoint escribe un punto en su forma canónica.
func formatPoint(p rtree.Point) string {
	return "POINT(" + strconv.FormatFloat(p.Lat, 'f', -1, 64) + " " +
		strconv.FormatFloat(p.Lon, 'f', -1, 64) + ")"
}

// parsePoint lee un punto desde su forma de texto. Acepta "POINT(lat lon)",
// "POINT(lat, lon)" y "lat,lon".
func parsePoint(v any) (rtree.Point, error) {
	s, ok := v.(string)
	if !ok {
		return rtree.Point{}, fmt.Errorf("sql: %v no es un POINT", v)
	}
	body := strings.TrimSpace(s)
	if up := strings.ToUpper(body); strings.HasPrefix(up, "POINT") {
		body = strings.TrimSpace(body[len("POINT"):])
		body = strings.TrimPrefix(body, "(")
		body = strings.TrimSuffix(body, ")")
	}
	parts := strings.FieldsFunc(body, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	if len(parts) != 2 {
		return rtree.Point{}, fmt.Errorf("sql: %q no es un POINT(lat lon)", s)
	}
	lat, err1 := strconv.ParseFloat(parts[0], 64)
	lon, err2 := strconv.ParseFloat(parts[1], 64)
	if err1 != nil || err2 != nil {
		return rtree.Point{}, fmt.Errorf("sql: %q no es un POINT(lat lon)", s)
	}
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return rtree.Point{}, fmt.Errorf("sql: %q está fuera de rango (lat ±90, lon ±180)", s)
	}
	return rtree.Point{Lat: lat, Lon: lon}, nil
}

// formatPolygon y parsePolygon hacen lo mismo para los polígonos, que solo
// existen como valores de consulta: "POLYGON((lat lon, lat lon, ...))".
func formatPolygon(pg rtree.Polygon) string {
	parts := make([]string, len(pg))
	for i, p := range pg {
		parts[i] = strconv.FormatFloat(p.Lat, 'f', -1, 64) + " " + strconv.FormatFloat(p.Lon, 'f', -1, 64)
	}
	return "POLYGON((" + strings.Join(parts, ", ") + "))"
}

func parsePolygon(v any) (rtree.Polygon, error) {
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("sql: %v no es un POLYGON", v)
	}
	body := strings.TrimSpace(s)
	if !strings.HasPrefix(strings.ToUpper(body), "POLYGON") {
		return nil, fmt.Errorf("sql: %q no es un POLYGON", s)
	}
	body = strings.Trim(strings.TrimSpace(body[len("POLYGON"):]), "()")
	var pg rtree.Polygon
	for _, pair := range strings.Split(body, ",") {
		p, err := parsePoint(pair)
		if err != nil {
			return nil, fmt.Errorf("sql: vértice inválido en el polígono: %w", err)
		}
		pg = append(pg, p)
	}
	if len(pg) < 3 {
		return nil, fmt.Errorf("sql: un polígono necesita al menos 3 vértices")
	}
	return pg, nil
}

// parseMetric traduce el tercer argumento opcional de distancia().
func parseMetric(v any) (rtree.DistanceMetric, error) {
	s, _ := v.(string)
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "haversine", "geodesica", "geodésica":
		return rtree.Haversine, nil
	case "euclidean", "euclidiana", "euclideana":
		return rtree.Euclidean, nil
	}
	return 0, fmt.Errorf("sql: métrica %v desconocida (use 'haversine' o 'euclidean')", v)
}

// spatialDistance es la distancia que devuelve distancia(): metros con
// Haversine y grados con la euclidiana.
func spatialDistance(a, b rtree.Point, metric rtree.DistanceMetric) float64 {
	d := rtree.Distance(a, b, metric)
	if metric == rtree.Haversine {
		return d * 1000
	}
	return d
}

func isDistanceFunc(name string) bool {
	switch strings.ToLower(name) {
	case "distancia", "distance", "st_distance":
		return true
	}
	return false
}

func isWithinFunc(name string) bool {
	switch strings.ToLower(name) {
	case "dentro", "within", "st_within", "st_contains", "st_intersects":
		return true
	}
	return false
}

// evalSpatialFunc evalúa las funciones escalares espaciales. ok es false si
// el nombre no es una función espacial.
func (ec *evalContext) evalSpatialFunc(fn *ast.FuncCallExpr) (any, bool, error) {
	name := strings.ToLower(fn.Name)
	args := make([]any, len(fn.Args))
	evalArgs := func() error {
		for i, a := range fn.Args {
			v, err := ec.eval(a)
			if err != nil {
				return err
			}
			args[i] = v
		}
		return nil
	}

	switch {
	case name == "point":
		if len(fn.Args) != 2 {
			return nil, true, fmt.Errorf("sql: POINT espera (lat, lon)")
		}
		if err := evalArgs(); err != nil {
			return nil, true, err
		}
		lat, ok1 := toFloat64(args[0])
		lon, ok2 := toFloat64(args[1])
		if !ok1 || !ok2 {
			return nil, true, fmt.Errorf("sql: POINT espera números, recibió (%v, %v)", args[0], args[1])
		}
		p := rtree.Point{Lat: lat, Lon: lon}
		if _, err := parsePoint(formatPoint(p)); err != nil {
			return nil, true, err
		}
		return formatPoint(p), true, nil

	case name == "polygon":
		if err := evalArgs(); err != nil {
			return nil, true, err
		}
		pg := make(rtree.Polygon, 0, len(args))
		for _, a := range args {
			p, err := parsePoint(a)
			if err != nil {
				return nil, true, err
			}
			pg = append(pg, p)
		}
		if len(pg) < 3 {
			return nil, true, fmt.Errorf("sql: POLYGON necesita al menos 3 puntos")
		}
		return formatPolygon(pg), true, nil

	case isDistanceFunc(name):
		if len(fn.Args) != 2 && len(fn.Args) != 3 {
			return nil, true, fmt.Errorf("sql: distancia espera (punto, punto [, métrica])")
		}
		if err := evalArgs(); err != nil {
			return nil, true, err
		}
		if args[0] == nil || args[1] == nil {
			return nil, true, nil
		}
		a, err := parsePoint(args[0])
		if err != nil {
			return nil, true, err
		}
		b, err := parsePoint(args[1])
		if err != nil {
			return nil, true, err
		}
		metric := rtree.Haversine
		if len(args) == 3 {
			if metric, err = parseMetric(args[2]); err != nil {
				return nil, true, err
			}
		}
		return spatialDistance(a, b, metric), true, nil

	case isWithinFunc(name):
		if len(fn.Args) != 2 {
			return nil, true, fmt.Errorf("sql: dentro espera (punto, polígono)")
		}
		if err := evalArgs(); err != nil {
			return nil, true, err
		}
		if args[0] == nil {
			return false, true, nil
		}
		p, err := parsePoint(args[0])
		if err != nil {
			return nil, true, err
		}
		pg, err := parsePolygon(args[1])
		if err != nil {
			return nil, true, err
		}
		return pg.Contains(p), true, nil

	case name == "latitud" || name == "lat":
		if err := evalArgs(); err != nil || len(args) != 1 {
			return nil, true, fmt.Errorf("sql: latitud espera un punto")
		}
		p, err := parsePoint(args[0])
		if err != nil {
			return nil, true, err
		}
		return p.Lat, true, nil

	case name == "longitud" || name == "lon":
		if err := evalArgs(); err != nil || len(args) != 1 {
			return nil, true, fmt.Errorf("sql: longitud espera un punto")
		}
		p, err := parsePoint(args[0])
		if err != nil {
			return nil, true, err
		}
		return p.Lon, true, nil
	}
	return nil, false, nil
}

// ---------------------------------------------------------------------------
// Índice R-Tree
// ---------------------------------------------------------------------------

// rtreeRef es lo que el R-Tree guarda por cada fila: el punto y la clave
// primaria, que sirve para leer la fila tanto en heap como en secuencial.
type rtreeRef struct {
	point rtree.Point
	pk    any
}

// rtreeIndex es el índice espacial de una columna POINT. El R-Tree vive en
// memoria y se reconstruye desde la tabla al abrir la base, igual que el resto
// de índices. Las bajas son lazy: la entrada queda en el árbol hasta la próxima
// reconstrucción y se ignora al resolver resultados.
type rtreeIndex struct {
	baseIndex
	pos   int
	tree  *rtree.RTree
	refs  map[storage.RID]rtreeRef
	byPK  map[string]storage.RID
	next  int64
	stale int
}

func newRTreeIndex(name, col string, tbl *Table) (Index, error) {
	pos, ok := tbl.ColumnIndex(col)
	if !ok {
		return nil, fmt.Errorf("sql: la columna %s no existe en %s", col, tbl.Name())
	}
	if tbl.Schema.Types[pos] != storage.TypeString {
		return nil, fmt.Errorf("sql: el R-Tree necesita una columna POINT, %s es %s", col, tbl.Schema.Types[pos])
	}
	if len(tbl.Schema.KeyCols) == 0 {
		return nil, fmt.Errorf("sql: el R-Tree necesita que %s tenga clave primaria", tbl.Name())
	}
	idx := &rtreeIndex{
		baseIndex: baseIndex{name: name, kind: RTreeIndexKind, col: tbl.Schema.Columns[pos], table: tbl},
		pos:       pos,
	}
	idx.reset()
	if err := idx.Rebuild(); err != nil {
		return nil, err
	}
	return idx, nil
}

func (idx *rtreeIndex) reset() {
	idx.tree = rtree.New(16)
	idx.refs = make(map[storage.RID]rtreeRef)
	idx.byPK = make(map[string]storage.RID)
	idx.stale = 0
}

func (idx *rtreeIndex) SupportsRange() bool { return false }

func (idx *rtreeIndex) pkOf(t storage.Tuple) any {
	pk := idx.table.Schema.KeyCols[0]
	if pk >= len(t) {
		return nil
	}
	return t[pk]
}

func pkKey(v any) string { return fmt.Sprintf("%T:%v", v, v) }

func (idx *rtreeIndex) Insert(t storage.Tuple, _ storage.RID) error {
	if idx.pos >= len(t) || t[idx.pos] == nil {
		return nil // un punto nulo no se indexa
	}
	p, err := parsePoint(t[idx.pos])
	if err != nil {
		return err
	}
	pk := idx.pkOf(t)
	if old, ok := idx.byPK[pkKey(pk)]; ok {
		delete(idx.refs, old)
		idx.stale++
	}
	idx.next++
	id := storage.RID{File: -1, Page: int32(idx.next >> 31), Slot: int32(idx.next & math.MaxInt32)}
	idx.refs[id] = rtreeRef{point: p, pk: pk}
	idx.byPK[pkKey(pk)] = id
	idx.tree.Insert(rtree.Entry{Point: p, RID: id})
	return nil
}

func (idx *rtreeIndex) DeleteRID(_ storage.RID, t storage.Tuple) error {
	key := pkKey(idx.pkOf(t))
	if id, ok := idx.byPK[key]; ok {
		delete(idx.byPK, key)
		delete(idx.refs, id)
		idx.stale++
	}
	// Muchas bajas acumuladas: se compacta el árbol.
	if idx.stale > 64 && idx.stale > len(idx.refs) {
		idx.compact()
	}
	return nil
}

func (idx *rtreeIndex) UpdateRID(rid storage.RID, prev, next storage.Tuple) error {
	if err := idx.DeleteRID(rid, prev); err != nil {
		return err
	}
	return idx.Insert(next, rid)
}

// compact reconstruye el árbol solo con las entradas vivas.
func (idx *rtreeIndex) compact() {
	tree := rtree.New(16)
	for id, ref := range idx.refs {
		tree.Insert(rtree.Entry{Point: ref.point, RID: id})
	}
	idx.tree = tree
	idx.stale = 0
}

func (idx *rtreeIndex) Rebuild() error {
	idx.reset()
	rows, err := idx.table.scanTable()
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := idx.Insert(r, storage.RID{}); err != nil {
			return err
		}
	}
	return nil
}

func (idx *rtreeIndex) Entries() int { return len(idx.refs) }

func (idx *rtreeIndex) Close() error { return nil }

// SearchExact busca las filas cuyo punto es exactamente key.
func (idx *rtreeIndex) SearchExact(key any) ([]storage.Tuple, error) {
	p, err := parsePoint(key)
	if err != nil {
		return nil, err
	}
	return idx.resolve(idx.tree.SearchRect(rtree.PointRect(p)))
}

func (idx *rtreeIndex) SearchRange(_, _ any) ([]storage.Tuple, error) {
	return nil, fmt.Errorf("sql: el R-Tree %s no admite búsquedas por rango de valores", idx.name)
}

// Radius devuelve las filas a menos de radius del centro. radius va en metros
// con Haversine y en grados con la euclidiana.
func (idx *rtreeIndex) Radius(center rtree.Point, radius float64, metric rtree.DistanceMetric) ([]storage.Tuple, error) {
	r := radius
	if metric == rtree.Haversine {
		r = radius / 1000
	}
	return idx.resolve(idx.tree.SearchRadius(center, r, metric))
}

// KNN devuelve las k filas más cercanas al centro, de la más cercana a la más
// lejana. Pide k más las bajas pendientes para que las entradas muertas no
// dejen el resultado corto.
func (idx *rtreeIndex) KNN(center rtree.Point, k int, metric rtree.DistanceMetric) ([]storage.Tuple, error) {
	if k <= 0 {
		return nil, nil
	}
	neighbors := idx.tree.KNN(center, k+idx.stale, metric)
	entries := make([]rtree.Entry, 0, len(neighbors))
	for _, n := range neighbors {
		if _, ok := idx.refs[n.Entry.RID]; ok {
			entries = append(entries, n.Entry)
		}
		if len(entries) == k {
			break
		}
	}
	return idx.resolve(entries)
}

// Polygon devuelve las filas cuyo punto cae dentro del polígono.
func (idx *rtreeIndex) Polygon(pg rtree.Polygon) ([]storage.Tuple, error) {
	entries, err := idx.tree.SearchPolygon(pg)
	if err != nil {
		return nil, err
	}
	return idx.resolve(entries)
}

// resolve convierte entradas del R-Tree en filas, leyéndolas por clave
// primaria. Conserva el orden de las entradas (importa en k-NN).
func (idx *rtreeIndex) resolve(entries []rtree.Entry) ([]storage.Tuple, error) {
	pkIdx, ok := idx.table.IndexOnColumn(idx.table.Schema.KeyCols[0])
	if !ok {
		return nil, fmt.Errorf("sql: %s no tiene índice de clave primaria", idx.table.Name())
	}
	out := make([]storage.Tuple, 0, len(entries))
	for _, e := range entries {
		ref, ok := idx.refs[e.RID]
		if !ok {
			continue // baja lazy
		}
		rows, err := pkIdx.SearchExact(ref.pk)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

// rtreeOn devuelve el R-Tree de una columna, si lo tiene.
func (t *Table) rtreeOn(col int) (*rtreeIndex, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, name := range t.order {
		if idx, ok := t.indexes[name].(*rtreeIndex); ok && idx.pos == col {
			return idx, true
		}
	}
	return nil, false
}

// ---------------------------------------------------------------------------
// Planner espacial
// ---------------------------------------------------------------------------

// spatialArgs reconoce func(columna, constante [, métrica]) y devuelve la
// columna con R-Tree y el valor constante ya evaluado.
func (t *Table) spatialArgs(fn *ast.FuncCallExpr) (*rtreeIndex, any, rtree.DistanceMetric, bool) {
	if len(fn.Args) < 2 {
		return nil, nil, 0, false
	}
	id, ok := fn.Args[0].(*ast.IdExpr)
	if !ok {
		return nil, nil, 0, false
	}
	col, ok := t.ColumnIndex(id.Name)
	if !ok {
		return nil, nil, 0, false
	}
	idx, ok := t.rtreeOn(col)
	if !ok {
		return nil, nil, 0, false
	}
	ec := &evalContext{table: t}
	val, err := ec.eval(fn.Args[1])
	if err != nil {
		return nil, nil, 0, false
	}
	metric := rtree.Haversine
	if len(fn.Args) == 3 {
		m, err := ec.eval(fn.Args[2])
		if err != nil {
			return nil, nil, 0, false
		}
		if metric, err = parseMetric(m); err != nil {
			return nil, nil, 0, false
		}
	}
	return idx, val, metric, true
}

// matchSpatial intenta resolver una condición del WHERE con un R-Tree:
// distancia(col, punto) < r (o <=) y dentro(col, polígono).
func (t *Table) matchSpatial(conds []ast.BinaryExpr) ([]storage.Tuple, Step, bool, error) {
	for _, c := range conds {
		// distancia(col, POINT(...)) < radio
		if c.Op == ast.OpLt || c.Op == ast.OpLte {
			fn, ok := c.Left.(*ast.FuncCallExpr)
			if !ok || !isDistanceFunc(fn.Name) || !isLiteral(c.Right) {
				continue
			}
			idx, val, metric, ok := t.spatialArgs(fn)
			if !ok {
				continue
			}
			center, err := parsePoint(val)
			if err != nil {
				return nil, Step{}, false, err
			}
			rv, err := (&evalContext{table: t}).eval(c.Right)
			if err != nil {
				return nil, Step{}, false, err
			}
			radius, ok := toFloat64(rv)
			if !ok {
				continue
			}
			rows, err := idx.Radius(center, radius, metric)
			if err != nil {
				return nil, Step{}, false, err
			}
			unit := "m"
			if metric == rtree.Euclidean {
				unit = "° (euclidiana)"
			}
			return rows, Step{
				Kind: StepIndexSeek,
				Detail: fmt.Sprintf("búsqueda por radio en R-Tree %s: %s a ≤ %g %s de %s",
					idx.Name(), idx.Column(), radius, unit, formatPoint(center)),
				Rows: len(rows),
				Cost: len(rows) + idx.Entries()/16,
			}, true, nil
		}
	}
	return nil, Step{}, false, nil
}

// matchWithin reconoce un dentro(col, POLYGON(...)) suelto en el WHERE, que
// llega como llamada a función y no como comparación.
func (t *Table) matchWithin(node ast.ASTNode) ([]storage.Tuple, Step, bool, error) {
	var fn *ast.FuncCallExpr
	switch n := node.(type) {
	case *ast.FuncCallExpr:
		fn = n
	case *ast.WhereExpr:
		if f, ok := n.Content.Left.(*ast.FuncCallExpr); ok && n.Content.Op == ast.OpUnknown {
			fn = f
		}
		if fn == nil && n.Content.Op == ast.OpAnd {
			for _, side := range []ast.ASTNode{n.Content.Left, n.Content.Right} {
				if rows, step, ok, err := t.matchWithin(side); ok || err != nil {
					return rows, step, ok, err
				}
			}
		}
		if fn == nil && n.Content.Op == ast.OpEq {
			// dentro(...) = true
			if f, ok := n.Content.Left.(*ast.FuncCallExpr); ok {
				if b, ok := n.Content.Right.(*ast.BoolExpr); ok && b.Value {
					fn = f
				}
			}
		}
	case *ast.BinaryExpr:
		if n.Op == ast.OpAnd {
			for _, side := range []ast.ASTNode{n.Left, n.Right} {
				if rows, step, ok, err := t.matchWithin(side); ok || err != nil {
					return rows, step, ok, err
				}
			}
		}
		if n.Op == ast.OpEq {
			if f, ok := n.Left.(*ast.FuncCallExpr); ok {
				if b, ok := n.Right.(*ast.BoolExpr); ok && b.Value {
					fn = f
				}
			}
		}
	}
	if fn == nil || !isWithinFunc(fn.Name) {
		return nil, Step{}, false, nil
	}
	idx, val, _, ok := t.spatialArgs(fn)
	if !ok {
		return nil, Step{}, false, nil
	}
	pg, err := parsePolygon(val)
	if err != nil {
		return nil, Step{}, false, err
	}
	rows, err := idx.Polygon(pg)
	if err != nil {
		return nil, Step{}, false, err
	}
	return rows, Step{
		Kind:   StepIndexSeek,
		Detail: fmt.Sprintf("intersección con polígono de %d vértices en R-Tree %s sobre %s", len(pg), idx.Name(), idx.Column()),
		Rows:   len(rows),
		Cost:   len(rows) + idx.Entries()/16,
	}, true, nil
}

// planKNN resuelve SELECT ... ORDER BY distancia(col, punto) [ASC] LIMIT k con
// el k-NN del R-Tree, sin ordenar toda la tabla. Solo aplica sin WHERE, GROUP
// BY ni agregados; en otro caso el motor ordena con external sort.
func (e *Engine) planKNN(tbl *Table, n *ast.Select) ([]storage.Tuple, []Step, bool, error) {
	if n.Closure != nil || len(n.GroupBy) > 0 || n.Having != nil || len(n.Join) > 0 ||
		n.OrderBy == nil || n.OrderBy.Descendent || n.Limit == nil || n.Limit.Count == nil || selectHasAggregate(n) {
		return nil, nil, false, nil
	}
	fn, ok := n.OrderBy.Expr.(*ast.FuncCallExpr)
	if !ok || !isDistanceFunc(fn.Name) {
		return nil, nil, false, nil
	}
	idx, val, metric, ok := tbl.spatialArgs(fn)
	if !ok {
		return nil, nil, false, nil
	}
	center, err := parsePoint(val)
	if err != nil {
		return nil, nil, false, err
	}
	ec := &evalContext{table: tbl}
	cv, err := ec.eval(n.Limit.Count)
	if err != nil {
		return nil, nil, false, err
	}
	k, ok := asInt64(cv)
	if !ok || k < 0 {
		return nil, nil, false, fmt.Errorf("sql: LIMIT inválido")
	}
	offset := int64(0)
	if n.Limit.Offset != nil {
		ov, err := ec.eval(n.Limit.Offset)
		if err != nil {
			return nil, nil, false, err
		}
		if offset, ok = asInt64(ov); !ok || offset < 0 {
			return nil, nil, false, fmt.Errorf("sql: OFFSET inválido")
		}
	}
	rows, err := idx.KNN(center, int(k+offset), metric)
	if err != nil {
		return nil, nil, false, err
	}
	if int(offset) >= len(rows) {
		rows = nil
	} else {
		rows = rows[offset:]
	}
	metricName := "haversine"
	if metric == rtree.Euclidean {
		metricName = "euclidiana"
	}
	return rows, []Step{{
		Kind: StepIndexScan,
		Detail: fmt.Sprintf("k-NN en R-Tree %s: los %d vecinos más cercanos a %s (%s, best-first sin ordenar la tabla)",
			idx.Name(), k, formatPoint(center), metricName),
		Rows: len(rows),
		Cost: len(rows) + idx.Entries()/16,
	}}, true, nil
}
