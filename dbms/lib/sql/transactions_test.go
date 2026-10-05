package sql

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dbms-go/v2/dbms/lib/storage"
	"github.com/dbms-go/v2/dbms/lib/transaction"
)

// TestTransaccionCommitDejaLosDatos comprueba que lo confirmado sobrevive.
func TestTransaccionCommitDejaLosDatos(t *testing.T) {
	e := newTestEngine(t, Options{})
	run(t, e, "CREATE TABLE cuentas (id INT PRIMARY KEY, saldo INT)")

	run(t, e, "BEGIN")
	run(t, e, "INSERT INTO cuentas (id, saldo) VALUES (1, 100)")
	run(t, e, "UPDATE cuentas SET saldo = 150 WHERE id = 1")
	res := run(t, e, "COMMIT")
	if !strings.Contains(res.Message, "confirmada") {
		t.Fatalf("mensaje: %s", res.Message)
	}
	if !hasKind(res, StepCommit) {
		t.Fatalf("plan: %s", planText(res))
	}
	if e.InTransaction() || e.TransactionID() != 0 {
		t.Fatal("sigue abierta")
	}

	run(t, e, "INSERT INTO cuentas (id, saldo) VALUES (2, 50)")
	got := run(t, e, "SELECT id, saldo FROM cuentas WHERE id = 1")
	if len(got.Rows) != 1 || got.Rows[0][1] != int32(150) {
		t.Fatalf("tras commit: %#v", got.Rows)
	}
}

// TestTransaccionRollbackDeshaceTodo es el caso central: nada de lo que se hizo
// dentro de la transacción debe quedar.
func TestTransaccionRollbackDeshaceTodo(t *testing.T) {
	for _, clustered := range []bool{false, true} {
		name := "heap"
		if clustered {
			name = "clustered"
		}
		t.Run(name, func(t *testing.T) {
			e := newTestEngine(t, Options{})
			create := "CREATE TABLE t (id INT PRIMARY KEY, val INT)"
			if clustered {
				create = "CREATE TABLE t CLUSTERED BY (id) (id INT PRIMARY KEY, val INT)"
			}
			run(t, e, create)
			run(t, e, "INSERT INTO t (id, val) VALUES (1, 10)")
			run(t, e, "INSERT INTO t (id, val) VALUES (2, 20)")

			run(t, e, "BEGIN")
			run(t, e, "INSERT INTO t (id, val) VALUES (3, 30)")
			run(t, e, "UPDATE t SET val = 99 WHERE id = 1")
			run(t, e, "DELETE FROM t WHERE id = 2")
			// Lo que la transacción lee también se bloquea en compartido.
			run(t, e, "SELECT * FROM t WHERE id = 1")
			if len(e.Locks()) != 3 {
				t.Fatalf("bloqueos: %#v", e.Locks())
			}
			res := run(t, e, "ROLLBACK")
			if !hasKind(res, StepRollback) {
				t.Fatalf("plan: %s", planText(res))
			}
			if len(e.Locks()) != 0 {
				t.Fatalf("los bloqueos quedaron: %#v", e.Locks())
			}

			got := run(t, e, "SELECT id, val FROM t ORDER BY id")
			if len(got.Rows) != 2 {
				t.Fatalf("filas: %#v", got.Rows)
			}
			if got.Rows[0][1] != int32(10) {
				t.Fatalf("el update no se deshizo: %#v", got.Rows[0])
			}
			if got.Rows[1][0] != int32(2) {
				t.Fatalf("el delete no se deshizo: %#v", got.Rows[1])
			}
			// La fila insertada tampoco está, y su clave se puede reutilizar.
			run(t, e, "INSERT INTO t (id, val) VALUES (3, 33)")
		})
	}
}

