package rtree

import (
	"math"
	"sort"

	"github.com/dbms-go/v2/dbms/lib/storage"
)

// 1. PUNTOS Y RECTÁNGULOS

// Point representa una coordenada geográfica.
type Point struct {
	Lat float64
	Lon float64
}

// Rect representa un rectángulo mínimo envolvente (MBR).
type Rect struct {
	Min Point
	Max Point
}

// NewRect crea un rectángulo.
func NewRect(a, b Point) Rect {
	return Rect{
		Min: Point{
			Lat: math.Min(a.Lat, b.Lat),
			Lon: math.Min(a.Lon, b.Lon),
		},
		Max: Point{
			Lat: math.Max(a.Lat, b.Lat),
			Lon: math.Max(a.Lon, b.Lon),
		},
	}
}

// PointRect convierte un punto en un MBR degenerado.
func PointRect(p Point) Rect {
	return Rect{
		Min: p,
		Max: p,
	}
}

func (r Rect) Area() float64 {
	return (r.Max.Lat - r.Min.Lat) *
		(r.Max.Lon - r.Min.Lon)
}

// margin es la mitad del perímetro. Sirve de desempate cuando el área es 0
// (puntos alineados), donde Area() no distingue entre rectángulos distintos.
func (r Rect) margin() float64 {
	return (r.Max.Lat - r.Min.Lat) +
		(r.Max.Lon - r.Min.Lon)
}

func (r Rect) Union(o Rect) Rect {
	return Rect{
		Min: Point{
			Lat: math.Min(r.Min.Lat, o.Min.Lat),
			Lon: math.Min(r.Min.Lon, o.Min.Lon),
		},
		Max: Point{
			Lat: math.Max(r.Max.Lat, o.Max.Lat),
			Lon: math.Max(r.Max.Lon, o.Max.Lon),
		},
	}
}

// Cuánto debe crecer el rectángulo para incluir otro rectángulo.
func (r Rect) Enlargement(o Rect) float64 {
	return r.Union(o).Area() - r.Area()
}

func (r Rect) Intersects(o Rect) bool {
	return !(r.Max.Lat < o.Min.Lat ||
		r.Min.Lat > o.Max.Lat ||
		r.Max.Lon < o.Min.Lon ||
		r.Min.Lon > o.Max.Lon)
}

// overlapArea es el área de la intersección entre dos rectángulos (0 si no se tocan).
func (r Rect) overlapArea(o Rect) float64 {
	dLat := math.Min(r.Max.Lat, o.Max.Lat) - math.Max(r.Min.Lat, o.Min.Lat)
	dLon := math.Min(r.Max.Lon, o.Max.Lon) - math.Max(r.Min.Lon, o.Min.Lon)
	if dLat <= 0 || dLon <= 0 {
		return 0
	}
	return dLat * dLon
}

func (r Rect) Contains(p Point) bool {
	return p.Lat >= r.Min.Lat &&
		p.Lat <= r.Max.Lat &&
		p.Lon >= r.Min.Lon &&
		p.Lon <= r.Max.Lon
}

// 2. ENTRADA DEL R-TREE
type Entry struct {
	Point Point
	RID   storage.RID
}

// 3. NODO
type Node struct {
	Leaf     bool
	Entries  []Entry
	Children []*Node

	// box es el MBR del nodo, cacheado. Se actualiza en Insert y en los
	// splits, así consultar un nodo cuesta O(1) en vez de recorrer todo su
	// subárbol (que era lo que hacía MBR() antes y anulaba la ventaja del árbol).
	box Rect
}

// MBR = Minimum Bounding Rectangle (valor cacheado, O(1)).
func (n *Node) MBR() Rect {
	return n.box
}

// recomputeBox recalcula box a partir del contenido directo del nodo
// (O(MaxEntries): para un nodo interno usa el box ya cacheado de cada hijo).
func (n *Node) recomputeBox() {
	if n.Leaf {
		if len(n.Entries) == 0 {
			n.box = Rect{}
			return
		}

		r := PointRect(n.Entries[0].Point)

		for _, e := range n.Entries[1:] {
			r = r.Union(PointRect(e.Point))
		}

		n.box = r
		return
	}

	if len(n.Children) == 0 {
		n.box = Rect{}
		return
	}

	r := n.Children[0].box

	for _, child := range n.Children[1:] {
		r = r.Union(child.box)
	}

	n.box = r
}

