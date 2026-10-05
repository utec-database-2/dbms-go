package sql

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/dbms-go/v2/dbms/lib/index/rtree"
)

// seedTiendas crea la tabla del enunciado con puntos de Lima.
func seedTiendas(t *testing.T, e *Engine, clustered bool) {
	t.Helper()
	create := "CREATE TABLE tiendas (id INT PRIMARY KEY, nombre VARCHAR(40), ubicacion POINT)"
	if clustered {
		create = "CREATE CLUSTERED TABLE tiendas (id INT PRIMARY KEY, nombre VARCHAR(40), ubicacion POINT)"
	}
	run(t, e, create)
	run(t, e, `INSERT INTO tiendas VALUES
		(1, 'Centro',  POINT(-12.0464, -77.0428)),
		(2, 'Norte',   POINT(-12.0200, -77.0428)),
		(3, 'Cercana', POINT(-12.0500, -77.0428)),
		(4, 'Sur',     POINT(-12.0700, -77.0428)),
		(5, 'Lejana',  POINT(-12.1060, -77.0428)),
		(6, 'Oeste',   'POINT(-12.0464 -77.0600)')`)
}

func names(res *Result, col int) []string {
	out := make([]string, 0, len(res.Rows))
	for _, r := range res.Rows {
		out = append(out, fmt.Sprint(r[col]))
	}
	return out
}

func TestSpatialPointYDistancia(t *testing.T) {
	e := newTestEngine(t, Options{})
	seedTiendas(t, e, false)

	res := run(t, e, "SELECT ubicacion FROM tiendas WHERE id = 1")
	if got := res.Rows[0][0]; got != "POINT(-12.0464 -77.0428)" {
		t.Fatalf("el punto debe guardarse con su signo y en forma canónica, quedó %v", got)
	}

	res = run(t, e, "SELECT nombre, distancia(ubicacion, POINT(-12.0464, -77.0428)) AS metros FROM tiendas WHERE id = 3")
	d, ok := res.Rows[0][1].(float64)
	if !ok || d < 390 || d > 410 {
		t.Fatalf("Centro -> Cercana debería estar a ~400 m, salió %v", res.Rows[0][1])
	}

	info := e.Files()[0]
	if info.Types[2] != "point" || info.IndexKinds[len(info.IndexKinds)-1] != "r-tree" {
		t.Fatalf("la columna POINT debe verse como point con un r-tree: %+v", info)
	}
}

func TestSpatialRadioUsaRTree(t *testing.T) {
	for _, clustered := range []bool{false, true} {
		t.Run(fmt.Sprintf("clustered=%v", clustered), func(t *testing.T) {
			e := newTestEngine(t, Options{})
			seedTiendas(t, e, clustered)

			res := run(t, e, "SELECT nombre FROM tiendas WHERE distancia(ubicacion, POINT(-12.0464, -77.0428)) < 3000")
			got := names(res, 0)
			sort.Strings(got)
			want := []string{"Centro", "Cercana", "Norte", "Oeste", "Sur"}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("radio 3 km: %v, se esperaba %v\n%s", got, want, planText(res))
			}
			if !strings.Contains(planText(res), "R-Tree") {
				t.Fatalf("el radio debe resolverse con el R-Tree:\n%s", planText(res))
			}

			// Con otra condición, el resto del WHERE se aplica encima.
			res = run(t, e, "SELECT nombre FROM tiendas WHERE distancia(ubicacion, POINT(-12.0464, -77.0428)) <= 3000 AND id > 3")
			got = names(res, 0)
			sort.Strings(got)
			if strings.Join(got, ",") != "Oeste,Sur" {
				t.Fatalf("radio + filtro: %v\n%s", got, planText(res))
			}
		})
	}
}

func TestSpatialKNNConOrderByLimit(t *testing.T) {
	e := newTestEngine(t, Options{})
	seedTiendas(t, e, false)

	res := run(t, e, "SELECT nombre FROM tiendas ORDER BY distancia(ubicacion, POINT(-12.0464, -77.0428)) LIMIT 3")
	// Centro (0 m), Cercana (~400 m), Oeste (~1.9 km); Norte está a ~2.9 km.
	if got := strings.Join(names(res, 0), ","); got != "Centro,Cercana,Oeste" {
		t.Fatalf("k-NN: %s\n%s", got, planText(res))
	}
	if !strings.Contains(planText(res), "k-NN") {
		t.Fatalf("ORDER BY distancia LIMIT k debe usar el k-NN del R-Tree:\n%s", planText(res))
	}

	// Con WHERE ya no aplica el atajo, pero el resultado tiene que coincidir
	// con el orden por distancia (external sort).
	res = run(t, e, "SELECT nombre FROM tiendas WHERE id > 1 ORDER BY distancia(ubicacion, POINT(-12.0464, -77.0428)) LIMIT 2")
	if got := strings.Join(names(res, 0), ","); got != "Cercana,Oeste" {
		t.Fatalf("k-NN con filtro: %s\n%s", got, planText(res))
	}
}

