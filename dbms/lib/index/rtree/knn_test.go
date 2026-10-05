package rtree

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"testing"
)

// bruteKNN calcula las distancias de todos los puntos y devuelve las k menores, ordenadas.
func bruteKNN(center Point, pts []Point, k int, metric DistanceMetric) []float64 {
	d := make([]float64, len(pts))
	for i, p := range pts {
		d[i] = Distance(center, p, metric)
	}
	sort.Float64s(d)
	if k < len(d) {
		d = d[:k]
	}
	return d
}

func closeTo(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9*math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
}

// La prueba central: KNN debe devolver exactamente las mismas distancias que
// ordenar todos los puntos, con ambas métricas, cualquier k y cualquier forma
// del árbol (incluyendo muchos puntos idénticos).
func TestKNNMatchesBruteForce(t *testing.T) {
	for _, maxEntries := range []int{2, 4, 16} {
		for _, n := range []int{0, 1, 5, 200, 3000} {
			for _, clustered := range []bool{false, true} {
				name := fmt.Sprintf("M=%d/n=%d/clustered=%v", maxEntries, n, clustered)
				t.Run(name, func(t *testing.T) {
					rng := rand.New(rand.NewSource(int64(7000 + maxEntries*100000 + n)))
					pts := genPoints(rng, n, clustered)

					tr := New(maxEntries)
					for i, p := range pts {
						tr.Insert(Entry{Point: p, RID: ridOf(i)})
					}

					centers := []Point{
						{Lat: -12.0, Lon: -77.0}, // dentro de los datos
						{Lat: 10, Lon: 10},       // lejos
						{Lat: 89.9, Lon: 0},      // cerca del polo
						{Lat: -12.0, Lon: 179.9}, // cerca del antimeridiano
					}
					for i := 0; i < 10 && n > 0; i++ {
						centers = append(centers, pts[rng.Intn(n)])
					}

					for _, c := range centers {
						for _, metric := range []DistanceMetric{Haversine, Euclidean} {
							for _, k := range []int{1, 3, 10, 50, n + 5} {
								want := bruteKNN(c, pts, k, metric)
								got := tr.KNN(c, k, metric)

								if len(got) != len(want) {
									t.Fatalf("centro %+v métrica %v k=%d: devolvió %d, esperaba %d",
										c, metric, k, len(got), len(want))
								}

								seen := map[int32]bool{}
								for i, nb := range got {
									if !closeTo(nb.Distance, want[i]) {
										t.Fatalf("centro %+v métrica %v k=%d, posición %d: distancia %v, esperaba %v",
											c, metric, k, i, nb.Distance, want[i])
									}
									if real := Distance(c, nb.Entry.Point, metric); !closeTo(real, nb.Distance) {
										t.Fatalf("la distancia reportada %v no coincide con la real %v", nb.Distance, real)
									}
									if seen[nb.Entry.RID.Slot] {
										t.Fatalf("la entrada %d aparece repetida", nb.Entry.RID.Slot)
									}
									seen[nb.Entry.RID.Slot] = true
								}
							}
						}
					}
				})
			}
		}
	}
}

func TestKNNEdgeCases(t *testing.T) {
	c := Point{Lat: -12, Lon: -77}

	empty := New(4)
	if got := empty.KNN(c, 5, Haversine); got != nil {
		t.Errorf("árbol vacío: esperaba nil, obtuve %v", got)
	}

	tr := New(4)
	tr.Insert(Entry{Point: Point{Lat: -12.01, Lon: -77}, RID: ridOf(1)})
	tr.Insert(Entry{Point: Point{Lat: -12.02, Lon: -77}, RID: ridOf(2)})

	if got := tr.KNN(c, 0, Haversine); got != nil {
		t.Errorf("k=0: esperaba nil, obtuve %v", got)
	}
	if got := tr.KNN(c, -3, Haversine); got != nil {
		t.Errorf("k negativo: esperaba nil, obtuve %v", got)
	}

	got := tr.KNN(c, 100, Haversine)
	if len(got) != 2 {
		t.Fatalf("k mayor que el tamaño: esperaba 2 resultados, obtuve %d", len(got))
	}
	if got[0].Entry.RID != ridOf(1) || got[1].Entry.RID != ridOf(2) {
		t.Errorf("orden incorrecto: %v, %v", got[0].Entry.RID, got[1].Entry.RID)
	}
}