// 4. R-TREE
type RTree struct {
	Root       *Node
	MaxEntries int
}

func New(maxEntries int) *RTree {
	if maxEntries < 2 {
		maxEntries = 4
	}

	return &RTree{
		Root: &Node{
			Leaf: true,
		},
		MaxEntries: maxEntries,
	}
}

// minEntries es el mínimo de entradas por grupo al dividir un nodo (~40% de
// MaxEntries, como en el R*-tree). Nunca supera la mitad de MaxEntries+1.
func (t *RTree) minEntries() int {
	m := (2*t.MaxEntries + 4) / 5
	if m < 1 {
		m = 1
	}
	return m
}

// 5. INSERCIÓN
func (t *RTree) Insert(e Entry) {
	split := t.insert(t.Root, e)

	// Si la raíz se dividió, creamos una nueva raíz.
	if split != nil {
		oldRoot := t.Root

		t.Root = &Node{
			Leaf: false,
			Children: []*Node{
				oldRoot,
				split,
			},
		}
		t.Root.recomputeBox()
	}
}

func (t *RTree) insert(n *Node, e Entry) *Node {

	// Caso: estamos en una hoja
	if n.Leaf {
		n.Entries = append(n.Entries, e)

		if len(n.Entries) > t.MaxEntries {
			return t.splitNode(n)
		}

		if len(n.Entries) == 1 {
			n.box = PointRect(e.Point)
		} else {
			n.box = n.box.Union(PointRect(e.Point))
		}

		return nil
	}

	// Caso: nodo interno
	index := t.chooseChild(n, e.Point)

	split := t.insert(n.Children[index], e)

	if split != nil {
		n.Children = append(n.Children, split)
	}

	if len(n.Children) > t.MaxEntries {
		return t.splitNode(n)
	}

	// El hijo por el que bajamos pudo crecer (o achicarse si se dividió).
	n.recomputeBox()

	return nil
}

// Elegimos el hijo cuyo MBR necesita crecer menos. Si empatan, el de menor
// área, y si siguen empatados, el que tenga menos contenido.
func (t *RTree) chooseChild(n *Node, p Point) int {
	pointRect := PointRect(p)

	best := 0
	bestGrowth := math.Inf(1)
	bestArea := math.Inf(1)

	for i, child := range n.Children {
		growth := child.box.Enlargement(pointRect)
		area := child.box.Area()

		if growth < bestGrowth ||
			(growth == bestGrowth && area < bestArea) {
			bestGrowth = growth
			bestArea = area
			best = i
		}
	}

	return best
}

// splitNode divide un nodo desbordado en dos. Devuelve el nodo nuevo; n queda
// con el primer grupo. Ambos tienen su box recalculado.
func (t *RTree) splitNode(n *Node) *Node {
	if n.Leaf {
		rects := make([]Rect, len(n.Entries))
		for i, e := range n.Entries {
			rects[i] = PointRect(e.Point)
		}

		order, k := chooseSplit(rects, t.minEntries())

		sorted := make([]Entry, len(n.Entries))
		for i, o := range order {
			sorted[i] = n.Entries[o]
		}

		newNode := &Node{
			Leaf:    true,
			Entries: append([]Entry(nil), sorted[k:]...),
		}
		n.Entries = append([]Entry(nil), sorted[:k]...)

		n.recomputeBox()
		newNode.recomputeBox()

		return newNode
	}

	rects := make([]Rect, len(n.Children))
	for i, c := range n.Children {
		rects[i] = c.box
	}

	order, k := chooseSplit(rects, t.minEntries())

	sorted := make([]*Node, len(n.Children))
	for i, o := range order {
		sorted[i] = n.Children[o]
	}

	newNode := &Node{
		Leaf:     false,
		Children: append([]*Node(nil), sorted[k:]...),
	}
	n.Children = append([]*Node(nil), sorted[:k]...)

	n.recomputeBox()
	newNode.recomputeBox()

	return newNode
}

