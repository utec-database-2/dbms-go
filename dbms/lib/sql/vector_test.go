package sql

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func floatRow(t *testing.T, res *Result, row, col int) float64 {
	t.Helper()
	if row >= len(res.Rows) || col >= len(res.Rows[row]) {
		t.Fatalf("fila/columna fuera de rango: %#v", res.Rows)
	}
	v, ok := res.Rows[row][col].(float64)
	if !ok {
		t.Fatalf("se esperaba float64 en [%d][%d], hay %T", row, col, res.Rows[row][col])
	}
	return v
}

func TestIVFVectorKNN(t *testing.T) {
	dir := t.TempDir()
	e, err := Open("app", Options{Dir: dir})
	if err != nil {
		t.Fatalf("apertura: %v", err)
	}
	defer func() {
		if e != nil {
			e.Close()
		}
	}()

	run(t, e, "CREATE TABLE embeddings (id INT PRIMARY KEY, v VECTOR(3))")
	for i := 1; i <= 8; i++ {
		run(t, e, "INSERT INTO embeddings (id, v) VALUES ("+
			itoa(i)+", VECTOR("+itoa(i)+", 0, 0))")
	}
	// También se admite el literal textual, útil para clientes que envían el
	// vector como JSON.
	run(t, e, `INSERT INTO embeddings (id, v) VALUES (9, '[9, 0, 0]')`)

	res := run(t, e, "CREATE INDEX idx_v ON embeddings USING IVF (v) WITH (nlist=2, nprobe=2, metric='euclidean')")
	if !strings.Contains(res.Message, "ivf") {
		t.Fatalf("CREATE INDEX no creó IVF: %s", res.Message)
	}

	res = run(t, e, "SELECT id FROM embeddings ORDER BY vector_distance(v, VECTOR(0, 0, 0)) LIMIT 3")
	if got := ids(res); !equalStrings(got, []string{"1", "2", "3"}) {
		t.Fatalf("kNN inicial: %v", got)
	}
	if !hasKind(res, StepIndexScan) || !strings.Contains(planText(res), "IVF") {
		t.Fatalf("el kNN no usó IVF:\n%s", planText(res))
	}

	// La distancia también se puede proyectar y comparar fuera del kNN.
	res = run(t, e, "SELECT vector_distance(v, VECTOR(0, 0, 0)) FROM embeddings WHERE id = 2")
	if d := floatRow(t, res, 0, 0); d != 2 {
		t.Fatalf("distancia de la fila 2: %v", d)
	}

	// UPDATE reindexa: la fila 2 (segundo RID del heap) se aleja y deja de
	// estar en el top-3.
	run(t, e, "UPDATE embeddings SET v = VECTOR(100, 0, 0) WHERE id = 2")
	res = run(t, e, "SELECT id FROM embeddings ORDER BY vector_distance(v, VECTOR(0, 0, 0)) LIMIT 3")
	if got := ids(res); !equalStrings(got, []string{"1", "3", "4"}) {
		t.Fatalf("kNN tras UPDATE: %v", got)
	}

	// DELETE reindexa.
	run(t, e, "DELETE FROM embeddings WHERE id = 3")
	res = run(t, e, "SELECT id FROM embeddings ORDER BY vector_distance(v, VECTOR(0, 0, 0)) LIMIT 3")
	if got := ids(res); !equalStrings(got, []string{"1", "4", "5"}) {
		t.Fatalf("kNN tras DELETE: %v", got)
	}

	// Dimensión incorrecta: falla en ingesta, no en mitad del índice.
	if _, err := e.Exec("INSERT INTO embeddings (id, v) VALUES (99, VECTOR(1, 2))"); err == nil {
		t.Fatal("aceptó un vector de dimensión incorrecta")
	}

	// Reapertura: el catálogo guarda kind/nlist/nprobe/metric y el IVF se
	// reentrena desde los vectores persistidos en la tabla.
	e.Close()
	e = nil
	e2, err := Open("app", Options{Dir: dir})
	if err != nil {
		t.Fatalf("reapertura: %v", err)
	}
	defer e2.Close()
	e = e2

	res = run(t, e, "SELECT id FROM embeddings ORDER BY vector_distance(v, VECTOR(0, 0, 0)) LIMIT 3")
	if got := ids(res); !equalStrings(got, []string{"1", "4", "5"}) {
		t.Fatalf("kNN tras reapertura: %v", got)
	}
	if !strings.Contains(planText(res), "IVF") {
		t.Fatalf("la reapertura perdió el tipo IVF:\n%s", planText(res))
	}
}

