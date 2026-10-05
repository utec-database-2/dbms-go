package spatial

import (
	"math"
	"strings"
	"testing"

	"github.com/dbms-go/v2/dbms/lib/sql"
)

// newLimaStore arma una base real con la tabla "ubicaciones" y 6 puntos de Lima.
func newLimaStore(t *testing.T) *Store {
	t.Helper()

	db, err := sql.Open("test", sql.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	stmts := []string{
		"CREATE TABLE ubicaciones (id INT PRIMARY KEY, nombre VARCHAR(40), tipo VARCHAR(20), latitud VARCHAR(20), longitud VARCHAR(20));",
		`INSERT INTO ubicaciones VALUES
			(1, "Tienda Centro",  "tienda", '-12.0432', '-77.0282'),
			(2, "Tienda Norte",   "tienda", '-12.0200', '-77.0282'),
			(3, "Tienda Cercana", "tienda", '-12.0500', '-77.0282'),
			(4, "Tienda Sur",     "tienda", '-12.0700', '-77.0282'),
			(5, "Tienda Lejana",  "tienda", '-12.1060', '-77.0282'),
			(6, "Tienda Oeste",   "tienda", '-12.0432', '-77.0600');`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("Execute %q: %v", s, err)
		}
	}

	return NewStore(db, "ubicaciones")
}

func TestSearchKNNOrdersByDistance(t *testing.T) {
	store := newLimaStore(t)

	resp, err := store.SearchKNN(KNNRequest{
		Latitude: -12.0432, Longitude: -77.0282, K: 4, Metric: "haversine",
	})
	if err != nil {
		t.Fatalf("SearchKNN: %v", err)
	}

	// Desde el centro: Centro (0 km), Cercana (≈0.76), Norte (≈2.58), Sur (≈3.0).
	want := []string{"Tienda Centro", "Tienda Cercana", "Tienda Norte", "Tienda Sur"}

	if len(resp.Results) != len(want) {
		t.Fatalf("esperaba %d vecinos, obtuve %d", len(want), len(resp.Results))
	}
	for i, r := range resp.Results {
		if r.Name != want[i] {
			t.Errorf("posición %d: obtuve %q, esperaba %q", i, r.Name, want[i])
		}
		if r.Rank != i+1 {
			t.Errorf("posición %d: rank=%d, esperaba %d", i, r.Rank, i+1)
		}
		if i > 0 && r.Distance < resp.Results[i-1].Distance {
			t.Errorf("las distancias no están ordenadas en la posición %d", i)
		}
	}

	if resp.K != 4 || resp.Metric != "haversine" {
		t.Errorf("eco de la petición incorrecto: k=%d metric=%q", resp.K, resp.Metric)
	}
	if last := resp.Results[len(resp.Results)-1].Distance; resp.MaxDistance != last {
		t.Errorf("MaxDistance=%v, esperaba la del último vecino (%v)", resp.MaxDistance, last)
	}
	if math.Abs(resp.Results[1].Distance-0.756) > 0.05 {
		t.Errorf("distancia a Tienda Cercana = %v km, esperaba ≈ 0.76", resp.Results[1].Distance)
	}
}

func TestSearchKNNEuclidean(t *testing.T) {
	store := newLimaStore(t)

	resp, err := store.SearchKNN(KNNRequest{
		Latitude: -12.0432, Longitude: -77.0282, K: 2, Metric: " Euclidean ",
	})
	if err != nil {
		t.Fatalf("SearchKNN: %v", err)
	}
	if len(resp.Results) != 2 || resp.Results[0].Name != "Tienda Centro" || resp.Results[1].Name != "Tienda Cercana" {
		t.Fatalf("resultados inesperados: %+v", resp.Results)
	}
	if resp.Metric != "euclidean" {
		t.Errorf("la métrica debe normalizarse a minúsculas, obtuve %q", resp.Metric)
	}
}

func TestSearchKNNMoreThanAvailable(t *testing.T) {
	store := newLimaStore(t)

	resp, err := store.SearchKNN(KNNRequest{
		Latitude: -12.0432, Longitude: -77.0282, K: 100, Metric: "haversine",
	})
	if err != nil {
		t.Fatalf("SearchKNN: %v", err)
	}
	if len(resp.Results) != 6 {
		t.Fatalf("k mayor que los datos: esperaba los 6 registros, obtuve %d", len(resp.Results))
	}
}

func TestSearchKNNValidation(t *testing.T) {
	store := newLimaStore(t)

	if _, err := store.SearchKNN(KNNRequest{K: 0, Metric: "haversine"}); err == nil {
		t.Error("k=0 debe dar error")
	}
	if _, err := store.SearchKNN(KNNRequest{K: -2, Metric: "haversine"}); err == nil {
		t.Error("k negativo debe dar error")
	}
	if _, err := store.SearchKNN(KNNRequest{K: 3, Metric: "manhattan"}); err == nil {
		t.Error("una métrica desconocida debe dar error")
	}
}

func TestSearchKNNWithoutTable(t *testing.T) {
	db, err := sql.Open("test", sql.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = NewStore(db, "ubicaciones").SearchKNN(KNNRequest{K: 3, Metric: "haversine"})
	if err == nil || !strings.Contains(err.Error(), "no existe") {
		t.Fatalf("esperaba un error de tabla inexistente, obtuve %v", err)
	}
}

// TestStoreConColumnaPoint: el panel de mapa también funciona si la tabla
// guarda las coordenadas en una columna POINT en vez de latitud/longitud.
func TestStoreConColumnaPoint(t *testing.T) {
	db, err := sql.Open("test", sql.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	for _, q := range []string{
		"CREATE TABLE ubicaciones (id INT PRIMARY KEY, nombre VARCHAR(40), tipo VARCHAR(20), ubicacion POINT)",
		`INSERT INTO ubicaciones VALUES
			(1, 'Tienda Centro',  'tienda', POINT(-12.0432, -77.0282)),
			(2, 'Tienda Norte',   'tienda', POINT(-12.0200, -77.0282)),
			(3, 'Tienda Cercana', 'tienda', POINT(-12.0500, -77.0282))`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	resp, err := NewStore(db, "ubicaciones").SearchKNN(KNNRequest{
		Latitude: -12.0432, Longitude: -77.0282, K: 2, Metric: "haversine",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 2 || resp.Results[0].Name != "Tienda Centro" || resp.Results[1].Name != "Tienda Cercana" {
		t.Fatalf("k-NN sobre columna POINT: %+v", resp.Results)
	}
}