func TestKNNKnownExample(t *testing.T) {
	center := Point{Lat: -12.0432, Lon: -77.0282}

	tr := New(4)
	tr.Insert(Entry{Point: Point{Lat: center.Lat + 0.05, Lon: center.Lon}, RID: ridOf(3)})  // ≈ 5.56 km
	tr.Insert(Entry{Point: Point{Lat: center.Lat + 0.01, Lon: center.Lon}, RID: ridOf(2)})  // ≈ 1.11 km
	tr.Insert(Entry{Point: center, RID: ridOf(1)})                                          // 0 km
	tr.Insert(Entry{Point: Point{Lat: center.Lat, Lon: center.Lon + 0.1}, RID: ridOf(4)})   // ≈ 10.9 km
	tr.Insert(Entry{Point: Point{Lat: center.Lat + 1, Lon: center.Lon + 1}, RID: ridOf(5)}) // ≈ 157 km

	got := tr.KNN(center, 3, Haversine)

	want := []int32{1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("esperaba %d vecinos, obtuve %d", len(want), len(got))
	}
	for i, nb := range got {
		if nb.Entry.RID.Slot != want[i] {
			t.Errorf("vecino %d: obtuve %d, esperaba %d", i, nb.Entry.RID.Slot, want[i])
		}
	}
	if math.Abs(got[1].Distance-1.112) > 0.01 {
		t.Errorf("distancia al 2.º vecino = %v km, esperaba ≈ 1.112", got[1].Distance)
	}
}

// Con todos los puntos idénticos el resultado debe seguir siendo válido y
// determinista (mismo orden entre dos corridas).
func TestKNNAllIdenticalPoints(t *testing.T) {
	tr := New(4)
	p := Point{Lat: -12, Lon: -77}
	for i := 0; i < 100; i++ {
		tr.Insert(Entry{Point: p, RID: ridOf(i)})
	}

	a := tr.KNN(Point{Lat: -12.1, Lon: -77.1}, 10, Haversine)
	b := tr.KNN(Point{Lat: -12.1, Lon: -77.1}, 10, Haversine)

	if len(a) != 10 || len(b) != 10 {
		t.Fatalf("esperaba 10 vecinos, obtuve %d y %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Entry.RID != b[i].Entry.RID {
			t.Fatalf("resultado no determinista en la posición %d", i)
		}
	}
}

// randomRect arma un rectángulo aleatorio válido (lat en [-90,90], lon en [-180,180]).
func randomRect(rng *rand.Rand, maxSpan float64) Rect {
	lat := -85 + rng.Float64()*170
	lon := -175 + rng.Float64()*350
	dLat := 0.001 + rng.Float64()*maxSpan
	dLon := 0.001 + rng.Float64()*maxSpan
	return Rect{
		Min: Point{Lat: math.Max(-90, lat), Lon: math.Max(-180, lon)},
		Max: Point{Lat: math.Min(90, lat+dLat), Lon: math.Min(180, lon+dLon)},
	}
}

