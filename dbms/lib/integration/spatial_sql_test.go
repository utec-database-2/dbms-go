package integration

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/dbms-go/v2/dbms/lib/index/rtree"
	"github.com/dbms-go/v2/dbms/lib/shared"
	"github.com/dbms-go/v2/dbms/lib/sql"
)

type geoRow struct {
	id  int
	lat float64
	lon float64
}

func (r geoRow) distMeters(lat, lon float64) float64 {
	return rtree.Distance(rtree.Point{Lat: lat, Lon: lon}, rtree.Point{Lat: r.lat, Lon: r.lon}, rtree.Haversine) * 1000
}

func (r geoRow) distDegrees(lat, lon float64) float64 {
	return rtree.Distance(rtree.Point{Lat: lat, Lon: lon}, rtree.Point{Lat: r.lat, Lon: r.lon}, rtree.Euclidean)
}

// genGeoRows crea n puntos alrededor de Lima. Las coordenadas se pasan por
// el mismo formato de texto que usa el INSERT, para que el cálculo de
// referencia use exactamente los mismos float64 que quedan guardados.
func genGeoRows(rng *rand.Rand, startID, n int) ([]geoRow, []string) {
	rows := make([]geoRow, 0, n)
	tuples := make([]string, 0, n)
	for i := 0; i < n; i++ {
		latText := fmt.Sprintf("%.9f", -12.0+(rng.Float64()-0.5)*0.3)
		lonText := fmt.Sprintf("%.9f", -77.0+(rng.Float64()-0.5)*0.3)
		lat, _ := strconv.ParseFloat(latText, 64)
		lon, _ := strconv.ParseFloat(lonText, 64)
		id := startID + i
		rows = append(rows, geoRow{id: id, lat: lat, lon: lon})
		tuples = append(tuples, fmt.Sprintf("(%d, 'p%d', POINT(%s, %s))", id, id, latText, lonText))
	}
	return rows, tuples
}

func insertGeoRows(t *testing.T, db *sql.Database, table string, tuples []string) {
	t.Helper()
	for start := 0; start < len(tuples); start += 100 {
		end := start + 100
		if end > len(tuples) {
			end = len(tuples)
		}
		exec(t, db, fmt.Sprintf("INSERT INTO %s VALUES %s;", table, strings.Join(tuples[start:end], ", ")))
	}
}

func newGeoDB(t *testing.T, n int) (*sql.Database, []geoRow) {
	t.Helper()
	db := newTestDB(t)
	exec(t, db, "CREATE TABLE lugares (id INT, nombre STRING, ubicacion POINT);")
	rows, tuples := genGeoRows(rand.New(rand.NewSource(21)), 1, n)
	insertGeoRows(t, db, "lugares", tuples)
	return db, rows
}

func idsOf(t *testing.T, res *sql.Result) []int {
	t.Helper()
	out := make([]int, 0, len(res.Rows))
	for _, r := range res.Rows {
		id, ok := r[0].(int)
		if !ok {
			t.Fatalf("la primera columna no es int: %T", r[0])
		}
		out = append(out, id)
	}
	return out
}

