package integration

import (
	"path/filepath"
	"testing"

	"github.com/dbms-go/v2/dbms/lib/sql"
)

func newTestDB(t *testing.T) *sql.Database {
	t.Helper()
	db, err := sql.Open(t.TempDir())
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func exec(t *testing.T, db *sql.Database, query string) *sql.Result {
	t.Helper()
	res, err := db.Execute(query)
	if err != nil {
		t.Fatalf("Execute %q: %v", query, err)
	}
	return res
}

func TestExecutorCreateInsertSelectWhereOrderBy(t *testing.T) {
	db := newTestDB(t)

	exec(t, db, "CREATE TABLE users (id INT, name STRING);")

	exec(t, db, "INSERT INTO users (id, name) VALUES (1, 'ana'), (3, 'carol'), (2, 'bob');")

	res := exec(t, db, "SELECT * FROM users WHERE id >= 2 ORDER BY id DESC;")
	wantCols := []string{"id", "name"}
	want := [][]any{{3, "carol"}, {2, "bob"}}
	if len(res.Columns) != 2 || res.Columns[0] != wantCols[0] || res.Columns[1] != wantCols[1] {
		t.Fatalf("columns = %v, want %v", res.Columns, wantCols)
	}
	if len(res.Rows) != len(want) {
		t.Fatalf("rows = %v, want %v", res.Rows, want)
	}
	for i, row := range res.Rows {
		if len(row) != 2 || row[0] != want[i][0] || row[1] != want[i][1] {
			t.Fatalf("row %d = %v, want %v", i, row, want[i])
		}
	}
}

func TestExecutorFullScanUsesIndexOrder(t *testing.T) {
	db := newTestDB(t)
	exec(t, db, "CREATE TABLE t (id INT, name STRING);")
	exec(t, db, "INSERT INTO t (id, name) VALUES (3, 'tres'), (1, 'uno'), (2, 'dos');")

	res := exec(t, db, "SELECT * FROM t;")
	if len(res.Rows) != 3 {
		t.Fatalf("rows = %v, want 3", res.Rows)
	}
	for i, wantID := range []int{1, 2, 3} {
		if res.Rows[i][0] != wantID {
			t.Fatalf("fila %d id = %v, want %d (orden del índice)", i, res.Rows[i][0], wantID)
		}
	}
}

func TestExecutorInsertWithoutColumnList(t *testing.T) {
	db := newTestDB(t)
	exec(t, db, "CREATE TABLE t (id INT, name STRING);")
	exec(t, db, "INSERT INTO t VALUES (1, 'por-posicion');")

	res := exec(t, db, "SELECT * FROM t;")
	if len(res.Rows) != 1 || res.Rows[0][1] != "por-posicion" {
		t.Fatalf("rows = %v", res.Rows)
	}
}

func TestExecutorSelectProjection(t *testing.T) {
	db := newTestDB(t)
	exec(t, db, "CREATE TABLE t (id INT, name STRING);")
	exec(t, db, "INSERT INTO t VALUES (1, 'ana'), (2, 'bob');")

	res := exec(t, db, "SELECT name FROM t ORDER BY id;")
	if len(res.Columns) != 1 || res.Columns[0] != "name" {
		t.Fatalf("columns = %v, want [name]", res.Columns)
	}
	if len(res.Rows) != 2 || res.Rows[0][0] != "ana" || res.Rows[1][0] != "bob" {
		t.Fatalf("rows = %v, want [ana bob]", res.Rows)
	}
}

func TestExecutorPointLookup(t *testing.T) {
	db := newTestDB(t)
	exec(t, db, "CREATE TABLE t (id INT, name STRING);")
	exec(t, db, "INSERT INTO t VALUES (1, 'ana'), (2, 'bob'), (3, 'carol');")

	res := exec(t, db, "SELECT * FROM t WHERE id = 2;")
	if len(res.Rows) != 1 || res.Rows[0][0] != 2 || res.Rows[0][1] != "bob" {
		t.Fatalf("rows = %v, want [{2 bob}]", res.Rows)
	}
}

func TestExecutorOrderByNonKeyColumn(t *testing.T) {
	db := newTestDB(t)
	exec(t, db, "CREATE TABLE t (id INT, p DECIMAL);")
	exec(t, db, "INSERT INTO t VALUES (1, 1.5), (2, 3.75), (3, 2.25);")

	res := exec(t, db, "SELECT * FROM t ORDER BY p DESC;")
	want := [][]any{{2, 3.75}, {3, 2.25}, {1, 1.5}}
	if len(res.Rows) != 3 {
		t.Fatalf("rows = %v, want %v", res.Rows, want)
	}
	for i, row := range res.Rows {
		if row[1] != want[i][1] {
			t.Fatalf("row %d = %v, want %v", i, row, want[i])
		}
	}
}

func TestExecutorDeleteWhere(t *testing.T) {
	db := newTestDB(t)
	exec(t, db, "CREATE TABLE t (id INT, name STRING);")
	exec(t, db, "INSERT INTO t VALUES (1, 'ana'), (2, 'bob'), (3, 'carol'), (4, 'dave');")

	del := exec(t, db, "DELETE FROM t WHERE id = 2;")
	if del.Affected != 1 {
		t.Fatalf("DELETE point: Affected = %d, want 1", del.Affected)
	}

	del = exec(t, db, "DELETE FROM t WHERE id >= 3;")
	if del.Affected != 2 {
		t.Fatalf("DELETE range: Affected = %d, want 2", del.Affected)
	}

	res := exec(t, db, "SELECT * FROM t;")
	if len(res.Rows) != 1 || res.Rows[0][0] != 1 {
		t.Fatalf("tras borrados quedan %v, want [{1 ana}]", res.Rows)
	}
}

func TestExecutorDeleteRequiresWhere(t *testing.T) {
	db := newTestDB(t)
	exec(t, db, "CREATE TABLE t (id INT, name STRING);")
	if _, err := db.Execute("DELETE FROM t;"); err == nil {
		t.Fatal("DELETE sin WHERE debería fallar")
	}
}

func TestExecutorErrors(t *testing.T) {
	db := newTestDB(t)
	exec(t, db, "CREATE TABLE t (id INT, name STRING);")

	if _, err := db.Execute("CREATE TABLE t (x INT);"); err == nil {
		t.Fatal("CREATE duplicado debería fallar")
	}
	if _, err := db.Execute("SELECT * FROM nope;"); err == nil {
		t.Fatal("SELECT de tabla inexistente debería fallar")
	}
	if _, err := db.Execute("INSERT INTO t (id, missing) VALUES (1, 2);"); err == nil {
		t.Fatal("INSERT con columna desconocida debería fallar")
	}
	if _, err := db.Execute("CREATE TABLE bad (name STRING);"); err == nil {
		t.Fatal("CREATE sin clave INT debería fallar")
	}
	if _, err := db.Execute("SELECT * FROM t WHERE id = 'x';"); err == nil {
		t.Fatal("WHERE contra string debería fallar")
	}
	exec(t, db, "SELECT * FROM t WHERE id < 10;") // rangos válidos pasan
}

func TestExecutorDataPersistsWithoutCatalog(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")

	db, err := sql.Open(dir)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	exec(t, db, "CREATE TABLE users (id INT, name STRING);")
	exec(t, db, "INSERT INTO users VALUES (1, 'ana'), (2, 'bob');")
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close: %v", err)
	}

	// El catálogo vive en memoria: hay que re-crear el esquema; los datos
	// sobreviven en el HeapFile y el índice se reconstruye al abrirlo.
	reopened, err := sql.Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { reopened.Close() })
	exec(t, reopened, "CREATE TABLE users (id INT, name STRING);")

	res := exec(t, reopened, "SELECT * FROM users ORDER BY id DESC;")
	want := [][]any{{2, "bob"}, {1, "ana"}}
	if len(res.Rows) != 2 {
		t.Fatalf("rows = %v, want %v", res.Rows, want)
	}
	for i, row := range res.Rows {
		if row[0] != want[i][0] || row[1] != want[i][1] {
			t.Fatalf("row %d = %v, want %v", i, row, want[i])
		}
	}
}