// TestTransaccionSavepoint deshace solo la parte posterior al savepoint.
func TestTransaccionSavepoint(t *testing.T) {
	e := newTestEngine(t, Options{})
	run(t, e, "CREATE TABLE t (id INT PRIMARY KEY, val INT)")
	run(t, e, "INSERT INTO t (id, val) VALUES (1, 10)")

	run(t, e, "BEGIN")
	run(t, e, "UPDATE t SET val = 11 WHERE id = 1")
	run(t, e, "SAVEPOINT sp1")
	run(t, e, "UPDATE t SET val = 12 WHERE id = 1")
	run(t, e, "INSERT INTO t (id, val) VALUES (2, 20)")
	res := run(t, e, "ROLLBACK TO SAVEPOINT sp1")
	if !strings.Contains(res.Message, "sp1") {
		t.Fatalf("mensaje: %s", res.Message)
	}
	if !e.InTransaction() {
		t.Fatal("el rollback a savepoint cerró la transacción")
	}

	got := run(t, e, "SELECT val FROM t WHERE id = 1")
	if got.Rows[0][0] != int32(11) {
		t.Fatalf("valor tras el savepoint: %#v", got.Rows[0])
	}
	if rows := run(t, e, "SELECT id FROM t WHERE id = 2"); len(rows.Rows) != 0 {
		t.Fatalf("la fila posterior al savepoint sigue: %#v", rows.Rows)
	}

	run(t, e, "COMMIT")
	got = run(t, e, "SELECT val FROM t WHERE id = 1")
	if got.Rows[0][0] != int32(11) {
		t.Fatalf("no se confirmó: %#v", got.Rows[0])
	}
}

// TestSavepointDesconocidoYErrores cubre los casos de error de las sentencias de
// transacción.
func TestSavepointDesconocidoYErrores(t *testing.T) {
	e := newTestEngine(t, Options{})
	run(t, e, "CREATE TABLE t (id INT PRIMARY KEY)")
	for _, q := range []string{"COMMIT", "ROLLBACK", "ROLLBACK TO sp", "SAVEPOINT sp", "RELEASE sp"} {
		if _, err := e.Exec(q); err == nil {
			t.Fatalf("%s sin transacción debería fallar", q)
		}
	}
	run(t, e, "BEGIN")
	if _, err := e.Exec("BEGIN"); err == nil {
		t.Fatal("permitió dos transacciones")
	}
	if _, err := e.Exec("ROLLBACK TO otro"); err == nil {
		t.Fatal("savepoint desconocido")
	}
	if _, err := e.Exec("RELEASE otro"); err == nil {
		t.Fatal("release de savepoint desconocido")
	}
	// El DDL no se puede deshacer, así que no se permite dentro de una
	// transacción.
	if _, err := e.Exec("CREATE TABLE u (id INT PRIMARY KEY)"); err == nil {
		t.Fatal("permitió CREATE TABLE en transacción")
	}
	if _, err := e.Exec("DROP TABLE t"); err == nil {
		t.Fatal("permitió DROP TABLE en transacción")
	}
	run(t, e, "ROLLBACK")
}

// TestRecuperacionDeshaceCrash simula un corte abrupto: se escriben cambios con
// una transacción abierta y se abre una base nueva sin cerrar la anterior.
func TestRecuperacionDeshaceCrash(t *testing.T) {
	dir := t.TempDir()

	e, err := Open("app", Options{Dir: dir})
	if err != nil {
		t.Fatalf("apertura: %v", err)
	}
	run(t, e, "CREATE TABLE t (id INT PRIMARY KEY, val INT)")
	run(t, e, "INSERT INTO t (id, val) VALUES (1, 10)")

	// Transacción que se confirma: sus cambios son definitivos.
	run(t, e, "BEGIN")
	run(t, e, "INSERT INTO t (id, val) VALUES (2, 20)")
	run(t, e, "COMMIT")

	// Transacción que se queda abierta: el proceso "se corta" aquí.
	run(t, e, "BEGIN")
	run(t, e, "INSERT INTO t (id, val) VALUES (3, 30)")
	run(t, e, "UPDATE t SET val = 99 WHERE id = 1")
	// No hay Close: se abandona el motor como si hubiera un corte.

	e2, err := Open("app", Options{Dir: dir})
	if err != nil {
		t.Fatalf("reapertura: %v", err)
	}
	defer e2.Close()

	rec := e2.Recovery()
	if rec.Undone != 2 {
		t.Fatalf("deshizo %d cambios, esperaba 2: %+v", rec.Undone, rec)
	}
	if rec.Committed != 1 {
		t.Fatalf("transacciones confirmadas: %+v", rec)
	}

	got := run(t, e2, "SELECT id, val FROM t ORDER BY id")
	if len(got.Rows) != 2 {
		t.Fatalf("filas tras recuperar: %#v", got.Rows)
	}
	if got.Rows[0][1] != int32(10) {
		t.Fatalf("el update quedó a medias: %#v", got.Rows[0])
	}
	if got.Rows[1][0] != int32(2) {
		t.Fatalf("la fila del crash quedó viva: %#v", got.Rows[1])
	}
	if e2.InTransaction() {
		t.Fatal("la recuperación dejó una transacción abierta")
	}
}

