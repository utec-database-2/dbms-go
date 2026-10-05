package rtree

import (
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"github.com/dbms-go/v2/dbms/lib/storage"
)

// ridOf construye un RID que identifica la entrada i. En este branch un RID es
// {File, Page, Slot}: la identidad de la entrada va en el slot, que es lo único
// que los tests necesitan distinguir.
func ridOf(i int) storage.RID {
	return storage.RID{Slot: int32(i)}
}

// ids devuelve los identificadores de las entradas, ordenados.
func ids(entries []Entry) []int {
	out := make([]int, 0, len(entries))
	for _, e := range entries {
		out = append(out, int(e.RID.Slot))
	}
	sort.Ints(out)
	return out
}

func TestEuclideanDistance(t *testing.T) {

	a := Point{
		Lat: 0,
		Lon: 0,
	}

	b := Point{
		Lat: 3,
		Lon: 4,
	}

	d := Distance(a, b, Euclidean)

	if d != 5 {
		t.Errorf("Esperado 5, obtenido %f", d)
	}
}

func TestHaversineDistanceKnownValues(t *testing.T) {
	// Con R = 6371 km, un grado de latitud mide 6371·π/180 ≈ 111.195 km.
	d := Distance(Point{0, 0}, Point{1, 0}, Haversine)
	if math.Abs(d-111.195) > 0.01 {
		t.Errorf("1° de latitud = %f km, esperado ≈ 111.195", d)
	}

	if d := Distance(Point{-12.04, -77.03}, Point{-12.04, -77.03}, Haversine); d != 0 {
		t.Errorf("distancia de un punto a sí mismo = %f, esperado 0", d)
	}

	a, b := Point{-12.0432, -77.0282}, Point{-12.1060, -77.0600}
	if d1, d2 := Distance(a, b, Haversine), Distance(b, a, Haversine); math.Abs(d1-d2) > 1e-9 {
		t.Errorf("Haversine no es simétrica: %f vs %f", d1, d2)
	}
}

func TestSearchRadiusHaversine(t *testing.T) {
	center := Point{Lat: -12.0432, Lon: -77.0282}

	tr := New(4)
	tr.Insert(Entry{Point: center, RID: ridOf(1)})                                    // 0 km
	tr.Insert(Entry{Point: Point{center.Lat + 0.01, center.Lon}, RID: ridOf(2)})      // ≈ 1.11 km
	tr.Insert(Entry{Point: Point{center.Lat + 0.05, center.Lon}, RID: ridOf(3)})      // ≈ 5.56 km
	tr.Insert(Entry{Point: Point{center.Lat, center.Lon + 0.1}, RID: ridOf(4)})       // ≈ 10.9 km
	tr.Insert(Entry{Point: Point{center.Lat + 1.0, center.Lon + 1.0}, RID: ridOf(5)}) // ≈ 157 km

	cases := []struct {
		radiusKm float64
		want     []int
	}{
		{0.5, []int{1}},
		{2, []int{1, 2}},
		{6, []int{1, 2, 3}},
		{12, []int{1, 2, 3, 4}},
		{500, []int{1, 2, 3, 4, 5}},
	}

	for _, c := range cases {
		got := ids(tr.SearchRadius(center, c.radiusKm, Haversine))
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("radio %v km: obtuve %v, esperaba %v", c.radiusKm, got, c.want)
		}
	}

	if got := tr.SearchRadius(center, -1, Haversine); got != nil {
		t.Errorf("radio negativo debe dar nil, obtuve %v", got)
	}
}

func TestSearchOnEmptyTree(t *testing.T) {
	tr := New(4)

	if got := tr.SearchRadius(Point{0, 0}, 1e6, Haversine); len(got) != 0 {
		t.Errorf("árbol vacío (radio): obtuve %d resultados", len(got))
	}
	if got := tr.SearchRect(NewRect(Point{-90, -180}, Point{90, 180})); len(got) != 0 {
		t.Errorf("árbol vacío (rect): obtuve %d resultados", len(got))
	}
}

// validate comprueba las invariantes estructurales del R-Tree.
func validate(t *testing.T, tr *RTree, wantEntries int) {
	t.Helper()

	leafDepth := -1
	count := 0

	var walk func(n *Node, depth int, isRoot bool)
	walk = func(n *Node, depth int, isRoot bool) {
		var recomputed Rect
		size := len(n.Entries)
		if !n.Leaf {
			size = len(n.Children)
		}

		if size > tr.MaxEntries {
			t.Fatalf("nodo con %d elementos > MaxEntries=%d", size, tr.MaxEntries)
		}
		if !isRoot && size < tr.minEntries() {
			t.Fatalf("nodo no raíz con %d elementos < mínimo %d", size, tr.minEntries())
		}

		if n.Leaf {
			if len(n.Children) != 0 {
				t.Fatalf("hoja con hijos")
			}
			if leafDepth == -1 {
				leafDepth = depth
			} else if leafDepth != depth {
				t.Fatalf("hojas a distinta profundidad: %d y %d", leafDepth, depth)
			}
			count += len(n.Entries)
			for i, e := range n.Entries {
				if i == 0 {
					recomputed = PointRect(e.Point)
				} else {
					recomputed = recomputed.Union(PointRect(e.Point))
				}
			}
		} else {
			if len(n.Entries) != 0 {
				t.Fatalf("nodo interno con entradas")
			}
			if size == 0 {
				t.Fatalf("nodo interno sin hijos")
			}
			for i, c := range n.Children {
				walk(c, depth+1, false)
				if i == 0 {
					recomputed = c.box
				} else {
					recomputed = recomputed.Union(c.box)
				}
			}
		}

		if size > 0 && n.box != recomputed {
			t.Fatalf("MBR cacheado desactualizado: tiene %+v, debería ser %+v", n.box, recomputed)
		}
	}

	walk(tr.Root, 0, true)

	if count != wantEntries {
		t.Fatalf("el árbol contiene %d entradas, esperaba %d", count, wantEntries)
	}
}