// chooseSplit es una versión simplificada del split del R*-tree.
//
// Recibe el rectángulo de cada elemento del nodo desbordado y devuelve un
// orden de esos elementos y un punto de corte k: los primeros k forman un
// grupo y el resto el otro. Pasos:
//  1. Para cada eje (lat, lon) se ordenan los elementos y se prueban todos los
//     cortes válidos; se elige el eje cuyos cortes tienen menor suma de
//     márgenes (rectángulos más "cuadrados" y compactos).
//  2. En ese eje se elige el corte con menor solapamiento entre los dos
//     grupos; si empatan, menor área total; si siguen empatados, el más
//     cercano al centro.
//
// Cortar siempre por longitud a la mitad (lo que había antes) genera
// rectángulos largos y solapados en latitud, y el árbol casi no puede descartar ramas.
func chooseSplit(rects []Rect, minFill int) (order []int, splitAt int) {
	n := len(rects)

	if minFill > n/2 {
		minFill = n / 2
	}
	if minFill < 1 {
		minFill = 1
	}

	type axisResult struct {
		order     []int
		prefix    []Rect // prefix[i] = unión de order[0..i]
		suffix    []Rect // suffix[i] = unión de order[i..n-1]
		marginSum float64
	}

	evaluate := func(less func(a, b Rect) bool) axisResult {
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}

		sort.SliceStable(idx, func(i, j int) bool {
			return less(rects[idx[i]], rects[idx[j]])
		})

		prefix := make([]Rect, n)
		suffix := make([]Rect, n)

		prefix[0] = rects[idx[0]]
		for i := 1; i < n; i++ {
			prefix[i] = prefix[i-1].Union(rects[idx[i]])
		}

		suffix[n-1] = rects[idx[n-1]]
		for i := n - 2; i >= 0; i-- {
			suffix[i] = suffix[i+1].Union(rects[idx[i]])
		}

		sum := 0.0
		for k := minFill; k <= n-minFill; k++ {
			sum += prefix[k-1].margin() + suffix[k].margin()
		}

		return axisResult{idx, prefix, suffix, sum}
	}

	byLat := evaluate(func(a, b Rect) bool {
		if a.Min.Lat != b.Min.Lat {
			return a.Min.Lat < b.Min.Lat
		}
		return a.Max.Lat < b.Max.Lat
	})
	byLon := evaluate(func(a, b Rect) bool {
		if a.Min.Lon != b.Min.Lon {
			return a.Min.Lon < b.Min.Lon
		}
		return a.Max.Lon < b.Max.Lon
	})

	best := byLat
	if byLon.marginSum < byLat.marginSum {
		best = byLon
	}

	bestK := -1
	bestOverlap := math.Inf(1)
	bestArea := math.Inf(1)
	bestCenter := math.Inf(1)

	for k := minFill; k <= n-minFill; k++ {
		left := best.prefix[k-1]
		right := best.suffix[k]

		overlap := left.overlapArea(right)
		area := left.Area() + right.Area()
		center := math.Abs(float64(k) - float64(n)/2)

		if overlap < bestOverlap ||
			(overlap == bestOverlap && area < bestArea) ||
			(overlap == bestOverlap && area == bestArea && center < bestCenter) {
			bestK = k
			bestOverlap = overlap
			bestArea = area
			bestCenter = center
		}
	}

	return best.order, bestK
}

// 7. MÉTRICAS DE DISTANCIA
// DistanceMetric representa la métrica usada para calcular distancias.
type DistanceMetric int

const (
	Euclidean DistanceMetric = iota
	Haversine
)

const earthRadiusKm = 6371.0

