package rtree

import (
	"errors"
	"math"
)

var ErrInvalidPolygon = errors.New("rtree: el polígono necesita al menos 3 vértices con coordenadas válidas")

// Polygon es un polígono dado por sus vértices en orden; el último se une con
// el primero. Las aristas son rectas en el plano (lat, lon) y el interior se
// define con la regla par-impar, así que también funciona con polígonos
// cóncavos y con orientación horaria o antihoraria. Los puntos sobre el
// borde cuentan como dentro.
type Polygon []Point

// normalized valida el polígono y le quita el vértice de cierre si viene repetido.
func (pg Polygon) normalized() (Polygon, error) {
	out := pg
	if n := len(out); n > 1 && out[0] == out[n-1] {
		out = out[:n-1]
	}
	if len(out) < 3 {
		return nil, ErrInvalidPolygon
	}
	for _, v := range out {
		if math.IsNaN(v.Lat) || math.IsNaN(v.Lon) || math.IsInf(v.Lat, 0) || math.IsInf(v.Lon, 0) {
			return nil, ErrInvalidPolygon
		}
	}
	return out, nil
}

// Bounds devuelve el rectángulo mínimo que contiene al polígono.
func (pg Polygon) Bounds() Rect {
	if len(pg) == 0 {
		return Rect{}
	}
	r := PointRect(pg[0])
	for _, v := range pg[1:] {
		r = r.Union(PointRect(v))
	}
	return r
}

// Contains indica si p está dentro del polígono o sobre su borde.
func (pg Polygon) Contains(p Point) bool {
	inside := false

	for i, j := 0, len(pg)-1; i < len(pg); j, i = i, i+1 {
		a, b := pg[j], pg[i]

		if onSegment(a, b, p) {
			return true
		}

		if (a.Lon > p.Lon) != (b.Lon > p.Lon) {
			x := a.Lat + (p.Lon-a.Lon)*(b.Lat-a.Lat)/(b.Lon-a.Lon)
			if p.Lat < x {
				inside = !inside
			}
		}
	}

	return inside
}

// cross es el producto cruz (b-a) x (c-a).
func cross(a, b, c Point) float64 {
	return (b.Lat-a.Lat)*(c.Lon-a.Lon) - (b.Lon-a.Lon)*(c.Lat-a.Lat)
}

func sign(v float64) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}

// onSegment indica si p está sobre el segmento ab (extremos incluidos).
func onSegment(a, b, p Point) bool {
	if cross(a, b, p) != 0 {
		return false
	}
	return p.Lat >= math.Min(a.Lat, b.Lat) && p.Lat <= math.Max(a.Lat, b.Lat) &&
		p.Lon >= math.Min(a.Lon, b.Lon) && p.Lon <= math.Max(a.Lon, b.Lon)
}

// segmentsIntersect indica si los segmentos ab y cd se cruzan o se tocan.
func segmentsIntersect(a, b, c, d Point) bool {
	o1 := sign(cross(a, b, c))
	o2 := sign(cross(a, b, d))
	o3 := sign(cross(c, d, a))
	o4 := sign(cross(c, d, b))

	if o1*o2 < 0 && o3*o4 < 0 {
		return true
	}

	return onSegment(a, b, c) || onSegment(a, b, d) ||
		onSegment(c, d, a) || onSegment(c, d, b)
}

// polygonEpsilon (grados, ≈0.1 mm) agranda los rectángulos al clasificarlos. Un
// punto a ~1e-16 de una arista puede dar distinto con la geometría exacta de
// relate y con la aritmética de Contains; con este margen, todo lo que está
// tan cerca de un borde se decide punto por punto con Contains, y relate solo
// descarta o acepta nodos que están claramente lejos de cualquier arista.
const polygonEpsilon = 1e-9

type relation int

const (
	disjoint relation = iota
	partial
	inside
)

// queryPolygon es un polígono ya validado, con su rectángulo envolvente calculado una vez.
type queryPolygon struct {
	pts    Polygon
	bounds Rect
}

// relate clasifica un rectángulo respecto del polígono:
//   - disjoint: no comparten ningún punto;
//   - inside: el rectángulo completo cae dentro del polígono;
//   - partial: se cruzan (o no se puede asegurar otra cosa).
//
// Si ninguna arista del polígono toca el borde del rectángulo, todo el
// rectángulo está en la misma zona (dentro o fuera) salvo que el polígono
// quede completamente dentro del rectángulo; por eso basta mirar una esquina
// y los vértices.
func (q queryPolygon) relate(r Rect) relation {
	r = Rect{
		Min: Point{Lat: r.Min.Lat - polygonEpsilon, Lon: r.Min.Lon - polygonEpsilon},
		Max: Point{Lat: r.Max.Lat + polygonEpsilon, Lon: r.Max.Lon + polygonEpsilon},
	}

	if !q.bounds.Intersects(r) {
		return disjoint
	}

	corners := [4]Point{
		r.Min,
		{Lat: r.Min.Lat, Lon: r.Max.Lon},
		r.Max,
		{Lat: r.Max.Lat, Lon: r.Min.Lon},
	}

	for i, j := 0, len(q.pts)-1; i < len(q.pts); j, i = i, i+1 {
		a, b := q.pts[j], q.pts[i]
		for k := 0; k < 4; k++ {
			if segmentsIntersect(a, b, corners[k], corners[(k+1)%4]) {
				return partial
			}
		}
	}

	if q.pts.Contains(r.Min) {
		return inside
	}

	for _, v := range q.pts {
		if r.Contains(v) {
			return partial
		}
	}

	return disjoint
}

// SearchPolygon devuelve las entradas cuyo punto está dentro del polígono
// (borde incluido). Descarta ramas enteras cuyo MBR no toca el polígono y, si
// el MBR de un nodo cae completo dentro, devuelve todo lo que hay debajo sin
// probar punto por punto.
func (t *RTree) SearchPolygon(pg Polygon) ([]Entry, error) {
	pts, err := pg.normalized()
	if err != nil {
		return nil, err
	}

	if t.Root == nil || (t.Root.Leaf && len(t.Root.Entries) == 0) {
		return nil, nil
	}

	var result []Entry
	t.searchPolygon(t.Root, queryPolygon{pts: pts, bounds: pts.Bounds()}, &result)

	return result, nil
}

func (t *RTree) searchPolygon(n *Node, q queryPolygon, result *[]Entry) {
	switch q.relate(n.box) {
	case disjoint:
		return
	case inside:
		collectAll(n, result)
		return
	}

	if n.Leaf {
		for _, e := range n.Entries {
			if q.pts.Contains(e.Point) {
				*result = append(*result, e)
			}
		}
		return
	}

	for _, child := range n.Children {
		t.searchPolygon(child, q, result)
	}
}

func collectAll(n *Node, result *[]Entry) {
	if n.Leaf {
		*result = append(*result, n.Entries...)
		return
	}
	for _, child := range n.Children {
		collectAll(child, result)
	}
}