// genPoints genera n puntos alrededor de Lima. Si clustered, usa solo unas
// pocas posiciones, lo que produce muchísimos puntos idénticos.
func genPoints(rng *rand.Rand, n int, clustered bool) []Point {
	pts := make([]Point, n)
	for i := range pts {
		if clustered {
			k := rng.Intn(15)
			pts[i] = Point{Lat: -12.0 + float64(k)*0.003, Lon: -77.0 + float64(k%4)*0.003}
			continue
		}
		pts[i] = Point{
			Lat: -12.0 + (rng.Float64()-0.5)*0.4,
			Lon: -77.0 + (rng.Float64()-0.5)*0.4,
		}
	}
	return pts
}

// TestSearchMatchesLinearScan es la prueba central: para muchas
// configuraciones, el R-Tree debe devolver exactamente lo mismo que un
// recorrido lineal (mismo conjunto de resultados), con ambas métricas.
func TestSearchMatchesLinearScan(t *testing.T) {
	for _, maxEntries := range []int{2, 3, 4, 16} {
		for _, n := range []int{0, 1, 5, 200, 3000} {
			for _, clustered := range []bool{false, true} {
				name := fmt.Sprintf("M=%d/n=%d/clustered=%v", maxEntries, n, clustered)
				t.Run(name, func(t *testing.T) {
					rng := rand.New(rand.NewSource(int64(maxEntries*100000 + n)))
					pts := genPoints(rng, n, clustered)

					tr := New(maxEntries)
					for i, p := range pts {
						tr.Insert(Entry{Point: p, RID: ridOf(i)})
					}
					validate(t, tr, n)

					centers := []Point{{Lat: -12.0, Lon: -77.0}, {Lat: 10, Lon: 10}}
					for i := 0; i < 20 && n > 0; i++ {
						centers = append(centers, pts[rng.Intn(n)])
					}

					type query struct {
						radius float64
						metric DistanceMetric
					}
					queries := []query{
						{0.3, Haversine}, {1, Haversine}, {5, Haversine}, {25, Haversine},
						{0.001, Euclidean}, {0.01, Euclidean}, {0.1, Euclidean},
					}

					for _, c := range centers {
						for _, q := range queries {
							var want []int
							for i, p := range pts {
								if Distance(c, p, q.metric) <= q.radius {
									want = append(want, i)
								}
							}
							sort.Ints(want)
							got := ids(tr.SearchRadius(c, q.radius, q.metric))
							if len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
								t.Fatalf("radio %v métrica %v centro %+v: R-Tree=%d resultados, lineal=%d",
									q.radius, q.metric, c, len(got), len(want))
							}
						}

						rect := NewRect(
							Point{Lat: c.Lat - 0.02, Lon: c.Lon - 0.05},
							Point{Lat: c.Lat + 0.03, Lon: c.Lon + 0.01},
						)
						var wantRect []int
						for i, p := range pts {
							if rect.Contains(p) {
								wantRect = append(wantRect, i)
							}
						}
						sort.Ints(wantRect)
						gotRect := ids(tr.SearchRect(rect))
						if len(gotRect) != len(wantRect) || (len(wantRect) > 0 && !reflect.DeepEqual(gotRect, wantRect)) {
							t.Fatalf("rect %+v: R-Tree=%d resultados, lineal=%d", rect, len(gotRect), len(wantRect))
						}
					}
				})
			}
		}
	}
}

// El MBR cacheado debe mantenerse correcto mientras se insertan puntos de a uno.
func TestBoxStaysValidAfterEveryInsert(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	pts := genPoints(rng, 400, false)

	tr := New(4)
	for i, p := range pts {
		tr.Insert(Entry{Point: p, RID: ridOf(i)})
		validate(t, tr, i+1)
	}
}

// Un círculo que contiene un polo no puede podarse por longitud: antes
// esto dividía por cos(lat)≈0.
func TestSearchRadiusNearPole(t *testing.T) {
	tr := New(4)
	tr.Insert(Entry{Point: Point{Lat: 89.9, Lon: 0}, RID: ridOf(1)})
	tr.Insert(Entry{Point: Point{Lat: 89.9, Lon: 90}, RID: ridOf(2)})
	tr.Insert(Entry{Point: Point{Lat: 89.9, Lon: 180}, RID: ridOf(3)})
	tr.Insert(Entry{Point: Point{Lat: 0, Lon: 0}, RID: ridOf(4)})

	got := ids(tr.SearchRadius(Point{Lat: 90, Lon: 0}, 50, Haversine))
	want := []int{1, 2, 3}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cerca del polo: obtuve %v, esperaba %v", got, want)
	}
}