// Distance calcula la distancia entre dos puntos.
func Distance(a, b Point, metric DistanceMetric) float64 {

	switch metric {

	case Euclidean:
		dLat := a.Lat - b.Lat
		dLon := a.Lon - b.Lon

		return math.Sqrt(
			dLat*dLat + dLon*dLon,
		)

	case Haversine:
		lat1 := a.Lat * math.Pi / 180
		lat2 := b.Lat * math.Pi / 180

		dLat := (b.Lat - a.Lat) * math.Pi / 180
		dLon := (b.Lon - a.Lon) * math.Pi / 180

		h := math.Sin(dLat/2)*math.Sin(dLat/2) +
			math.Cos(lat1)*
				math.Cos(lat2)*
				math.Sin(dLon/2)*
				math.Sin(dLon/2)

		c := 2 * math.Atan2(
			math.Sqrt(h),
			math.Sqrt(1-h),
		)

		return earthRadiusKm * c
	}

	return 0
}

// 8. CONSULTA POR RECTÁNGULO
func (t *RTree) SearchRect(query Rect) []Entry {
	var result []Entry

	t.searchRect(t.Root, query, &result)

	return result
}

func (t *RTree) searchRect(
	n *Node,
	query Rect,
	result *[]Entry,
) {
	// Nodo vacío (solo puede pasar con la raíz de un árbol sin datos).
	if n.Leaf && len(n.Entries) == 0 {
		return
	}

	if !n.box.Intersects(query) {
		return
	}

	// Hoja
	if n.Leaf {
		for _, e := range n.Entries {
			if query.Contains(e.Point) {
				*result = append(*result, e)
			}
		}

		return
	}

	// Nodo interno
	for _, child := range n.Children {
		t.searchRect(child, query, result)
	}
}

// 9. CONSULTA POR RADIO
// radius:
//   - Haversine -> kilómetros
//   - Euclidean -> unidades de coordenadas

func (t *RTree) SearchRadius(
	center Point,
	radius float64,
	metric DistanceMetric,
) []Entry {

	if radius < 0 {
		return nil
	}

	var query Rect

	switch metric {

	case Euclidean:

		query = NewRect(
			Point{
				Lat: center.Lat - radius,
				Lon: center.Lon - radius,
			},
			Point{
				Lat: center.Lat + radius,
				Lon: center.Lon + radius,
			},
		)

	case Haversine:

		// Rectángulo (en grados) que contiene el círculo de búsqueda; solo
		// sirve para descartar ramas, la distancia exacta se verifica en la hoja.
		//
		// delta es el radio angular. La extensión máxima en longitud de un
		// círculo esférico es asin(sin(delta)/cos(lat)); si el círculo
		// alcanza un polo (o es enorme) no hay límite en longitud.
		// No contempla el cruce del antimeridiano (±180°).

		delta := radius / earthRadiusKm

		dLat := math.Inf(1)
		dLon := math.Inf(1)

		if delta < math.Pi/2 {
			dLat = delta * 180 / math.Pi

			if cosLat := math.Cos(center.Lat * math.Pi / 180); cosLat > 0 {
				if s := math.Sin(delta) / cosLat; s < 1 {
					dLon = math.Asin(s) * 180 / math.Pi
				}
			}
		}

		query = NewRect(
			Point{
				Lat: center.Lat - dLat,
				Lon: center.Lon - dLon,
			},
			Point{
				Lat: center.Lat + dLat,
				Lon: center.Lon + dLon,
			},
		)
	}

	var result []Entry

	t.searchRadius(
		t.Root,
		center,
		radius,
		metric,
		query,
		&result,
	)

	return result
}

func (t *RTree) searchRadius(
	n *Node,
	center Point,
	radius float64,
	metric DistanceMetric,
	query Rect,
	result *[]Entry,
) {
	// Nodo vacío (solo puede pasar con la raíz de un árbol sin datos).
	if n.Leaf && len(n.Entries) == 0 {
		return
	}

	// Si el MBR no tiene relación con la zona consultada,
	// descartamos todo el nodo.
	if !n.box.Intersects(query) {
		return
	}

	// Hoja
	if n.Leaf {

		for _, e := range n.Entries {

			d := Distance(
				center,
				e.Point,
				metric,
			)

			if d <= radius {
				*result = append(*result, e)
			}
		}

		return
	}

	// Nodo interno
	for _, child := range n.Children {

		t.searchRadius(
			child,
			center,
			radius,
			metric,
			query,
			result,
		)
	}
}