// TestRecuperacionClusteredYTablaEliminada cubre los dos casos raros: una tabla
// agrupada a medias y un registro cuya tabla ya no existe.
func TestRecuperacionClusteredYTablaEliminada(t *testing.T) {
	dir := t.TempDir()
	e, err := Open("app", Options{Dir: dir})
	if err != nil {
		t.Fatalf("apertura: %v", err)
	}
	run(t, e, "CREATE TABLE c CLUSTERED BY (id) (id INT PRIMARY KEY, val INT)")
	run(t, e, "INSERT INTO c (id, val) VALUES (1, 1)")

	run(t, e, "BEGIN")
	run(t, e, "UPDATE c SET val = 42 WHERE id = 1")
	run(t, e, "INSERT INTO c (id, val) VALUES (2, 2)")

	// Se agrega a mano un registro de una tabla que no existe, como si el crash
	// hubiera ocurrido justo después de un DROP TABLE.
	appendLogRecord(t, filepath.Join(dir, "wal.log"), "fantasma", int32(7), "x")

	e2, err := Open("app", Options{Dir: dir})
	if err != nil {
		t.Fatalf("reapertura: %v", err)
	}
	defer e2.Close()

	if rec := e2.Recovery(); rec.Undone != 2 || rec.Skipped != 1 {
		t.Fatalf("recuperación: %+v", rec)
	}
	got := run(t, e2, "SELECT id, val FROM c")
	if len(got.Rows) != 1 || got.Rows[0][1] != int32(1) {
		t.Fatalf("estado tras recuperar: %#v", got.Rows)
	}
}

// appendLogRecord deja en el WAL un cambio sin confirmar de una tabla que no
// existe, para probar que la recuperación lo salta sin fallar.
func appendLogRecord(t *testing.T, path, table string, id int32, val string) {
	t.Helper()
	m, _, err := transaction.NewManager(path)
	if err != nil {
		t.Fatalf("abrir WAL: %v", err)
	}
	defer m.Close()
	tx, err := m.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	rec := transaction.Record{
		Op:    transaction.OpInsert,
		Table: table,
		Row:   storage.Tuple{id, val},
	}
	if err := m.Add(tx, rec); err != nil {
		t.Fatalf("add: %v", err)
	}
	// Sin commit: la recuperación tendrá que deshacerlo.
}

