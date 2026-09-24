package rtree

import (
	"math"
	"sort"

	"github.com/dbms-go/v2/dbms/lib/storage"
)

const EarthRadiusKm = 6371.0088

// DistanceMetric representa la métrica usada para calcular distancias.
type DistanceMetric int

const (
	Euclidean DistanceMetric = iota
	Haversine
)

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

// PointRect convierte un punto en un MBR degenerado.
func PointRect(p Point) Rect {
	return Rect{
		Min: p,
		Max: p,
	}
}

// NewRect crea un rectángulo.
func NewRect(min, max Point) Rect {
	return Rect{
		Min: min,
		Max: max,
	}
}

// Normalize asegura que Min <= Max.
func (r Rect) Normalize() Rect {
	return Rect{
		Min: Point{
			Lat: math.Min(r.Min.Lat, r.Max.Lat),
			Lon: math.Min(r.Min.Lon, r.Max.Lon),
		},
		Max: Point{
			Lat: math.Max(r.Min.Lat, r.Max.Lat),
			Lon: math.Max(r.Min.Lon, r.Max.Lon),
		},
	}
}

// Area devuelve el área del rectángulo en unidades de coordenadas.
func (r Rect) Area() float64 {
	r = r.Normalize()

	return (r.Max.Lat - r.Min.Lat) *
		(r.Max.Lon - r.Min.Lon)
}

// Union devuelve el MBR que contiene ambos rectángulos.
func (r Rect) Union(other Rect) Rect {
	r = r.Normalize()
	other = other.Normalize()

	return Rect{
		Min: Point{
			Lat: math.Min(
				r.Min.Lat,
				other.Min.Lat,
			),
			Lon: math.Min(
				r.Min.Lon,
				other.Min.Lon,
			),
		},
		Max: Point{
			Lat: math.Max(
				r.Max.Lat,
				other.Max.Lat,
			),
			Lon: math.Max(
				r.Max.Lon,
				other.Max.Lon,
			),
		},
	}
}

// Enlargement calcula cuánto aumenta el área al incluir otro rectángulo.
func (r Rect) Enlargement(other Rect) float64 {
	return r.Union(other).Area() - r.Area()
}

// Intersects indica si dos rectángulos se intersectan.
func (r Rect) Intersects(other Rect) bool {
	r = r.Normalize()
	other = other.Normalize()

	return r.Min.Lat <= other.Max.Lat &&
		r.Max.Lat >= other.Min.Lat &&
		r.Min.Lon <= other.Max.Lon &&
		r.Max.Lon >= other.Min.Lon
}

// ContainsPoint indica si el punto está dentro del rectángulo.
func (r Rect) ContainsPoint(p Point) bool {
	r = r.Normalize()
	return p.Lat >= r.Min.Lat &&
		p.Lat <= r.Max.Lat &&
		p.Lon >= r.Min.Lon &&
		p.Lon <= r.Max.Lon
}

// Entry es una entrada del R-Tree.
// El RID permite recuperar posteriormente el registro real desde el Heap File.
type Entry struct {
	Point Point
	RID   storage.RID
}

// node representa un nodo del R-Tree.
type node struct {
	leaf bool
	rect Rect
	entries []Entry
	children []*node
	parent *node
}

// RTree representa el índice espacial.
type RTree struct {
	root *node

	maxEntries int
	minEntries int
}

// New crea un R-Tree.
// maxEntries indica cuántas entradas puede contener un nodo antes de dividirse.
func New(maxEntries int) *RTree {
	if maxEntries < 4 {
		maxEntries = 4
	}

	minEntries := maxEntries / 2

	return &RTree{
		root: &node{
			leaf: true,
		},
		maxEntries: maxEntries,
		minEntries: minEntries,
	}
}

func NewDefault() *RTree {
	return New(8)
}

// Insert agrega un punto al R-Tree.
func (t *RTree) Insert(entry Entry) {

	rect := PointRect(entry.Point)

	leaf := t.chooseLeaf(
		t.root,
		rect,
	)

	leaf.entries = append(
		leaf.entries,
		entry,
	)

	t.expandToParent(leaf)

	if len(leaf.entries) > t.maxEntries {
		t.splitLeaf(leaf)
	}
}

// SearchRect busca todos los puntos cuyo MBR
// intersecta el rectángulo indicado.
func (t *RTree) SearchRect(query Rect) []Entry {

	query = query.Normalize()

	out := make([]Entry, 0)

	t.searchRect(
		t.root,
		query,
		&out,
	)

	return out
}

func (t *RTree) SearchRadius(
	center Point,
	radius float64,
	metric DistanceMetric,
) []Entry {

	if radius < 0 {
		return nil
	}

	bbox := radiusBoundingBox(
		center,
		radius,
		metric,
	)

	candidates := t.SearchRect(bbox)

	out := make(
		[]Entry,
		0,
		len(candidates),
	)

	for _, entry := range candidates {

		distance := Distance(
			center,
			entry.Point,
			metric,
		)

		if distance <= radius+1e-12 {
			out = append(
				out,
				entry,
			)
		}
	}

	// Los dejamos ordenados desde el más cercano
	// hasta el más lejano.
	sort.SliceStable(
		out,
		func(i, j int) bool {

			di := Distance(
				center,
				out[i].Point,
				metric,
			)

			dj := Distance(
				center,
				out[j].Point,
				metric,
			)

			if di == dj {

				if out[i].RID.PageID ==
					out[j].RID.PageID {

					return out[i].RID.SlotID <
						out[j].RID.SlotID
				}

				return out[i].RID.PageID <
					out[j].RID.PageID
			}

			return di < dj
		},
	)

	return out
}

// Distance calcula la distancia entre dos puntos.
func Distance(
	a Point,
	b Point,
	metric DistanceMetric,
) float64 {

	switch metric {

	case Euclidean:

		return math.Hypot(
			a.Lat-b.Lat,
			a.Lon-b.Lon,
		)

	case Haversine:

		lat1 := degToRad(a.Lat)
		lat2 := degToRad(b.Lat)

		dLat := degToRad(
			b.Lat - a.Lat,
		)

		dLon := degToRad(
			b.Lon - a.Lon,
		)

		h := math.Sin(dLat/2)*
			math.Sin(dLat/2) +
			math.Cos(lat1)*
				math.Cos(lat2)*
				math.Sin(dLon/2)*
				math.Sin(dLon/2)

		// Protección contra pequeños errores numéricos.
		h = math.Min(
			1,
			math.Max(0, h),
		)

		return 2 *
			EarthRadiusKm *
			math.Asin(
				math.Sqrt(h),
			)

	default:
		return math.NaN()
	}
}

func radiusBoundingBox(
	center Point,
	radius float64,
	metric DistanceMetric,
) Rect {

	switch metric {

	case Euclidean:

		return NewRect(
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

		// Aproximación local de grados a kilómetros.
		latDelta := radius / 111.32

		cosLat := math.Cos(
			degToRad(center.Lat),
		)

		if math.Abs(cosLat) < 1e-12 {
			cosLat = 1e-12
		}

		lonDelta :=
			radius /
				(111.32 * math.Abs(cosLat))

		return NewRect(
			Point{
				Lat: center.Lat - latDelta,
				Lon: center.Lon - lonDelta,
			},
			Point{
				Lat: center.Lat + latDelta,
				Lon: center.Lon + lonDelta,
			},
		)

	default:

		return PointRect(center)
	}
}

func degToRad(v float64) float64 {
	return v * math.Pi / 180
}
