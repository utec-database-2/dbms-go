package rtree

import (
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"sort"
	"testing"
)

func square(x0, y0, x1, y1 float64) Polygon {
	return Polygon{{Lat: x0, Lon: y0}, {Lat: x1, Lon: y0}, {Lat: x1, Lon: y1}, {Lat: x0, Lon: y1}}
}

func TestPolygonContainsSquare(t *testing.T) {
	sq := square(0, 0, 10, 10)

	cases := []struct {
		p    Point
		want bool
		name string
	}{
		{Point{5, 5}, true, "centro"},
		{Point{0.001, 9.999}, true, "cerca de una esquina, adentro"},
		{Point{-0.001, 5}, false, "justo afuera a la izquierda"},
		{Point{10.001, 5}, false, "justo afuera a la derecha"},
		{Point{5, 10.001}, false, "arriba"},
		{Point{20, 20}, false, "lejos"},
		{Point{0, 0}, true, "vértice"},
		{Point{10, 10}, true, "vértice opuesto"},
		{Point{0, 5}, true, "borde izquierdo"},
		{Point{5, 10}, true, "borde superior"},
		{Point{10, 3}, true, "borde derecho"},
	}
	for _, c := range cases {
		if got := sq.Contains(c.p); got != c.want {
			t.Errorf("%s %+v: Contains = %v, esperaba %v", c.name, c.p, got, c.want)
		}
	}
}

func TestPolygonContainsConcaveL(t *testing.T) {
	l := Polygon{{0, 0}, {10, 0}, {10, 4}, {4, 4}, {4, 10}, {0, 10}}

	cases := []struct {
		p    Point
		want bool
	}{
		{Point{2, 2}, true},
		{Point{8, 2}, true},
		{Point{2, 8}, true},
		{Point{8, 8}, false},
		{Point{6, 6}, false},
		{Point{4, 4}, true},
		{Point{5, 4}, true},
	}
	for _, c := range cases {
		if got := l.Contains(c.p); got != c.want {
			t.Errorf("L %+v: Contains = %v, esperaba %v", c.p, got, c.want)
		}
	}
}

func TestPolygonOrientationAndClosingVertexDoNotMatter(t *testing.T) {
	ccw := square(0, 0, 10, 10)
	cw := Polygon{ccw[3], ccw[2], ccw[1], ccw[0]}
	closed := append(append(Polygon(nil), ccw...), ccw[0])

	for _, p := range []Point{{5, 5}, {11, 5}, {0, 0}, {5, 0}, {-1, -1}} {
		a, b := ccw.Contains(p), cw.Contains(p)
		if a != b {
			t.Errorf("orientación: %+v da %v (antihorario) y %v (horario)", p, a, b)
		}
		if got, want := closed.Contains(p), a; got != want {
			t.Errorf("vértice de cierre repetido: %+v da %v, esperaba %v", p, got, want)
		}
	}

	tr := New(4)
	tr.Insert(Entry{Point: Point{5, 5}, RID: ridOf(1)})
	tr.Insert(Entry{Point: Point{50, 50}, RID: ridOf(2)})

	for name, pg := range map[string]Polygon{"antihorario": ccw, "horario": cw, "cerrado": closed} {
		got, err := tr.SearchPolygon(pg)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(ids(got), []int{1}) {
			t.Errorf("%s: obtuve %v, esperaba [1]", name, ids(got))
		}
	}
}

// Regla par-impar: en un moño (polígono que se cruza a sí mismo) las dos
// alas cuentan como interior y el cruce no.
func TestPolygonSelfIntersectingBowTie(t *testing.T) {
	bow := Polygon{{0, 0}, {10, 10}, {10, 0}, {0, 10}}

	for _, in := range []Point{{1, 5}, {9, 5}} {
		if !bow.Contains(in) {
			t.Errorf("%+v está en un ala del moño y debe contar como interior", in)
		}
	}
	for _, out := range []Point{{5, 1}, {5, 9}, {20, 5}, {-1, 5}} {
		if bow.Contains(out) {
			t.Errorf("%+v está fuera del moño", out)
		}
	}
}

