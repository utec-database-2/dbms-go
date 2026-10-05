package sql

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dbms-go/v2/dbms/lib/storage"
)

func run(t *testing.T, e *Engine, q string) *Result {
	t.Helper()
	res, err := e.Exec(q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return res
}

func planText(res *Result) string {
	var b strings.Builder
	for _, s := range res.Plan {
		fmt.Fprintf(&b, "  %-14s %s (rows=%d)\n", s.Kind, s.Detail, s.Rows)
	}
	return b.String()
}

func kinds(res *Result) []StepKind {
	out := make([]StepKind, 0, len(res.Plan))
	for _, s := range res.Plan {
		out = append(out, s.Kind)
	}
	return out
}

func hasKind(res *Result, k StepKind) bool {
	for _, s := range res.Plan {
		if s.Kind == k {
			return true
		}
	}
	return false
}

func newTestEngine(t *testing.T, opt Options) *Engine {
	t.Helper()
	if opt.Dir == "" {
		opt.Dir = t.TempDir()
	}
	e := New("test", opt)
	t.Cleanup(e.Close)
	return e
}

func seedUsers(t *testing.T, e *Engine) {
	t.Helper()
	run(t, e, "CREATE TABLE users (id INT PRIMARY KEY, name VARCHAR(32), age INT)")
	for _, q := range []string{
		"INSERT INTO users (id, name, age) VALUES (1, 'ana', 30)",
		"INSERT INTO users (id, name, age) VALUES (2, 'bob', 25)",
		"INSERT INTO users (id, name, age) VALUES (3, 'cid', 41)",
		"INSERT INTO users (id, name, age) VALUES (4, 'dan', 25)",
	} {
		run(t, e, q)
	}
}

// TestUnclusteredHeapBPlus recorre el camino completo de una tabla no agrupada:
// heap file para las filas, B+ no agrupado para el acceso por clave y por rango.
func TestUnclusteredHeapBPlus(t *testing.T) {
	e := newTestEngine(t, Options{})
	seedUsers(t, e)

	tbl, _ := e.cat.get("users")
	if tbl.Clustered {
		t.Fatal("users no debería ser agrupada")
	}
	if _, ok := tbl.IndexOnColumn(0); !ok {
		t.Fatal("falta el índice de la clave primaria")
	}

	res := run(t, e, "SELECT * FROM users")
	if len(res.Rows) != 4 {
		t.Fatalf("SELECT * devolvió %d filas", len(res.Rows))
	}

	// Igualdad: el planner debe elegir el índice.
	res = run(t, e, "SELECT name FROM users WHERE id = 3")
	if len(res.Rows) != 1 || res.Rows[0][0] != "cid" {
		t.Fatalf("id = 3: %#v", res.Rows)
	}
	if !hasKind(res, StepIndexSeek) {
		t.Fatalf("se esperaba index-seek:\n%s", planText(res))
	}

	// Rango: barrido del B+.
	res = run(t, e, "SELECT id FROM users WHERE id >= 2 AND id <= 3")
	if len(res.Rows) != 2 {
		t.Fatalf("rango devolvió %d filas:\n%s", len(res.Rows), planText(res))
	}

	// Sin condición indexable: recorrido secuencial.
	res = run(t, e, "SELECT * FROM users WHERE name = 'bob'")
	if len(res.Rows) != 1 {
		t.Fatalf("name = 'bob' devolvió %d filas", len(res.Rows))
	}
	if !hasKind(res, StepScan) {
		t.Fatalf("se esperaba seq-scan:\n%s", planText(res))
	}
	t.Log("\n" + planText(res))

	// UPDATE y DELETE mantienen índices y heap coherentes.
	if r := run(t, e, "UPDATE users SET age = 26 WHERE id = 4"); r.Affected != 1 {
		t.Fatalf("UPDATE afectan %d", r.Affected)
	}
	res = run(t, e, "SELECT age FROM users WHERE id = 4")
	if len(res.Rows) != 1 || res.Rows[0][0] != int32(26) {
		t.Fatalf("tras UPDATE: %#v", res.Rows)
	}
	if r := run(t, e, "DELETE FROM users WHERE id = 1"); r.Affected != 1 {
		t.Fatalf("DELETE afecta %d", r.Affected)
	}
	res = run(t, e, "SELECT * FROM users")
	if len(res.Rows) != 3 {
		t.Fatalf("tras DELETE hay %d filas", len(res.Rows))
	}
	if r := run(t, e, "SELECT * FROM users WHERE id = 1"); len(r.Rows) != 0 {
		t.Fatalf("la fila borrada sigue en el índice: %#v", r.Rows)
	}
}

// TestClusteredSequential usa archivo secuencial + B+ agrupado, donde el orden
// físico lo impone la clave primaria.
func TestClusteredSequential(t *testing.T) {
	e := newTestEngine(t, Options{})
	run(t, e, "CREATE CLUSTERED TABLE t (id INT PRIMARY KEY, v INT)")
	for _, q := range []string{
		"INSERT INTO t (id, v) VALUES (3, 30)",
		"INSERT INTO t (id, v) VALUES (1, 10)",
		"INSERT INTO t (id, v) VALUES (2, 20)",
	} {
		run(t, e, q)
	}

	tbl, _ := e.cat.get("t")
	if !tbl.Clustered || tbl.seq == nil {
		t.Fatal("t debería usar archivo secuencial")
	}

	// El almacenamiento agrupado devuelve las filas ordenadas por PK.
	res := run(t, e, "SELECT * FROM t")
	if len(res.Rows) != 3 {
		t.Fatalf("SELECT * devolvió %d filas", len(res.Rows))
	}
	for i, want := range []int32{1, 2, 3} {
		if res.Rows[i][0] != want {
			t.Fatalf("orden agrupado: fila %d = %v, se esperaba %v", i, res.Rows[i][0], want)
		}
	}

	res = run(t, e, "SELECT * FROM t WHERE id = 2")
	if len(res.Rows) != 1 || res.Rows[0][1] != int32(20) {
		t.Fatalf("id = 2: %#v", res.Rows)
	}
	if !hasKind(res, StepIndexSeek) {
		t.Fatalf("se esperaba index-seek:\n%s", planText(res))
	}

	if r := run(t, e, "UPDATE t SET v = 99 WHERE id = 2"); r.Affected != 1 {
		t.Fatalf("UPDATE afecta %d", r.Affected)
	}
	res = run(t, e, "SELECT v FROM t WHERE id = 2")
	if len(res.Rows) != 1 || res.Rows[0][0] != int32(99) {
		t.Fatalf("tras UPDATE: %#v", res.Rows)
	}
	if r := run(t, e, "DELETE FROM t WHERE id = 3"); r.Affected != 1 {
		t.Fatalf("DELETE afecta %d", r.Affected)
	}
	res = run(t, e, "SELECT * FROM t")
	if len(res.Rows) != 2 {
		t.Fatalf("tras DELETE hay %d filas", len(res.Rows))
	}
}

// TestClusteredSecondaryIndex comprueba el índice secundario sobre una tabla
// agrupada, cuya referencia a la fila es la clave primaria.
func TestClusteredSecondaryIndex(t *testing.T) {
	e := newTestEngine(t, Options{})
	run(t, e, "CREATE CLUSTERED TABLE t (id INT PRIMARY KEY, code VARCHAR(16))")
	for _, q := range []string{
		"INSERT INTO t (id, code) VALUES (1, 'aa')",
		"INSERT INTO t (id, code) VALUES (2, 'bb')",
	} {
		run(t, e, q)
	}
	res := run(t, e, "CREATE INDEX idx_t_code ON t (code)")
	if !strings.Contains(res.Message, "unclustered-bplus") {
		t.Fatalf("mensaje inesperado: %s", res.Message)
	}
	res = run(t, e, "SELECT * FROM t WHERE code = 'bb'")
	if len(res.Rows) != 1 || res.Rows[0][0] != int32(2) {
		t.Fatalf("código secundario: %#v\n%s", res.Rows, planText(res))
	}
	if r := run(t, e, "DELETE FROM t WHERE id = 1"); r.Affected != 1 {
		t.Fatalf("DELETE afecta %d", r.Affected)
	}
	res = run(t, e, "SELECT * FROM t WHERE code = 'aa'")
	if len(res.Rows) != 0 {
		t.Fatalf("el índice secundario no se limpió: %#v", res.Rows)
	}
}

// TestHashIndex comprueba el extendible hashing por igualdad.
func TestHashIndex(t *testing.T) {
	e := newTestEngine(t, Options{HashBucketSize: 2})
	run(t, e, "CREATE TABLE h (id INT PRIMARY KEY, email VARCHAR(32))")
	run(t, e, "CREATE INDEX hash_h_email ON h (email)")
	for i := 1; i <= 8; i++ {
		run(t, e, fmt.Sprintf("INSERT INTO h (id, email) VALUES (%d, 'u%d@x.com')", i, i))
	}
	tbl, _ := e.cat.get("h")
	idx, ok := tbl.Index("hash_h_email")
	if !ok {
		t.Fatal("no se creó el índice hash")
	}
	if idx.SupportsRange() {
		t.Fatal("el hash dinámico no debe soportar rangos")
	}
	if idx.Entries() != 8 {
		t.Fatalf("entradas = %d", idx.Entries())
	}
	res := run(t, e, "SELECT id FROM h WHERE email = 'u5@x.com'")
	if len(res.Rows) != 1 || res.Rows[0][0] != int32(5) {
		t.Fatalf("hash: %#v\n%s", res.Rows, planText(res))
	}
	if !strings.Contains(planText(res), "extendible-hash") {
		t.Fatalf("el plan no menciona el hash:\n%s", planText(res))
	}
}

// TestUniqueIndex rechaza claves repetidas.
func TestUniqueIndex(t *testing.T) {
	e := newTestEngine(t, Options{})
	run(t, e, "CREATE TABLE u (id INT PRIMARY KEY, email VARCHAR(32))")
	run(t, e, "CREATE UNIQUE INDEX idx_u_email ON u (email)")
	run(t, e, "INSERT INTO u (id, email) VALUES (1, 'a@x')")
	if _, err := e.Exec("INSERT INTO u (id, email) VALUES (2, 'a@x')"); err == nil {
		t.Fatal("se esperaba un error de clave duplicada")
	}
	if _, err := e.Exec("INSERT INTO u (id, email) VALUES (2, 'b@x')"); err != nil {
		t.Fatalf("clave distinta: %v", err)
	}
}

// TestExternalSortObligatorioAMultiplesRuns fuerza el camino de runs + k-way
// merge: el buffer es de 2 slots y hay 5 filas.
func TestExternalSortObligatorioAMultiplesRuns(t *testing.T) {
	e := newTestEngine(t, Options{SortBufferSlots: 2})
	run(t, e, "CREATE TABLE s (id INT PRIMARY KEY, v INT)")
	for i := 5; i >= 1; i-- {
		run(t, e, fmt.Sprintf("INSERT INTO s (id, v) VALUES (%d, %d)", i, (i*7)%5))
	}
	res := run(t, e, "SELECT * FROM s ORDER BY v ASC")
	if !hasKind(res, StepExternalSort) {
		t.Fatalf("falta external-sort:\n%s", planText(res))
	}
	if !strings.Contains(planText(res), "k-way merge") {
		t.Fatalf("se esperaba k-way merge con varios runs:\n%s", planText(res))
	}
	for i := 1; i < len(res.Rows); i++ {
		prev, cur := res.Rows[i-1][1], res.Rows[i][1]
		if compareAny(prev, cur) > 0 {
			t.Fatalf("orden incorrecto en %d: %v > %v (%#v)", i, prev, cur, res.Rows)
		}
	}

	res = run(t, e, "SELECT * FROM s ORDER BY v DESC")
	for i := 1; i < len(res.Rows); i++ {
		if compareAny(res.Rows[i-1][1], res.Rows[i][1]) < 0 {
			t.Fatalf("orden descendente incorrecto: %#v", res.Rows)
		}
	}
}

// TestDistinctLimitOffset cubre la parte final del pipeline.
func TestDistinctLimitOffset(t *testing.T) {
	e := newTestEngine(t, Options{})
	seedUsers(t, e)

	res := run(t, e, "SELECT DISTINCT age FROM users")
	if len(res.Rows) != 3 {
		t.Fatalf("DISTINCT devolvió %d filas: %#v", len(res.Rows), res.Rows)
	}

	res = run(t, e, "SELECT id FROM users ORDER BY id LIMIT 2")
	if len(res.Rows) != 2 || res.Rows[0][0] != int32(1) {
		t.Fatalf("LIMIT: %#v", res.Rows)
	}
	res = run(t, e, "SELECT id FROM users ORDER BY id LIMIT 2 OFFSET 2")
	if len(res.Rows) != 2 || res.Rows[0][0] != int32(3) {
		t.Fatalf("OFFSET: %#v", res.Rows)
	}
	res = run(t, e, "SELECT name AS nombre, age FROM users WHERE id = 1")
	if len(res.Columns) != 2 || res.Columns[0] != "nombre" {
		t.Fatalf("alias: %#v", res.Columns)
	}
	res = run(t, e, "SELECT age + 1 FROM users WHERE id = 1")
	if len(res.Rows) != 1 || res.Rows[0][0] != int64(31) {
		t.Fatalf("expresión proyectada: %#v", res.Rows)
	}
}

// TestTruncateAndDrop ejercita el resto del DDL.
func TestTruncateAndDrop(t *testing.T) {
	e := newTestEngine(t, Options{})
	seedUsers(t, e)
	res := run(t, e, "TRUNCATE TABLE users")
	if res.Affected != 4 {
		t.Fatalf("truncate afectó %d", res.Affected)
	}
	if r := run(t, e, "SELECT * FROM users"); len(r.Rows) != 0 {
		t.Fatalf("quedaron %d filas", len(r.Rows))
	}
	run(t, e, "DROP TABLE users")
	if _, err := e.Exec("SELECT * FROM users"); err == nil {
		t.Fatal("la tabla sigue existiendo")
	}
}

// TestSpatialNoImplementado comprueba que las funciones espaciales se parsean
// pero no se ejecutan: la base de datos espacial todavía no existe.
func TestSpatialNoImplementado(t *testing.T) {
	e := newTestEngine(t, Options{})
	run(t, e, "CREATE TABLE g (id INT PRIMARY KEY, geom VARCHAR(64))")
	run(t, e, "INSERT INTO g (id, geom) VALUES (1, 'POINT(0 0)')")
	_, err := e.Exec("SELECT ST_Area(geom) FROM g")
	if err == nil || !strings.Contains(err.Error(), "todavía no está implementada") {
		t.Fatalf("se esperaba 'no implementada', obtuve %v", err)
	}
}

// TestFilesExpone el estado físico de las tablas.
func TestFiles(t *testing.T) {
	e := newTestEngine(t, Options{})
	seedUsers(t, e)
	run(t, e, "CREATE CLUSTERED TABLE c (id INT PRIMARY KEY, v INT)")
	run(t, e, "INSERT INTO c (id, v) VALUES (1, 10)")

	files := e.Files()
	if len(files) != 2 {
		t.Fatalf("Files devolvió %d tablas", len(files))
	}
	for _, f := range files {
		t.Logf("%s clustered=%v rows=%d indexes=%v", f.Name, f.Clustered, f.Rows, f.Indexes)
		if f.Rows == 0 {
			t.Fatalf("%s sin filas", f.Name)
		}
	}
}

// TestErrorDeEsquema comprueba los mensajes de error del almacenamiento.
func TestErrorDeEsquema(t *testing.T) {
	e := newTestEngine(t, Options{})
	if _, err := e.Exec("CREATE TABLE x (a INT)"); err == nil {
		t.Fatal("una tabla sin clave primaria no puede ser agrupada, pero tampoco debe fallar aquí")
	}
	run(t, e, "CREATE TABLE x (a INT PRIMARY KEY, b VARCHAR(4))")
	if _, err := e.Exec("INSERT INTO x (a, b) VALUES (1, 'demasiado largo')"); err == nil {
		t.Fatal("se esperaba error por exceder VARCHAR(4)")
	}
	var tupleErr error = storage.ErrTupleTooLarge
	_ = tupleErr
}

// TestGroupByAgregados cubre la agrupación real con COUNT, SUM, AVG, MIN y MAX,
// además de HAVING y ORDER BY sobre el resultado agrupado.
func TestGroupByAgregados(t *testing.T) {
	e := newTestEngine(t, Options{})
	seedUsers(t, e)

	res := run(t, e, "SELECT age, COUNT(*) FROM users GROUP BY age ORDER BY age")
	if len(res.Rows) != 3 {
		t.Fatalf("grupos: %#v", res.Rows)
	}
	// ages: 30(1), 25(2), 41(1) -> ordenado: 25(2), 30(1), 41(1)
	if res.Rows[0][0] != int32(25) || res.Rows[0][1] != int64(2) {
		t.Fatalf("grupo 25: %#v", res.Rows[0])
	}
	if res.Rows[2][0] != int32(41) || res.Rows[2][1] != int64(1) {
		t.Fatalf("grupo 41: %#v", res.Rows[2])
	}
	if !hasKind(res, StepGroupBy) {
		t.Fatalf("falta group-by:\n%s", planText(res))
	}

	// Los grupos conservan el orden en que aparecen por primera vez.
	res = run(t, e, "SELECT age, SUM(age), MIN(age), MAX(age), AVG(age) FROM users GROUP BY age")
	if len(res.Rows) != 3 {
		t.Fatalf("agregados: %#v", res.Rows)
	}
	if res.Rows[0][0] != int32(30) || res.Rows[0][1] != int64(30) ||
		res.Rows[0][2] != int32(30) || res.Rows[0][3] != int32(30) || res.Rows[0][4] != float64(30) {
		t.Fatalf("agregados del grupo 30: %#v", res.Rows[0])
	}
	if res.Rows[1][0] != int32(25) || res.Rows[1][1] != int64(50) || res.Rows[1][4] != float64(25) {
		t.Fatalf("agregados del grupo 25: %#v", res.Rows[1])
	}

	res = run(t, e, "SELECT age FROM users GROUP BY age HAVING COUNT(*) > 1")
	if len(res.Rows) != 1 || res.Rows[0][0] != int32(25) {
		t.Fatalf("HAVING: %#v", res.Rows)
	}

	res = run(t, e, "SELECT COUNT(*) FROM users")
	if len(res.Rows) != 1 || res.Rows[0][0] != int64(4) {
		t.Fatalf("COUNT(*): %#v", res.Rows)
	}
}