// minDist debe ser una cota inferior: nunca mayor que la distancia real a ningún
// punto del rectángulo. Si no lo fuera, KNN podría descartar ramas que sí
// contienen vecinos. Se prueba con esquinas, bordes e interior de rectángulos de
// todos los tamaños (incluidos enormes y polares).
func TestMinDistIsLowerBound(t *testing.T) {
	rng := rand.New(rand.NewSource(11))

	for iter := 0; iter < 4000; iter++ {
		span := []float64{0.01, 1, 10, 60, 170}[rng.Intn(5)]
		r := randomRect(rng, span)

		p := Point{Lat: -90 + rng.Float64()*180, Lon: -180 + rng.Float64()*360}
		if rng.Intn(3) == 0 { // a veces muy cerca del rectángulo
			p = Point{
				Lat: r.Min.Lat + (rng.Float64()-0.5)*(r.Max.Lat-r.Min.Lat+2),
				Lon: r.Min.Lon + (rng.Float64()-0.5)*(r.Max.Lon-r.Min.Lon+2),
			}
			// el punto debe ser una coordenada válida
			p.Lat = math.Max(-90, math.Min(90, p.Lat))
			p.Lon = math.Max(-180, math.Min(180, p.Lon))
		}

		samples := []Point{
			r.Min, r.Max,
			{Lat: r.Min.Lat, Lon: r.Max.Lon}, {Lat: r.Max.Lat, Lon: r.Min.Lon},
		}
		for s := 0; s < 300; s++ {
			samples = append(samples, Point{
				Lat: r.Min.Lat + rng.Float64()*(r.Max.Lat-r.Min.Lat),
				Lon: r.Min.Lon + rng.Float64()*(r.Max.Lon-r.Min.Lon),
			})
		}
		for s := 0; s <= 100; s++ { // los cuatro bordes
			f := float64(s) / 100
			samples = append(samples,
				Point{Lat: r.Min.Lat + f*(r.Max.Lat-r.Min.Lat), Lon: r.Min.Lon},
				Point{Lat: r.Min.Lat + f*(r.Max.Lat-r.Min.Lat), Lon: r.Max.Lon},
				Point{Lat: r.Min.Lat, Lon: r.Min.Lon + f*(r.Max.Lon-r.Min.Lon)},
				Point{Lat: r.Max.Lat, Lon: r.Min.Lon + f*(r.Max.Lon-r.Min.Lon)},
			)
		}

		for _, metric := range []DistanceMetric{Haversine, Euclidean} {
			bound := minDist(p, r, metric)

			for _, q := range samples {
				if d := Distance(p, q, metric); bound > d+1e-9 {
					t.Fatalf("métrica %v: minDist=%v pero existe un punto del rectángulo a %v\n  p=%+v rect=%+v q=%+v",
						metric, bound, d, p, r, q)
				}
			}

			if r.Contains(p) && bound != 0 {
				t.Fatalf("métrica %v: p dentro del rectángulo pero minDist=%v", metric, bound)
			}
		}
	}
}

// ternaryMinOnMeridian encuentra numéricamente la distancia mínima de p al
// segmento del meridiano `lon` entre lat1 y lat2 (la distancia es unimodal a lo
// largo del segmento). Sirve de referencia independiente de la fórmula cerrada.
func ternaryMinOnMeridian(p Point, lon, lat1, lat2 float64) float64 {
	lo, hi := lat1, lat2
	f := func(lat float64) float64 { return Distance(p, Point{Lat: lat, Lon: lon}, Haversine) }
	for i := 0; i < 200; i++ {
		m1 := lo + (hi-lo)/3
		m2 := hi - (hi-lo)/3
		if f(m1) < f(m2) {
			hi = m2
		} else {
			lo = m1
		}
	}
	return math.Min(f(lo), math.Min(f(lat1), f(lat2)))
}

// Además de ser cota inferior, para un punto fuera de la franja de longitud del
// rectángulo minDist debe ser exacta: igual al mínimo calculado numéricamente
// sobre los dos bordes meridianos (una cota floja sería correcta pero haría
// que KNN visite de más).
func TestMinDistHaversineIsExactOutsideLonRange(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	checked := 0

	for iter := 0; iter < 3000; iter++ {
		r := randomRect(rng, []float64{0.05, 2, 15}[rng.Intn(3)])
		p := Point{Lat: -89 + rng.Float64()*178, Lon: -179 + rng.Float64()*358}

		if p.Lon >= r.Min.Lon && p.Lon <= r.Max.Lon {
			continue
		}

		want := math.Min(
			ternaryMinOnMeridian(p, r.Min.Lon, r.Min.Lat, r.Max.Lat),
			ternaryMinOnMeridian(p, r.Max.Lon, r.Min.Lat, r.Max.Lat),
		)
		got := minDist(p, r, Haversine)

		if math.Abs(got-want) > 1e-6*math.Max(1, want) {
			t.Fatalf("minDist=%v, mínimo numérico=%v\n  p=%+v rect=%+v", got, want, p, r)
		}
		checked++
	}

	if checked < 1000 {
		t.Fatalf("solo se probaron %d casos válidos, el generador está mal", checked)
	}
}