func sortedCopy(in []int) []int {
	out := append([]int(nil), in...)
	sort.Ints(out)
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func planText(res *sql.Result) string { return strings.Join(res.Plan, " | ") }

const (
	radiusPlanStep = "Búsqueda por radio en el R-Tree"
	knnPlanStep    = "k-NN en el R-Tree"
)

func TestSpatialSQLRadiusMatchesBruteForce(t *testing.T) {
	db, rows := newGeoDB(t, 1500)
	rng := rand.New(rand.NewSource(8))

	type op struct {
		text    string
		keep    func(d, r float64) bool
		indexed bool
	}
	ops := []op{
		{"<", func(d, r float64) bool { return d < r }, true},
		{"<=", func(d, r float64) bool { return d <= r }, true},
		{">", func(d, r float64) bool { return d > r }, false},
		{">=", func(d, r float64) bool { return d >= r }, false},
	}

	for q := 0; q < 12; q++ {
		lat := -12.0 + (rng.Float64()-0.5)*0.3
		lon := -77.0 + (rng.Float64()-0.5)*0.3
		for _, radius := range []float64{300, 2000, 9000} {
			for _, o := range ops {
				query := fmt.Sprintf("SELECT * FROM lugares WHERE distancia(ubicacion, POINT(%.9f, %.9f)) %s %v;", lat, lon, o.text, radius)
				res := exec(t, db, query)

				var want []int
				for _, r := range rows {
					if o.keep(r.distMeters(lat, lon), radius) {
						want = append(want, r.id)
					}
				}
				got := idsOf(t, res)
				if !equalInts(got, want) {
					t.Fatalf("%s\n  motor=%d filas, referencia=%d filas", query, len(got), len(want))
				}

				usesRTree := strings.Contains(planText(res), radiusPlanStep)
				if o.indexed && !usesRTree {
					t.Fatalf("%s debería usar el R-Tree. Plan: %s", query, planText(res))
				}
				if !o.indexed && usesRTree {
					t.Fatalf("%s no se puede indexar pero el plan dice R-Tree: %s", query, planText(res))
				}
			}
		}
	}
}

func TestSpatialSQLEuclideanMetricArgument(t *testing.T) {
	db, rows := newGeoDB(t, 800)
	lat, lon := -12.0, -77.0

	for _, radius := range []float64{0.005, 0.03, 0.1} {
		res := exec(t, db, fmt.Sprintf(
			"SELECT * FROM lugares WHERE distancia(ubicacion, POINT(%v, %v), 'euclidean') <= %v;", lat, lon, radius))

		var want []int
		for _, r := range rows {
			if r.distDegrees(lat, lon) <= radius {
				want = append(want, r.id)
			}
		}
		if got := idsOf(t, res); !equalInts(got, want) {
			t.Fatalf("radio %v grados: motor=%d filas, referencia=%d", radius, len(got), len(want))
		}
		if !strings.Contains(planText(res), radiusPlanStep) || !strings.Contains(planText(res), "Euclidiana") {
			t.Fatalf("el plan debería indicar R-Tree y Euclidiana: %s", planText(res))
		}
	}
}

func TestSpatialSQLKNNMatchesBruteForce(t *testing.T) {
	db, rows := newGeoDB(t, 1500)
	rng := rand.New(rand.NewSource(17))

	for q := 0; q < 10; q++ {
		lat := -12.0 + (rng.Float64()-0.5)*0.4
		lon := -77.0 + (rng.Float64()-0.5)*0.4

		byDistance := append([]geoRow(nil), rows...)
		sort.Slice(byDistance, func(i, j int) bool {
			return byDistance[i].distMeters(lat, lon) < byDistance[j].distMeters(lat, lon)
		})

		for _, k := range []int{1, 10, 50, 5000} {
			res := exec(t, db, fmt.Sprintf(
				"SELECT * FROM lugares ORDER BY distancia(ubicacion, POINT(%.9f, %.9f)) LIMIT %d;", lat, lon, k))

			n := k
			if n > len(byDistance) {
				n = len(byDistance)
			}
			want := make([]int, n)
			for i := 0; i < n; i++ {
				want[i] = byDistance[i].id
			}

			if got := idsOf(t, res); !equalInts(got, want) {
				t.Fatalf("k=%d: el orden de los vecinos no coincide con la referencia", k)
			}
			if !strings.Contains(planText(res), knnPlanStep) {
				t.Fatalf("k=%d debería usar k-NN del R-Tree. Plan: %s", k, planText(res))
			}
		}
	}
}

func TestSpatialSQLOrderByDistanceDescAndWithoutLimit(t *testing.T) {
	db, rows := newGeoDB(t, 600)
	lat, lon := -12.01, -77.02

	byDistance := append([]geoRow(nil), rows...)
	sort.Slice(byDistance, func(i, j int) bool {
		return byDistance[i].distMeters(lat, lon) < byDistance[j].distMeters(lat, lon)
	})

	all := exec(t, db, fmt.Sprintf("SELECT * FROM lugares ORDER BY distancia(ubicacion, POINT(%v, %v));", lat, lon))
	if len(all.Rows) != len(rows) {
		t.Fatalf("sin LIMIT esperaba %d filas, obtuve %d", len(rows), len(all.Rows))
	}
	for i, id := range idsOf(t, all) {
		if id != byDistance[i].id {
			t.Fatalf("ASC sin LIMIT: posición %d tiene id %d, esperaba %d", i, id, byDistance[i].id)
		}
	}
	if strings.Contains(planText(all), knnPlanStep) {
		t.Fatalf("sin LIMIT no debe usar k-NN: %s", planText(all))
	}

	desc := exec(t, db, fmt.Sprintf("SELECT * FROM lugares ORDER BY distancia(ubicacion, POINT(%v, %v)) DESC LIMIT 5;", lat, lon))
	got := idsOf(t, desc)
	for i := 0; i < 5; i++ {
		if want := byDistance[len(byDistance)-1-i].id; got[i] != want {
			t.Fatalf("DESC LIMIT 5: posición %d tiene id %d, esperaba %d", i, got[i], want)
		}
	}
	if strings.Contains(planText(desc), knnPlanStep) {
		t.Fatalf("DESC no puede usar k-NN: %s", planText(desc))
	}
}

func TestSpatialSQLRadiusPlusOrderByDistanceAndLimit(t *testing.T) {
	db, rows := newGeoDB(t, 1000)
	lat, lon, radius := -12.0, -77.0, 6000.0

	var inside []geoRow
	for _, r := range rows {
		if r.distMeters(lat, lon) < radius {
			inside = append(inside, r)
		}
	}
	sort.Slice(inside, func(i, j int) bool { return inside[i].distMeters(lat, lon) < inside[j].distMeters(lat, lon) })

	res := exec(t, db, fmt.Sprintf(
		"SELECT * FROM lugares WHERE distancia(ubicacion, POINT(%v, %v)) < %v ORDER BY distancia(ubicacion, POINT(%v, %v)) LIMIT 7;",
		lat, lon, radius, lat, lon))

	n := 7
	if len(inside) < n {
		n = len(inside)
	}
	got := idsOf(t, res)
	if len(got) != n {
		t.Fatalf("obtuve %d filas, esperaba %d", len(got), n)
	}
	for i := 0; i < n; i++ {
		if got[i] != inside[i].id {
			t.Fatalf("posición %d: id %d, esperaba %d", i, got[i], inside[i].id)
		}
	}
}

func TestSpatialSQLIndexStaysConsistentAfterInsertAndDelete(t *testing.T) {
	db, rows := newGeoDB(t, 700)
	lat, lon, radius := -12.0, -77.0, 7000.0

	query := fmt.Sprintf("SELECT * FROM lugares WHERE distancia(ubicacion, POINT(%v, %v)) < %v;", lat, lon, radius)
	expected := func(live []geoRow) []int {
		var want []int
		for _, r := range live {
			if r.distMeters(lat, lon) < radius {
				want = append(want, r.id)
			}
		}
		return want
	}
	check := func(label string, live []geoRow) *sql.Result {
		res := exec(t, db, query)
		if got := idsOf(t, res); !equalInts(got, expected(live)) {
			t.Fatalf("%s: motor=%d filas, referencia=%d filas", label, len(got), len(expected(live)))
		}
		return res
	}

	first := check("primera consulta", rows)
	if !strings.Contains(planText(first), "Construcción del R-Tree") {
		t.Fatalf("la primera consulta debía construir el R-Tree: %s", planText(first))
	}

	more, tuples := genGeoRows(rand.New(rand.NewSource(99)), 10000, 300)
	insertGeoRows(t, db, "lugares", tuples)
	live := append(append([]geoRow(nil), rows...), more...)
	second := check("tras INSERT", live)
	if strings.Contains(planText(second), "Construcción del R-Tree") {
		t.Fatalf("un INSERT no debe obligar a reconstruir el R-Tree: %s", planText(second))
	}

	exec(t, db, "DELETE FROM lugares WHERE id = 5;")
	var afterKeyDelete []geoRow
	for _, r := range live {
		if r.id != 5 {
			afterKeyDelete = append(afterKeyDelete, r)
		}
	}
	live = afterKeyDelete
	third := check("tras DELETE por clave", live)
	if !strings.Contains(planText(third), "Construcción del R-Tree") {
		t.Fatalf("tras un DELETE el R-Tree debe reconstruirse: %s", planText(third))
	}

	del := exec(t, db, fmt.Sprintf("DELETE FROM lugares WHERE distancia(ubicacion, POINT(%v, %v)) < 2500;", lat, lon))
	var survivors []geoRow
	removed := 0
	for _, r := range live {
		if r.distMeters(lat, lon) < 2500 {
			removed++
		} else {
			survivors = append(survivors, r)
		}
	}
	if int(del.Affected) != removed {
		t.Fatalf("DELETE espacial afectó %d filas, esperaba %d", del.Affected, removed)
	}
	if !strings.Contains(planText(del), radiusPlanStep) {
		t.Fatalf("el DELETE espacial debería usar el R-Tree: %s", planText(del))
	}
	check("tras DELETE espacial", survivors)

	all := exec(t, db, "SELECT * FROM lugares;")
	if len(all.Rows) != len(survivors) {
		t.Fatalf("quedaron %d filas, esperaba %d", len(all.Rows), len(survivors))
	}
}

func TestSpatialSQLLimitWithoutSpatialFunctions(t *testing.T) {
	db := newTestDB(t)
	exec(t, db, "CREATE TABLE t (id INT, name STRING);")
	exec(t, db, "INSERT INTO t VALUES (3, 'c'), (1, 'a'), (2, 'b'), (5, 'e'), (4, 'd');")

	asc := exec(t, db, "SELECT * FROM t LIMIT 2;")
	if got := idsOf(t, asc); !equalInts(got, []int{1, 2}) {
		t.Fatalf("LIMIT 2 = %v, esperaba [1 2]", got)
	}
	if !strings.Contains(planText(asc), "LIMIT 2") {
		t.Fatalf("el plan debe mostrar el LIMIT: %s", planText(asc))
	}

	desc := exec(t, db, "SELECT * FROM t ORDER BY id DESC LIMIT 3;")
	if got := idsOf(t, desc); !equalInts(got, []int{5, 4, 3}) {
		t.Fatalf("ORDER BY id DESC LIMIT 3 = %v, esperaba [5 4 3]", got)
	}

	if zero := exec(t, db, "SELECT * FROM t LIMIT 0;"); len(zero.Rows) != 0 {
		t.Fatalf("LIMIT 0 devolvió %d filas", len(zero.Rows))
	}
	if big := exec(t, db, "SELECT * FROM t LIMIT 100;"); len(big.Rows) != 5 {
		t.Fatalf("LIMIT mayor que la tabla devolvió %d filas, esperaba 5", len(big.Rows))
	}

	where := exec(t, db, "SELECT * FROM t WHERE id >= 2 LIMIT 2;")
	if got := idsOf(t, where); !equalInts(got, []int{2, 3}) {
		t.Fatalf("WHERE id >= 2 LIMIT 2 = %v, esperaba [2 3]", got)
	}
}

func TestSpatialSQLDistanceBetweenTwoColumns(t *testing.T) {
	db := newTestDB(t)
	exec(t, db, "CREATE TABLE restaurantes (id INT, ubicacion POINT, mi_ubicacion POINT);")
	exec(t, db, `INSERT INTO restaurantes VALUES
		(1, POINT(-12.00, -77.00), POINT(-12.30, -77.00)),
		(2, POINT(-12.00, -77.00), POINT(-12.01, -77.00)),
		(3, POINT(-12.00, -77.00), POINT(-12.10, -77.00));`)

	res := exec(t, db, "SELECT * FROM restaurantes ORDER BY distancia(ubicacion, mi_ubicacion) LIMIT 2;")
	if got := idsOf(t, res); !equalInts(got, []int{2, 3}) {
		t.Fatalf("ORDER BY distancia entre columnas = %v, esperaba [2 3]", got)
	}
	if strings.Contains(planText(res), knnPlanStep) || strings.Contains(planText(res), radiusPlanStep) {
		t.Fatalf("la distancia entre dos columnas no usa R-Tree: %s", planText(res))
	}

	near := exec(t, db, "SELECT * FROM restaurantes WHERE distancia(ubicacion, mi_ubicacion) < 5000;")
	if got := idsOf(t, near); !equalInts(got, []int{2}) {
		t.Fatalf("WHERE distancia entre columnas = %v, esperaba [2]", got)
	}
	if strings.Contains(planText(near), radiusPlanStep) {
		t.Fatalf("el WHERE entre dos columnas no usa R-Tree: %s", planText(near))
	}
}

func TestSpatialSQLPointKeepsPrecisionAndSerializes(t *testing.T) {
	db := newTestDB(t)
	exec(t, db, "CREATE TABLE t (id INT, ubicacion POINT);")
	exec(t, db, "INSERT INTO t VALUES (1, POINT(-12.0464321987, -77.0428123456));")

	res := exec(t, db, "SELECT * FROM t;")
	pt, ok := res.Rows[0][1].(shared.Point)
	if !ok {
		t.Fatalf("la columna POINT devolvió %T", res.Rows[0][1])
	}
	if pt.Lat != -12.0464321987 || pt.Lon != -77.0428123456 {
		t.Fatalf("POINT = %v, perdió precisión", pt)
	}
	if pt.String() != "POINT(-12.0464321987, -77.0428123456)" {
		t.Fatalf("String() = %q", pt.String())
	}

	data, err := json.Marshal(res.Rows[0])
	if err != nil {
		t.Fatalf("json: %v", err)
	}
	if string(data) != `[1,"POINT(-12.0464321987, -77.0428123456)"]` {
		t.Fatalf("json = %s", data)
	}
}

func TestSpatialSQLDefaultPointWhenColumnOmitted(t *testing.T) {
	db := newTestDB(t)
	exec(t, db, "CREATE TABLE t (id INT, nombre STRING, ubicacion POINT);")
	exec(t, db, "INSERT INTO t (id, nombre) VALUES (1, 'sin punto');")

	res := exec(t, db, "SELECT * FROM t;")
	if pt, ok := res.Rows[0][2].(shared.Point); !ok || pt != (shared.Point{}) {
		t.Fatalf("la columna POINT omitida debería ser POINT(0, 0), obtuve %#v", res.Rows[0][2])
	}
}

func TestSpatialSQLTypesAreCaseInsensitive(t *testing.T) {
	db := newTestDB(t)
	exec(t, db, "create table t (id int, nombre string, ubicacion point);")
	exec(t, db, "insert into t values (1, 'a', point(-12.0, -77.0)), (2, 'b', POINT(-12.5, -77.5));")

	res := exec(t, db, "select * from t where DISTANCIA(ubicacion, Point(-12.0, -77.0)) < 100 order by id limit 5;")
	if got := idsOf(t, res); !equalInts(got, []int{1}) {
		t.Fatalf("resultado = %v, esperaba [1]", got)
	}
}

func TestSpatialSQLPersistsWithoutCatalog(t *testing.T) {
	dir := t.TempDir()

	db, err := sql.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	exec(t, db, "CREATE TABLE t (id INT, ubicacion POINT);")
	exec(t, db, "INSERT INTO t VALUES (1, POINT(-12.0, -77.0)), (2, POINT(-12.4, -77.4));")
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db2, err := sql.Open(dir)
	if err != nil {
		t.Fatalf("Open (2): %v", err)
	}
	defer db2.Close()
	exec(t, db2, "CREATE TABLE t (id INT, ubicacion POINT);")

	res := exec(t, db2, "SELECT * FROM t WHERE distancia(ubicacion, POINT(-12.0, -77.0)) < 1000;")
	if got := idsOf(t, res); !equalInts(got, []int{1}) {
		t.Fatalf("tras reabrir, resultado = %v, esperaba [1]", got)
	}
	if !strings.Contains(planText(res), "Construcción del R-Tree") {
		t.Fatalf("tras reabrir el R-Tree debe reconstruirse desde el Heap File: %s", planText(res))
	}
}

func TestSpatialSQLErrors(t *testing.T) {
	db := newTestDB(t)
	exec(t, db, "CREATE TABLE t (id INT, nombre STRING, ubicacion POINT);")
	exec(t, db, "INSERT INTO t VALUES (1, 'a', POINT(-12.0, -77.0));")

	bad := []struct {
		name  string
		query string
	}{
		{"distancia sobre columna que no es POINT", "SELECT * FROM t WHERE distancia(nombre, POINT(0, 0)) < 5;"},
		{"columna desconocida", "SELECT * FROM t WHERE distancia(nope, POINT(0, 0)) < 5;"},
		{"un solo argumento", "SELECT * FROM t WHERE distancia(ubicacion) < 5;"},
		{"cuatro argumentos", "SELECT * FROM t WHERE distancia(ubicacion, POINT(0, 0), 'haversine', 1) < 5;"},
		{"función desconocida", "SELECT * FROM t WHERE cercania(ubicacion, POINT(0, 0)) < 5;"},
		{"métrica desconocida", "SELECT * FROM t WHERE distancia(ubicacion, POINT(0, 0), 'manhattan') < 5;"},
		{"métrica que no es texto", "SELECT * FROM t WHERE distancia(ubicacion, POINT(0, 0), 5) < 5;"},
		{"comparar contra un texto", "SELECT * FROM t WHERE distancia(ubicacion, POINT(0, 0)) < 'x';"},
		{"AND con distancia", "SELECT * FROM t WHERE distancia(ubicacion, POINT(0, 0)) < 5 AND id = 1;"},
		{"ORDER BY una columna POINT", "SELECT * FROM t ORDER BY ubicacion;"},
		{"ORDER BY distancia inválida", "SELECT * FROM t ORDER BY distancia(nombre, POINT(0, 0));"},
		{"DELETE con distancia inválida", "DELETE FROM t WHERE distancia(nombre, POINT(0, 0)) < 5;"},
		{"número en columna POINT", "INSERT INTO t VALUES (2, 'b', 5);"},
		{"texto en columna POINT", "INSERT INTO t VALUES (2, 'b', 'x');"},
		{"POINT en columna STRING", "INSERT INTO t VALUES (2, POINT(1, 2), POINT(1, 2));"},
		{"POINT en la clave", "INSERT INTO t VALUES (POINT(1, 2), 'b', POINT(1, 2));"},
	}
	for _, c := range bad {
		if _, err := db.Execute(c.query); err == nil {
			t.Errorf("%s: %q debería fallar", c.name, c.query)
		}
	}

	if res := exec(t, db, "SELECT * FROM t;"); len(res.Rows) != 1 {
		t.Fatalf("los INSERT inválidos no deben dejar filas: hay %d", len(res.Rows))
	}
}

func TestSpatialSQLPointAsFirstColumnIsRejected(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.Execute("CREATE TABLE t (ubicacion POINT, id INT);"); err == nil {
		t.Fatal("la clave (primera columna) debe seguir siendo INT")
	}
}