func TestSpatialPoligonoYEuclidiana(t *testing.T) {
	e := newTestEngine(t, Options{})
	seedTiendas(t, e, false)

	// Rectángulo alrededor del centro: entran Centro y Cercana.
	res := run(t, e, `SELECT nombre FROM tiendas WHERE dentro(ubicacion,
		POLYGON(POINT(-12.040, -77.050), POINT(-12.040, -77.035), POINT(-12.055, -77.035), POINT(-12.055, -77.050))) = true`)
	got := names(res, 0)
	sort.Strings(got)
	if strings.Join(got, ",") != "Centro,Cercana" {
		t.Fatalf("polígono: %v\n%s", got, planText(res))
	}
	if !strings.Contains(planText(res), "polígono") {
		t.Fatalf("el polígono debe resolverse con el R-Tree:\n%s", planText(res))
	}

	// Euclidiana en grados: 0.03° alrededor del centro.
	res = run(t, e, "SELECT nombre FROM tiendas WHERE distancia(ubicacion, POINT(-12.0464, -77.0428), 'euclidean') < 0.03")
	got = names(res, 0)
	sort.Strings(got)
	if strings.Join(got, ",") != "Centro,Cercana,Norte,Oeste,Sur" {
		t.Fatalf("euclidiana: %v", got)
	}
}

func TestSpatialMantieneElIndiceYPersiste(t *testing.T) {
	dir := t.TempDir()
	e := newTestEngine(t, Options{Dir: dir})
	seedTiendas(t, e, false)

	run(t, e, "DELETE FROM tiendas WHERE id = 3")
	run(t, e, "UPDATE tiendas SET ubicacion = POINT(-12.0465, -77.0429) WHERE id = 5")
	res := run(t, e, "SELECT nombre FROM tiendas ORDER BY distancia(ubicacion, POINT(-12.0464, -77.0428)) LIMIT 2")
	if got := strings.Join(names(res, 0), ","); got != "Centro,Lejana" {
		t.Fatalf("tras DELETE/UPDATE: %s", got)
	}
	if _, err := e.Exec("INSERT INTO tiendas VALUES (9, 'Mala', 'no es un punto')"); err == nil {
		t.Fatal("un texto que no es POINT debe rechazarse")
	}
	e.Close()

	// Al reabrir, el R-Tree se reconstruye desde la tabla.
	e2, err := Open("test", Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Close()
	res = run(t, e2, "SELECT nombre FROM tiendas WHERE distancia(ubicacion, POINT(-12.0464, -77.0428)) < 100")
	got := names(res, 0)
	sort.Strings(got)
	if strings.Join(got, ",") != "Centro,Lejana" || !strings.Contains(planText(res), "R-Tree") {
		t.Fatalf("tras reabrir: %v\n%s", got, planText(res))
	}
}

// TestSpatialCoincideConFuerzaBruta compara el R-Tree con el recorrido
// secuencial sobre una malla de puntos.
func TestSpatialCoincideConFuerzaBruta(t *testing.T) {
	e := newTestEngine(t, Options{})
	run(t, e, "CREATE TABLE p (id INT PRIMARY KEY, ubicacion POINT)")
	var values []string
	id := 0
	for i := 0; i < 20; i++ {
		for j := 0; j < 20; j++ {
			id++
			values = append(values, fmt.Sprintf("(%d, POINT(%f, %f))", id, -12.2+float64(i)*0.02, -77.2+float64(j)*0.02))
		}
	}
	run(t, e, "INSERT INTO p VALUES "+strings.Join(values, ", "))

	center := rtree.Point{Lat: -12.05, Lon: -77.03}
	all := run(t, e, "SELECT id, ubicacion FROM p")
	want := 0
	for _, r := range all.Rows {
		pt, err := parsePoint(r[1])
		if err != nil {
			t.Fatal(err)
		}
		if spatialDistance(center, pt, rtree.Haversine) < 5000 {
			want++
		}
	}
	res := run(t, e, "SELECT id FROM p WHERE distancia(ubicacion, POINT(-12.05, -77.03)) < 5000")
	if len(res.Rows) != want || want == 0 {
		t.Fatalf("R-Tree devolvió %d filas, fuerza bruta %d", len(res.Rows), want)
	}
}