func TestHNSWVectorKNN(t *testing.T) {
	dir := t.TempDir()
	e, err := Open("app", Options{Dir: dir})
	if err != nil {
		t.Fatalf("apertura: %v", err)
	}
	run(t, e, "CREATE TABLE embeddings (id INT PRIMARY KEY, v VECTOR(3))")
	run(t, e, "INSERT INTO embeddings VALUES (1, VECTOR(1, 0, 0)), (2, VECTOR(2, 0, 0)), (3, VECTOR(3, 0, 0)), (4, VECTOR(4, 0, 0))")

	res := run(t, e, "CREATE INDEX idx_h ON embeddings USING HNSW (v) WITH (ef_construction=64, ef_search=32, m=16, ml=0.5, metric='euclidean')")
	if !strings.Contains(res.Message, "hnsw") {
		t.Fatalf("CREATE INDEX no creó HNSW: %s", res.Message)
	}

	res = run(t, e, "SELECT id FROM embeddings ORDER BY vector_distance(v, VECTOR(0, 0, 0)) LIMIT 2")
	if got := ids(res); !equalStrings(got, []string{"1", "2"}) {
		t.Fatalf("kNN HNSW inicial: %v", got)
	}
	if !strings.Contains(planText(res), "HNSW") {
		t.Fatalf("el kNN no usó HNSW:\n%s", planText(res))
	}

	run(t, e, "UPDATE embeddings SET v = VECTOR(100, 0, 0) WHERE id = 2")
	run(t, e, "DELETE FROM embeddings WHERE id = 3")
	res = run(t, e, "SELECT id FROM embeddings ORDER BY vector_distance(v, VECTOR(0, 0, 0)) LIMIT 2")
	if got := ids(res); !equalStrings(got, []string{"1", "4"}) {
		t.Fatalf("kNN HNSW tras DML: %v", got)
	}

	e.Close()
	e, err = Open("app", Options{Dir: dir})
	if err != nil {
		t.Fatalf("reapertura: %v", err)
	}
	defer e.Close()
	res = run(t, e, "SELECT id FROM embeddings ORDER BY vector_distance(v, VECTOR(0, 0, 0)) LIMIT 2")
	if got := ids(res); !equalStrings(got, []string{"1", "4"}) {
		t.Fatalf("kNN HNSW tras reapertura: %v", got)
	}
}

func TestIVFCosineYErrorDeMetrica(t *testing.T) {
	e := newTestEngine(t, Options{})
	run(t, e, "CREATE TABLE docs_vec (id INT PRIMARY KEY, v VECTOR(2))")
	run(t, e, "INSERT INTO docs_vec VALUES (1, VECTOR(1, 0)), (2, VECTOR(0, 1)), (3, VECTOR(2, 0))")
	run(t, e, "CREATE INDEX idx_cos ON docs_vec USING IVF (v) WITH (nlist=2, nprobe=2, metric='cosine')")

	res := run(t, e, "SELECT id FROM docs_vec ORDER BY vector_distance(v, VECTOR(1, 0), 'cosine') LIMIT 2")
	got := ids(res)
	if len(got) != 2 || got[0] != "1" {
		t.Fatalf("kNN coseno: %v", got)
	}
	// La fila 3 es el mismo vector escalado: el coseno la considera casi
	// empatada con la 1. No imponemos el orden entre ambas, solo que la 2
	// (ortogonal) quede fuera del top-2.
	for _, id := range got {
		if id == "2" {
			t.Fatalf("el kNN coseno incluyó el vector ortogonal: %v", got)
		}
	}

	if _, err := e.Exec("SELECT id FROM docs_vec ORDER BY vector_distance(v, VECTOR(1, 0), 'euclidean') LIMIT 2"); err == nil {
		t.Fatal("aceptó una métrica distinta a la del índice")
	}
}

func TestIVFEnTablaVaciaYTAgrupada(t *testing.T) {
	e := newTestEngine(t, Options{})
	run(t, e, "CREATE TABLE vacia (id INT PRIMARY KEY, v VECTOR(2))")
	run(t, e, "CREATE INDEX idx_vacia ON vacia USING IVF (v) WITH (nlist=4, nprobe=4)")

	// Antes de alcanzar nlist el índice usa fuerza bruta sobre pending.
	run(t, e, "INSERT INTO vacia VALUES (1, VECTOR(1, 0))")
	res := run(t, e, "SELECT id FROM vacia ORDER BY vector_distance(v, VECTOR(0, 0)) LIMIT 1")
	if got := ids(res); !equalStrings(got, []string{"1"}) {
		t.Fatalf("tabla vacía con un vector: %v", got)
	}
	for i := 2; i <= 5; i++ {
		run(t, e, "INSERT INTO vacia VALUES ("+itoa(i)+", VECTOR("+itoa(i)+", 0))")
	}
	res = run(t, e, "SELECT id FROM vacia ORDER BY vector_distance(v, VECTOR(0, 0)) LIMIT 2")
	if got := ids(res); !equalStrings(got, []string{"1", "2"}) {
		t.Fatalf("tabla vacía tras entrenar: %v", got)
	}

	// Tabla agrupada: la referencia interna es la PK, no un RID.
	run(t, e, "CREATE TABLE agrupada CLUSTERED BY (id) (id INT PRIMARY KEY, v VECTOR(2))")
	run(t, e, "INSERT INTO agrupada VALUES (1, VECTOR(1, 0)), (2, VECTOR(2, 0)), (3, VECTOR(3, 0)), (4, VECTOR(4, 0))")
	run(t, e, "CREATE INDEX idx_agrupada ON agrupada USING IVF (v) WITH (nlist=2, nprobe=2)")
	run(t, e, "UPDATE agrupada SET v = VECTOR(100, 0) WHERE id = 2")
	run(t, e, "DELETE FROM agrupada WHERE id = 3")
	res = run(t, e, "SELECT id FROM agrupada ORDER BY vector_distance(v, VECTOR(0, 0)) LIMIT 2")
	if got := ids(res); !equalStrings(got, []string{"1", "4"}) {
		t.Fatalf("IVF agrupado: %v", got)
	}
}

