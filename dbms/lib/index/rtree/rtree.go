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