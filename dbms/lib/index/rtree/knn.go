package rtree

import (
	"container/heap"
	"math"
)

// Neighbor es un resultado de KNN: la entrada y su distancia al punto
// consultado (kilómetros con Haversine, grados con Euclidean).
type Neighbor struct {
	Entry    Entry
	Distance float64
}

// KNN devuelve las k entradas más cercanas a center, ordenadas de menor a
// mayor distancia. Si el árbol tiene menos de k entradas devuelve todas.
//
// Es una búsqueda best-first (Hjaltason & Samet): una cola de prioridad
// mezcla nodos, con prioridad igual a la distancia mínima posible de
// cualquier punto de su MBR, y entradas, con su distancia exacta. Siempre se
// expande lo más cercano; cuando lo que sale de la cola es una entrada, ya
// no puede existir un punto más cercano sin visitar, porque todo lo pendiente
// tiene una cota inferior mayor o igual. Por eso solo se leen las ramas que
// pueden contener alguno de los k vecinos.
func (t *RTree) KNN(center Point, k int, metric DistanceMetric) []Neighbor {
	if k <= 0 || t.Root == nil || (t.Root.Leaf && len(t.Root.Entries) == 0) {
		return nil
	}

	queue := &knnQueue{}
	seq := 0
	push := func(item knnItem) {
		item.seq = seq
		seq++
		heap.Push(queue, item)
	}

	push(knnItem{dist: minDist(center, t.Root.box, metric), node: t.Root})

	out := make([]Neighbor, 0, k)

	for queue.Len() > 0 && len(out) < k {
		item := heap.Pop(queue).(knnItem)

		if item.node == nil {
			out = append(out, Neighbor{Entry: item.entry, Distance: item.dist})
			continue
		}

		n := item.node

		if n.Leaf {
			for _, e := range n.Entries {
				push(knnItem{dist: Distance(center, e.Point, metric), entry: e})
			}
			continue
		}

		for _, child := range n.Children {
			push(knnItem{dist: minDist(center, child.box, metric), node: child})
		}
	}

	return out
}

// knnItem es un elemento de la cola: un nodo por explorar (node != nil) o
// una entrada ya resuelta.
type knnItem struct {
	dist  float64
	seq   int // desempate determinista: orden de inserción
	node  *Node
	entry Entry
}

type knnQueue []knnItem

func (q knnQueue) Len() int { return len(q) }

func (q knnQueue) Less(i, j int) bool {
	if q[i].dist != q[j].dist {
		return q[i].dist < q[j].dist
	}
	// A igual distancia, primero las entradas: se terminan antes.
	if (q[i].node == nil) != (q[j].node == nil) {
		return q[i].node == nil
	}
	return q[i].seq < q[j].seq
}

func (q knnQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }

func (q *knnQueue) Push(x any) { *q = append(*q, x.(knnItem)) }

func (q *knnQueue) Pop() any {
	old := *q
	n := len(old)
	item := old[n-1]
	*q = old[:n-1]
	return item
}

// 10. DISTANCIA MÍNIMA PUNTO-RECTÁNGULO
//
// minDist devuelve una cota inferior de la distancia entre p y cualquier punto
// dentro del rectángulo r, con la misma métrica que Distance. Es 0 si p está
// dentro de r. Que sea una cota inferior verdadera es lo que hace correcto a KNN.

func minDist(p Point, r Rect, metric DistanceMetric) float64 {
	switch metric {

	case Euclidean:
		dLat := axisGap(p.Lat, r.Min.Lat, r.Max.Lat)
		dLon := axisGap(p.Lon, r.Min.Lon, r.Max.Lon)

		return math.Sqrt(dLat*dLat + dLon*dLon)

	case Haversine:
		return minDistHaversine(p, r)
	}

	return 0
}

// axisGap es la distancia de v al intervalo [lo, hi] (0 si v está dentro).
func axisGap(v, lo, hi float64) float64 {
	if v < lo {
		return lo - v
	}
	if v > hi {
		return v - hi
	}
	return 0
}

func toRad(deg float64) float64 { return deg * math.Pi / 180 }

// minDistHaversine calcula la distancia geodésica mínima de p a un rectángulo
// lat/lon (km).
//
//   - Si la longitud de p cae dentro de la del rectángulo, lo más cercano es
//     subir o bajar por el meridiano: la distancia es solo la diferencia de latitud.
//   - Si cae fuera, el punto más cercano está sobre uno de los dos bordes
//     meridianos (la distancia a un punto no tiene mínimos locales
//     interiores, y a lo largo de un paralelo cos(Δlon) solo crece al alejarse
//     de la longitud de p), así que basta mirar ambos bordes.
//
// No contempla rectángulos que crucen el antimeridiano (±180°).
func minDistHaversine(p Point, r Rect) float64 {
	if p.Lon >= r.Min.Lon && p.Lon <= r.Max.Lon {
		return earthRadiusKm * toRad(axisGap(p.Lat, r.Min.Lat, r.Max.Lat))
	}

	d := math.Min(
		distToMeridianSegment(p, r.Min.Lon, r.Min.Lat, r.Max.Lat),
		distToMeridianSegment(p, r.Max.Lon, r.Min.Lat, r.Max.Lat),
	)

	// Margen de 1e-9 para que el redondeo de la trigonometría nunca vuelva la
	// cota inferior mayor que la distancia real.
	return d * (1 - 1e-9)
}

// distToMeridianSegment es la distancia geodésica mínima (km) de p al segmento
// del meridiano `lon` comprendido entre las latitudes lat1 y lat2 (lat1 <= lat2).
//
// Sobre el meridiano, el punto Q(φ) cumple P·Q = A·cosφ + B·sinφ, con
// A = cos(φp)·cos(Δλ) y B = sin(φp), y la distancia es mínima donde ese producto
// es máximo. Si A > 0 el máximo está en φ* = atan(B/A) (el "pie de la
// perpendicular"); si φ* cae dentro del segmento esa es la respuesta, y si no, o
// si A <= 0, el mínimo está en uno de los dos extremos.
func distToMeridianSegment(p Point, lon, lat1, lat2 float64) float64 {
	best := math.Min(
		Distance(p, Point{Lat: lat1, Lon: lon}, Haversine),
		Distance(p, Point{Lat: lat2, Lon: lon}, Haversine),
	)

	dl := toRad(normalizeLonDiff(lon - p.Lon))

	if cosDl := math.Cos(dl); cosDl > 0 {
		foot := math.Atan(math.Tan(toRad(p.Lat))/cosDl) * 180 / math.Pi

		if foot > lat1 && foot < lat2 {
			x := math.Cos(toRad(p.Lat)) * math.Abs(math.Sin(dl))
			if x > 1 {
				x = 1
			}

			best = math.Min(best, earthRadiusKm*math.Asin(x))
		}
	}

	return best
}

// normalizeLonDiff lleva una diferencia de longitudes al rango [-180, 180).
func normalizeLonDiff(d float64) float64 {
	d = math.Mod(d+180, 360)
	if d < 0 {
		d += 360
	}
	return d - 180
}
