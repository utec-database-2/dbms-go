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
}

// MBR = Minimum Bounding Rectangle
func (n *Node) MBR() Rect {
	if n.Leaf {
		if len(n.Entries) == 0 {
			return Rect{}
		}

		r := PointRect(n.Entries[0].Point)

		for _, e := range n.Entries[1:] {
			r = r.Union(PointRect(e.Point))
		}

		return r
	}

	if len(n.Children) == 0 {
		return Rect{}
	}

	r := n.Children[0].MBR()

	for _, child := range n.Children[1:] {
		r = r.Union(child.MBR())
	}

	return r
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
	}
}

func (t *RTree) insert(n *Node, e Entry) *Node {

	// Caso: estamos en una hoja
	if n.Leaf {
		n.Entries = append(n.Entries, e)

		if len(n.Entries) > t.MaxEntries {
			return t.splitNode(n)
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

	return nil
}

// Elegimos el hijo cuyo MBR necesita crecer menos.
func (t *RTree) chooseChild(n *Node, p Point) int {
	pointRect := PointRect(p)

	best := 0
	bestGrowth := math.Inf(1)

	for i, child := range n.Children {
		growth := child.MBR().Enlargement(pointRect)

		if growth < bestGrowth {
			bestGrowth = growth
			best = i
		}
	}

	return best
}

func (t *RTree) splitNode(n *Node) *Node {
	// Nodo hoja

	if n.Leaf {

		sort.Slice(n.Entries, func(i, j int) bool {
			return n.Entries[i].Point.Lon <
				n.Entries[j].Point.Lon
		})

		middle := len(n.Entries) / 2

		newNode := &Node{
			Leaf: true,
			Entries: append(
				[]Entry(nil),
				n.Entries[middle:]...,
			),
		}

		n.Entries = n.Entries[:middle]

		return newNode
	}

	sort.Slice(n.Children, func(i, j int) bool {
		return n.Children[i].MBR().Min.Lon <
			n.Children[j].MBR().Min.Lon
	})

	middle := len(n.Children) / 2

	newNode := &Node{
		Leaf: false,
		Children: append(
			[]*Node(nil),
			n.Children[middle:]...,
		),
	}

	n.Children = n.Children[:middle]
	return newNode
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
	if !n.MBR().Intersects(query) {
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

		// Convertimos aproximadamente el radio
		// de kilómetros a grados.

		dLat := radius / earthRadiusKm * 180 / math.Pi

		cosLat := math.Cos(center.Lat * math.Pi / 180)

		dLon := radius / (earthRadiusKm * cosLat)
		dLon = dLon * 180 / math.Pi

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

	// Si el MBR no tiene relación con la zona consultada,
	// descartamos todo el nodo.
	if !n.MBR().Intersects(query) {
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