func TestPolygonValidation(t *testing.T) {
	tr := New(4)
	tr.Insert(Entry{Point: Point{1, 1}, RID: ridOf(1)})

	bad := map[string]Polygon{
		"vacío":             {},
		"un vértice":        {{1, 1}},
		"dos vértices":      {{0, 0}, {1, 1}},
		"tres con cierre":   {{0, 0}, {5, 5}, {0, 0}},
		"NaN":               {{0, 0}, {math.NaN(), 1}, {2, 0}},
		"infinito":          {{0, 0}, {math.Inf(1), 1}, {2, 0}},
		"longitud infinita": {{0, 0}, {1, math.Inf(-1)}, {2, 0}},
		"nil":               nil,
	}

	for name, pg := range bad {
		if _, err := tr.SearchPolygon(pg); err != ErrInvalidPolygon {
			t.Errorf("%s: err = %v, esperaba ErrInvalidPolygon", name, err)
		}
	}

	if _, err := tr.SearchPolygon(Polygon{{0, 0}, {4, 0}, {0, 4}}); err != nil {
		t.Errorf("un triángulo válido no debe fallar: %v", err)
	}
}

func TestSearchPolygonOnEmptyTree(t *testing.T) {
	got, err := New(4).SearchPolygon(square(0, 0, 10, 10))
	if err != nil || len(got) != 0 {
		t.Fatalf("árbol vacío: got=%v err=%v", got, err)
	}
}

func regularPolygon(rng *rand.Rand, cx, cy, radius float64, n int) Polygon {
	angles := make([]float64, n)
	for i := range angles {
		angles[i] = rng.Float64() * 2 * math.Pi
	}
	sort.Float64s(angles)
	pg := make(Polygon, n)
	for i, a := range angles {
		pg[i] = Point{Lat: cx + radius*math.Cos(a), Lon: cy + radius*math.Sin(a)}
	}
	return pg
}

func starPolygon(rng *rand.Rand, cx, cy, radius float64, spikes int) Polygon {
	pg := make(Polygon, 0, spikes*2)
	for i := 0; i < spikes*2; i++ {
		a := float64(i) * math.Pi / float64(spikes)
		r := radius
		if i%2 == 1 {
			r = radius * (0.2 + 0.4*rng.Float64())
		}
		pg = append(pg, Point{Lat: cx + r*math.Cos(a), Lon: cy + r*math.Sin(a)})
	}
	return pg
}

func radialPolygon(rng *rand.Rand, cx, cy, radius float64, n int) Polygon {
	angles := make([]float64, n)
	for i := range angles {
		angles[i] = rng.Float64() * 2 * math.Pi
	}
	sort.Float64s(angles)
	pg := make(Polygon, n)
	for i, a := range angles {
		r := radius * (0.25 + 0.75*rng.Float64())
		pg[i] = Point{Lat: cx + r*math.Cos(a), Lon: cy + r*math.Sin(a)}
	}
	return pg
}

func scribblePolygon(rng *rand.Rand, cx, cy, radius float64, n int) Polygon {
	pg := make(Polygon, n)
	for i := range pg {
		pg[i] = Point{Lat: cx + (rng.Float64()-0.5)*2*radius, Lon: cy + (rng.Float64()-0.5)*2*radius}
	}
	return pg
}

// SearchPolygon debe devolver exactamente los puntos para los que Contains
// da verdadero, sea cual sea la forma del polígono o la del árbol. Los
// vértices y los puntos medios de las aristas se agregan como datos para
// ejercitar el borde.
func TestSearchPolygonMatchesBruteForce(t *testing.T) {
	for _, maxEntries := range []int{2, 4, 16} {
		for _, clustered := range []bool{false, true} {
			name := fmt.Sprintf("M=%d/clustered=%v", maxEntries, clustered)
			t.Run(name, func(t *testing.T) {
				rng := rand.New(rand.NewSource(int64(500 + maxEntries)))

				shapes := []struct {
					name string
					make func() Polygon
				}{
					{"triángulo", func() Polygon { return regularPolygon(rng, -12.0, -77.0, 0.08, 3) }},
					{"convexo", func() Polygon { return regularPolygon(rng, -12.0, -77.0, 0.1, 9) }},
					{"estrella", func() Polygon { return starPolygon(rng, -12.0, -77.0, 0.1, 7) }},
					{"radial cóncavo", func() Polygon { return radialPolygon(rng, -12.0, -77.0, 0.1, 25) }},
					{"auto-intersectado", func() Polygon { return scribblePolygon(rng, -12.0, -77.0, 0.1, 8) }},
					{"muy chico", func() Polygon { return regularPolygon(rng, -12.0, -77.0, 0.003, 5) }},
					{"enorme", func() Polygon { return regularPolygon(rng, -12.0, -77.0, 5, 6) }},
				}

				for _, shape := range shapes {
					for rep := 0; rep < 6; rep++ {
						pg := shape.make()

						pts := genPoints(rng, 1500, clustered)
						pts = append(pts, pg...)
						for i := range pg {
							a, b := pg[i], pg[(i+1)%len(pg)]
							pts = append(pts, Point{Lat: (a.Lat + b.Lat) / 2, Lon: (a.Lon + b.Lon) / 2})
						}

						tr := New(maxEntries)
						for i, p := range pts {
							tr.Insert(Entry{Point: p, RID: ridOf(i)})
						}

						var want []int
						for i, p := range pts {
							if pg.Contains(p) {
								want = append(want, i)
							}
						}
						sort.Ints(want)

						found, err := tr.SearchPolygon(pg)
						if err != nil {
							t.Fatalf("%s: %v", shape.name, err)
						}
						got := ids(found)

						if len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
							t.Fatalf("%s #%d: R-Tree=%d puntos, recorrido lineal=%d\n  polígono=%v",
								shape.name, rep, len(got), len(want), pg)
						}
					}
				}
			})
		}
	}
}

