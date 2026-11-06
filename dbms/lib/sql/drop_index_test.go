package sql

import (
	"strings"
	"testing"
)

// TestDropIndexPorNombre comprueba que el registro global resuelve la tabla
// dueña y que el índice desaparece también tras reabrir el catálogo.
func TestDropIndexPorNombre(t *testing.T) {
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

	run(t, e, "CREATE TABLE t (id INT PRIMARY KEY, name VARCHAR(16))")
	run(t, e, "INSERT INTO t (id, name) VALUES (1, 'ana'), (2, 'bob')")
	run(t, e, "CREATE INDEX idx_t_name ON t(name)")

	tbl, ok := e.cat.get("t")
	if !ok {
		t.Fatal("no está la tabla t")
	}
	if _, ok := tbl.Index("idx_t_name"); !ok {
		t.Fatal("no se creó idx_t_name")
	}

	res := run(t, e, "DROP INDEX idx_t_name")
	if !strings.Contains(res.Message, "idx_t_name") || !strings.Contains(res.Message, "eliminado") {
		t.Fatalf("mensaje inesperado: %s", res.Message)
	}
	if _, ok := tbl.Index("idx_t_name"); ok {
		t.Fatal("el índice sigue en la tabla")
	}
	if got := tbl.IndexNames(); len(got) != 1 || got[0] != "pk_t" {
		t.Fatalf("índices tras DROP INDEX: %#v", got)
	}

	// La consulta sigue siendo correcta (ahora por recorrido).
	res = run(t, e, "SELECT id FROM t WHERE name = 'bob'")
	if len(res.Rows) != 1 || res.Rows[0][0] != int32(2) {
		t.Fatalf("consulta tras eliminar el índice: %#v", res.Rows)
	}

	e.Close()
	e = nil

	e2, err := Open("app", Options{Dir: dir})
	if err != nil {
		t.Fatalf("reapertura: %v", err)
	}
	defer e2.Close()
	tbl2, ok := e2.cat.get("t")
	if !ok {
		t.Fatal("no se reabrió t")
	}
	if _, ok := tbl2.Index("idx_t_name"); ok {
		t.Fatal("el índice reapareció tras reabrir el catálogo")
	}
	// El nombre queda libre para otra tabla.
	run(t, e2, "CREATE TABLE otra (id INT PRIMARY KEY, name VARCHAR(16))")
	run(t, e2, "CREATE INDEX idx_t_name ON otra(name)")
}

func TestDropIndexIfExistsYErrores(t *testing.T) {
	e := newTestEngine(t, Options{})
	run(t, e, "CREATE TABLE t (id INT PRIMARY KEY, name VARCHAR(16))")

	res := run(t, e, "DROP INDEX IF EXISTS no_existe")
	if !strings.Contains(res.Message, "no existe") {
		t.Fatalf("mensaje de IF EXISTS: %s", res.Message)
	}
	if _, err := e.Exec("DROP INDEX no_existe"); err == nil {
		t.Fatal("DROP INDEX de un índice inexistente debía fallar")
	}

	// El índice primario pertenece al esquema de la tabla y no se puede quitar
	// por separado.
	if _, err := e.Exec("DROP INDEX pk_t"); err == nil {
		t.Fatal("permitió eliminar el índice primario")
	}

	// Tampoco el B+ agrupado de una tabla secuencial.
	run(t, e, "CREATE TABLE c CLUSTERED BY (id) (id INT PRIMARY KEY, val INT)")
	if _, err := e.Exec("DROP INDEX pk_c"); err == nil {
		t.Fatal("permitió eliminar el índice agrupado")
	}
}

// TestDropIndexAmbiguedadGlobal documenta que el nombre de índice no es único
// entre tablas: si hay dos dueños, DROP INDEX no elige uno al azar.
func TestDropIndexAmbiguedadGlobal(t *testing.T) {
	e := newTestEngine(t, Options{})
	run(t, e, "CREATE TABLE a (id INT PRIMARY KEY, x INT)")
	run(t, e, "CREATE TABLE b (id INT PRIMARY KEY, x INT)")
	run(t, e, "CREATE INDEX idx_x ON a(x)")
	run(t, e, "CREATE INDEX idx_x ON b(x)")

	_, err := e.Exec("DROP INDEX idx_x")
	if err == nil {
		t.Fatal("DROP INDEX debía rechazar un nombre ambiguo")
	}
	if !strings.Contains(err.Error(), "varias tablas") {
		t.Fatalf("error inesperado: %v", err)
	}
	if owners := e.indexOwners("idx_x"); len(owners) != 2 {
		t.Fatalf("el registro global tiene %d dueños, esperaba 2", len(owners))
	}

	// ON tabla deshace la ambigüedad y elimina solo la copia indicada.
	res := run(t, e, "DROP INDEX idx_x ON b")
	if !strings.Contains(res.Message, "idx_x") || !strings.Contains(res.Message, "b") {
		t.Fatalf("mensaje inesperado: %s", res.Message)
	}
	if owners := e.indexOwners("idx_x"); len(owners) != 1 || owners[0].Name() != "a" {
		t.Fatalf("el registro global quedó mal: %#v", owners)
	}

	// Al desaparecer el dueño b, el nombre vuelve a ser inequívoco.
	run(t, e, "DROP TABLE b")
	res = run(t, e, "DROP INDEX idx_x")
	if !strings.Contains(res.Message, "idx_x") {
		t.Fatalf("mensaje inesperado: %s", res.Message)
	}
	if owners := e.indexOwners("idx_x"); len(owners) != 0 {
		t.Fatalf("el registro global no se limpió: %#v", owners)
	}
}

func TestDropIndexIVF(t *testing.T) {
	e := newTestEngine(t, Options{})
	run(t, e, "CREATE TABLE docs (id INT PRIMARY KEY, v VECTOR(2))")
	run(t, e, "INSERT INTO docs VALUES (1, VECTOR(1, 0)), (2, VECTOR(2, 0)), (3, VECTOR(3, 0))")
	run(t, e, "CREATE INDEX idx_docs_v ON docs USING IVF (v) WITH (nlist=2, nprobe=2)")

	res := run(t, e, "DROP INDEX idx_docs_v")
	if !strings.Contains(res.Message, "idx_docs_v") {
		t.Fatalf("mensaje inesperado: %s", res.Message)
	}
	res = run(t, e, "SELECT id FROM docs ORDER BY vector_distance(v, VECTOR(0, 0)) LIMIT 2")
	if len(res.Rows) != 2 {
		t.Fatalf("kNN sin índice: %#v", res.Rows)
	}
	if strings.Contains(planText(res), "IVF") {
		t.Fatalf("el plan sigue usando el IVF eliminado:\n%s", planText(res))
	}
}