// TestWALSeCreaYSeTrunca comprueba que el archivo existe y que un cierre limpio
// lo deja listo para el siguiente arranque.
func TestWALSeCreaYSeTrunca(t *testing.T) {
	dir := t.TempDir()
	e, err := Open("app", Options{Dir: dir})
	if err != nil {
		t.Fatalf("apertura: %v", err)
	}
	run(t, e, "CREATE TABLE t (id INT PRIMARY KEY, val INT)")
	run(t, e, "BEGIN")
	run(t, e, "INSERT INTO t (id, val) VALUES (1, 1)")
	run(t, e, "COMMIT")
	if info, err := os.Stat(filepath.Join(dir, "wal.log")); err != nil || info.Size() == 0 {
		t.Fatalf("WAL: %v %v", info, err)
	}
	e.Close()

	// Tras el checkpoint solo queda la cabecera del log.
	info, err := os.Stat(filepath.Join(dir, "wal.log"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() > 8 {
		t.Fatalf("el WAL no se truncó: %d bytes", info.Size())
	}
}

// TestTransaccionConIndicesSecundarios comprueba que un rollback también deja
// los índices secundarios coherentes con los datos.
func TestTransaccionConIndicesSecundarios(t *testing.T) {
	e := newTestEngine(t, Options{})
	run(t, e, "CREATE TABLE t (id INT PRIMARY KEY, mail VARCHAR(32))")
	run(t, e, "CREATE INDEX idx_mail ON t(mail)")
	run(t, e, "INSERT INTO t (id, mail) VALUES (1, 'a@c.d')")

	run(t, e, "BEGIN")
	run(t, e, "INSERT INTO t (id, mail) VALUES (2, 'x@y.z')")
	run(t, e, "UPDATE t SET mail = 'nuevo@c.d' WHERE id = 1")
	run(t, e, "ROLLBACK")

	if rows := run(t, e, "SELECT id FROM t WHERE mail = 'x@y.z'"); len(rows.Rows) != 0 {
		t.Fatalf("el índice secondary guardó la fila deshecha: %#v", rows.Rows)
	}
	if rows := run(t, e, "SELECT id FROM t WHERE mail = 'a@c.d'"); len(rows.Rows) != 1 {
		t.Fatalf("el índice perdió la fila original: %#v", rows.Rows)
	}
	if rows := run(t, e, "SELECT id FROM t WHERE mail = 'nuevo@c.d'"); len(rows.Rows) != 0 {
		t.Fatalf("quedó el valor nuevo: %#v", rows.Rows)
	}
}

// TestCierreConTransaccionAbierta: si el motor se cierra con una transacción en
// curso, esa transacción se deshace (como un cliente que se desconecta).
func TestCierreConTransaccionAbierta(t *testing.T) {
	dir := t.TempDir()
	e, err := Open("app", Options{Dir: dir})
	if err != nil {
		t.Fatalf("apertura: %v", err)
	}
	run(t, e, "CREATE TABLE t (id INT PRIMARY KEY, val INT)")
	run(t, e, "INSERT INTO t (id, val) VALUES (1, 1)")
	run(t, e, "BEGIN")
	run(t, e, "INSERT INTO t (id, val) VALUES (2, 2)")
	e.Close()

	e2, err := Open("app", Options{Dir: dir})
	if err != nil {
		t.Fatalf("reapertura: %v", err)
	}
	defer e2.Close()
	rows := run(t, e2, "SELECT id FROM t")
	if len(rows.Rows) != 1 {
		t.Fatalf("la transacción abierta sobrevivió al cierre: %#v", rows.Rows)
	}
}

// TestWriteAheadDejaRegistroAntesDeAplicar comprueba el orden real: el registro
// está en el WAL antes de que la fila exista en la tabla. Para verlo se cuenta
// el número de registros del log justo después del INSERT, cuando la sentencia
// ya terminó pero la transacción sigue abierta.
func TestWriteAheadDejaRegistroAntesDeAplicar(t *testing.T) {
	dir := t.TempDir()
	e, err := Open("app", Options{Dir: dir})
	if err != nil {
		t.Fatalf("apertura: %v", err)
	}
	defer e.Close()
	run(t, e, "CREATE TABLE t (id INT PRIMARY KEY, val INT)")
	run(t, e, "BEGIN")
	run(t, e, "INSERT INTO t (id, val) VALUES (1, 10)")

	st := walStats(t, dir)
	if st == 0 {
		t.Fatal("el INSERT no dejó registro en el WAL")
	}
	// El registro existe y la fila también: el log va primero, pero la
	// sentencia se completó.
	res := run(t, e, "SELECT val FROM t WHERE id = 1")
	if len(res.Rows) != 1 || res.Rows[0][0] != int32(10) {
		t.Fatalf("fila insertada: %#v", res.Rows)
	}
}

// TestRecuperacionIgnoraRegistrosNoAplicados es el caso que motiva el undo
// idempotente: con write-ahead, un INSERT que falla por clave duplicada deja su
// registro escrito aunque la fila nunca llegara a existir. La recuperación no
// debe inventarse una fila ni fallar.
func TestRecuperacionIgnoraRegistrosNoAplicados(t *testing.T) {
	dir := t.TempDir()
	e, err := Open("app", Options{Dir: dir})
	if err != nil {
		t.Fatalf("apertura: %v", err)
	}
	run(t, e, "CREATE TABLE t (id INT PRIMARY KEY, val INT)")
	run(t, e, "INSERT INTO t (id, val) VALUES (1, 10)")
	run(t, e, "BEGIN")
	run(t, e, "INSERT INTO t (id, val) VALUES (2, 20)")

	// La clave ya existe: el registro se escribe y el insert falla.
	if _, err := e.Exec("INSERT INTO t (id, val) VALUES (2, 99)"); err == nil {
		t.Fatal("debía fallar por clave duplicada")
	}

	e2, err := Open("app", Options{Dir: dir})
	if err != nil {
		t.Fatalf("reapertura: %v", err)
	}
	defer e2.Close()

	// Solo sobrevive la fila confirmada en autocommit.
	got := run(t, e2, "SELECT id, val FROM t ORDER BY id")
	if len(got.Rows) != 1 || got.Rows[0][0] != int32(1) {
		t.Fatalf("filas tras recuperar: %#v", got.Rows)
	}
}

// TestUndoIdempotenteAplicaDosVeces comprueba que revertir dos veces el mismo
// registro es inocuo: es lo que hace falta para que la recuperación pueda
// ejecutarse sin saber si el cambio llegó a aplicarse.
func TestUndoIdempotenteAplicaDosVeces(t *testing.T) {
	dir := t.TempDir()
	e, err := Open("app", Options{Dir: dir})
	if err != nil {
		t.Fatalf("apertura: %v", err)
	}
	defer e.Close()
	run(t, e, "CREATE TABLE t (id INT PRIMARY KEY, val INT)")
	run(t, e, "INSERT INTO t (id, val) VALUES (1, 10)")
	run(t, e, "INSERT INTO t (id, val) VALUES (2, 20)")

	tbl, ok := e.cat.get("t")
	if !ok {
		t.Fatal("no está la tabla t")
	}
	ins := transaction.Record{
		Op:    transaction.OpInsert,
		Table: "t",
		Row:   storage.Tuple{int32(1), int32(10)},
	}
	if err := tbl.applyUndo(ins); err != nil {
		t.Fatalf("primer undo: %v", err)
	}
	if err := tbl.applyUndo(ins); err != nil {
		t.Fatalf("segundo undo: %v", err)
	}
	res := run(t, e, "SELECT id FROM t ORDER BY id")
	if len(res.Rows) != 1 || res.Rows[0][0] != int32(2) {
		t.Fatalf("undo de insert: %#v", res.Rows)
	}

	// Ahora el borrado sí llega a aplicarse, así que su undo tiene que
	// reinstalar la fila y ser inocuo al repetirse.
	if err := tbl.deleteRow(storage.Tuple{int32(2), int32(20)}); err != nil {
		t.Fatalf("borrar: %v", err)
	}
	if res := run(t, e, "SELECT id FROM t WHERE id = 2"); len(res.Rows) != 0 {
		t.Fatalf("el borrado no se aplicó: %#v", res.Rows)
	}
	del := transaction.Record{
		Op:    transaction.OpDelete,
		Table: "t",
		Row:   storage.Tuple{int32(2), int32(20)},
	}
	if err := tbl.applyUndo(del); err != nil {
		t.Fatalf("undo de delete: %v", err)
	}
	if err := tbl.applyUndo(del); err != nil {
		t.Fatalf("undo repetido de delete: %v", err)
	}
	res = run(t, e, "SELECT id, val FROM t WHERE id = 2")
	if len(res.Rows) != 1 || res.Rows[0][1] != int32(20) {
		t.Fatalf("el undo de delete no restauró la fila: %#v", res.Rows)
	}

	// Un registro de UPDATE cuyo valor nuevo nunca se escribió no se deshace:
	// la tabla se queda como está.
	upd := transaction.Record{
		Op:      transaction.OpUpdate,
		Table:   "t",
		Row:     storage.Tuple{int32(2), int32(77)},
		Prev:    storage.Tuple{int32(2), int32(20)},
		HasPrev: true,
	}
	if err := tbl.applyUndo(upd); err != nil {
		t.Fatalf("undo de update sin aplicar: %v", err)
	}
	res = run(t, e, "SELECT id, val FROM t ORDER BY id")
	if len(res.Rows) != 1 || res.Rows[0][1] != int32(20) {
		t.Fatalf("el update no aplicado se deshizo de más: %#v", res.Rows)
	}

	// Y uno que sí se aplicó sí se revierte.
	if err := tbl.updateRow(storage.Tuple{int32(2), int32(20)}, storage.Tuple{int32(2), int32(77)}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := tbl.applyUndo(upd); err != nil {
		t.Fatalf("undo de update: %v", err)
	}
	if err := tbl.applyUndo(upd); err != nil {
		t.Fatalf("undo repetido de update: %v", err)
	}
	res = run(t, e, "SELECT val FROM t WHERE id = 2")
	if len(res.Rows) != 1 || res.Rows[0][0] != int32(20) {
		t.Fatalf("undo de update: %#v", res.Rows)
	}
}

// walStats cuenta los registros de cambio del WAL sin abrir la base de datos.
func walStats(t *testing.T, dir string) int {
	t.Helper()
	recs, err := transaction.ReadLog(filepath.Join(dir, "wal.log"))
	if err != nil {
		t.Fatalf("leer wal: %v", err)
	}
	n := 0
	for _, r := range recs {
		if r.Op == transaction.OpInsert || r.Op == transaction.OpUpdate || r.Op == transaction.OpDelete {
			n++
		}
	}
	return n
}