func TestIVFConClavePrimariaCompuesta(t *testing.T) {
	e := newTestEngine(t, Options{})
	// La PK usa las columnas 1 y 2: ref.pk ya viene con esa tupla y no debe
	// volver a reordenarse al desindexar.
	run(t, e, "CREATE CLUSTERED TABLE comp (x INT, a INT, b INT, v VECTOR(2), PRIMARY KEY (a, b))")
	run(t, e, "INSERT INTO comp VALUES (0, 1, 10, VECTOR(1, 0)), (0, 2, 20, VECTOR(2, 0)), (0, 3, 30, VECTOR(3, 0))")
	run(t, e, "CREATE INDEX idx_comp_v ON comp USING IVF (v) WITH (nlist=2, nprobe=2)")

	run(t, e, "UPDATE comp SET v = VECTOR(100, 0) WHERE b = 10")
	// La igualdad sobre la primera columna de una PK compuesta no puede usar
	// el B+ agrupado (la clave está incompleta), pero el DELETE debe funcionar.
	run(t, e, "DELETE FROM comp WHERE a = 2")
	res := run(t, e, "SELECT a FROM comp ORDER BY vector_distance(v, VECTOR(0, 0)) LIMIT 2")
	if got := ids(res); !equalStrings(got, []string{"3", "1"}) {
		t.Fatalf("IVF con PK compuesta: %v", got)
	}
}

func TestIVFCatalogoYRollback(t *testing.T) {
	dir := t.TempDir()
	e, err := Open("app", Options{Dir: dir})
	if err != nil {
		t.Fatalf("apertura: %v", err)
	}
	defer e.Close()

	run(t, e, "CREATE TABLE docs (id INT PRIMARY KEY, v VECTOR(2))")
	run(t, e, "INSERT INTO docs VALUES (1, VECTOR(1, 0)), (2, VECTOR(2, 0)), (3, VECTOR(3, 0))")
	run(t, e, "CREATE INDEX idx_docs_v ON docs USING IVF (v) WITH (nlist=2, nprobe=2, metric='euclidean')")

	buf, err := os.ReadFile(filepath.Join(dir, catalogFile))
	if err != nil {
		t.Fatalf("catálogo: %v", err)
	}
	var disk catalogDisk
	if err := json.Unmarshal(buf, &disk); err != nil {
		t.Fatalf("catálogo ilegible: %v", err)
	}
	found := false
	for _, tdisk := range disk.Tables {
		for _, idx := range tdisk.Indexes {
			if idx.Name == "idx_docs_v" {
				found = true
				if idx.Kind != "ivf" || idx.NList != 2 || idx.NProbe != 2 || idx.Metric != "euclidean" {
					t.Fatalf("metadatos IVF persistidos: %#v", idx)
				}
			}
		}
	}
	if !found {
		t.Fatal("el catálogo no guardó el índice IVF")
	}

	// El WAL debe poder codificar y deshacer vectores.
	run(t, e, "BEGIN")
	run(t, e, "INSERT INTO docs VALUES (4, VECTOR(4, 0))")
	run(t, e, "ROLLBACK")
	res := run(t, e, "SELECT COUNT(*) FROM docs")
	if len(res.Rows) != 1 || res.Rows[0][0] != int64(3) {
		t.Fatalf("el ROLLBACK no deshizo el INSERT vectorial: %#v", res.Rows)
	}
}

func TestVectorDistanceSinIndice(t *testing.T) {
	e := newTestEngine(t, Options{})
	run(t, e, "CREATE TABLE raw (id INT PRIMARY KEY, v VECTOR(2))")
	run(t, e, "INSERT INTO raw VALUES (1, VECTOR(3, 4)), (2, VECTOR(0, 0))")
	res := run(t, e, "SELECT id, vector_distance(v, VECTOR(0, 0)) AS d FROM raw ORDER BY d")
	if len(res.Rows) != 2 || res.Rows[0][0] != int32(2) {
		t.Fatalf("orden por distancia sin índice: %#v", res.Rows)
	}
	if d := floatRow(t, res, 1, 1); d != 5 {
		t.Fatalf("distancia euclidiana: %v", d)
	}

	res = run(t, e, "SELECT id FROM raw WHERE vector_distance(v, VECTOR(0, 0)) < 4")
	if got := ids(res); !equalStrings(got, []string{"2"}) {
		t.Fatalf("filtro por vector_distance: %v", got)
	}
}