// relate puede decir "partial" por prudencia, pero nunca debe afirmar
// "disjoint" o "inside" si hay un punto del rectángulo que lo contradiga.
func TestRelateNeverContradictsSampledPoints(t *testing.T) {
	rng := rand.New(rand.NewSource(77))
	seen := map[relation]int{}

	for iter := 0; iter < 3000; iter++ {
		var pg Polygon
		switch iter % 4 {
		case 0:
			pg = regularPolygon(rng, 0, 0, 10, 3+rng.Intn(6))
		case 1:
			pg = starPolygon(rng, 0, 0, 10, 3+rng.Intn(5))
		case 2:
			pg = radialPolygon(rng, 0, 0, 10, 5+rng.Intn(20))
		default:
			pg = scribblePolygon(rng, 0, 0, 10, 4+rng.Intn(6))
		}
		q := queryPolygon{pts: pg, bounds: pg.Bounds()}

		span := []float64{0.2, 1, 4, 12, 30}[rng.Intn(5)]
		x, y := (rng.Float64()-0.5)*30, (rng.Float64()-0.5)*30
		r := NewRect(Point{x, y}, Point{x + rng.Float64()*span, y + rng.Float64()*span})

		samples := []Point{r.Min, r.Max, {r.Min.Lat, r.Max.Lon}, {r.Max.Lat, r.Min.Lon}}
		for s := 0; s < 200; s++ {
			samples = append(samples, Point{
				Lat: r.Min.Lat + rng.Float64()*(r.Max.Lat-r.Min.Lat),
				Lon: r.Min.Lon + rng.Float64()*(r.Max.Lon-r.Min.Lon),
			})
		}
		for s := 0; s <= 60; s++ {
			f := float64(s) / 60
			samples = append(samples,
				Point{r.Min.Lat + f*(r.Max.Lat-r.Min.Lat), r.Min.Lon},
				Point{r.Min.Lat + f*(r.Max.Lat-r.Min.Lat), r.Max.Lon},
				Point{r.Min.Lat, r.Min.Lon + f*(r.Max.Lon-r.Min.Lon)},
				Point{r.Max.Lat, r.Min.Lon + f*(r.Max.Lon-r.Min.Lon)},
			)
		}

		anyIn, anyOut := false, false
		for _, p := range samples {
			if pg.Contains(p) {
				anyIn = true
			} else {
				anyOut = true
			}
		}

		rel := q.relate(r)
		seen[rel]++

		if rel == disjoint && anyIn {
			t.Fatalf("relate dijo disjoint pero hay un punto del rectángulo dentro del polígono\n  rect=%+v polígono=%v", r, pg)
		}
		if rel == inside && anyOut {
			t.Fatalf("relate dijo inside pero hay un punto del rectángulo fuera del polígono\n  rect=%+v polígono=%v", r, pg)
		}
	}

	for _, rel := range []relation{disjoint, partial, inside} {
		if seen[rel] < 50 {
			t.Fatalf("el generador casi no produjo la relación %d (%d veces): la prueba no cubre todos los casos", rel, seen[rel])
		}
	}
}

// Si el polígono contiene todo el árbol, el resultado debe ser todo el árbol,
// sin perder ni duplicar entradas (se devuelven por la rama "inside").
func TestSearchPolygonCoveringEverythingReturnsEveryEntryOnce(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	pts := genPoints(rng, 2000, false)

	tr := New(8)
	for i, p := range pts {
		tr.Insert(Entry{Point: p, RID: ridOf(i)})
	}

	found, err := tr.SearchPolygon(square(-20, -100, 20, -50))
	if err != nil {
		t.Fatal(err)
	}

	got := ids(found)
	if len(got) != len(pts) {
		t.Fatalf("obtuve %d entradas, esperaba %d", len(got), len(pts))
	}
	for i, id := range got {
		if id != i {
			t.Fatalf("falta o se repite la entrada %d (posición %d tiene %d)", i, i, id)
		}
	}
}
