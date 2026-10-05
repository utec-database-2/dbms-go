package transaction

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dbms-go/v2/dbms/lib/lockmanager"
	"github.com/dbms-go/v2/dbms/lib/storage"
)

// store es una base en memoria que aplica y deshace cambios como lo haría el
// motor: Insert borra, Delete inserta y Update restaura el valor anterior.
type store struct {
	rows map[string]storage.Tuple
}

func newStore() *store { return &store{rows: make(map[string]storage.Tuple)} }

// apply simula el efecto del cambio en el almacén.
func (s *store) apply(r Record) {
	switch r.Op {
	case OpInsert:
		s.rows[key(r.Table, r.Row)] = r.Row
	case OpDelete:
		delete(s.rows, key(r.Table, r.Row))
	case OpUpdate:
		delete(s.rows, key(r.Table, r.Prev))
		s.rows[key(r.Table, r.Row)] = r.Row
	}
}

func (s *store) undo(r Record) error {
	switch r.Op {
	case OpInsert:
		delete(s.rows, key(r.Table, r.Row))
	case OpDelete:
		s.rows[key(r.Table, r.Row)] = r.Row
	case OpUpdate:
		s.rows[key(r.Table, r.Prev)] = r.Prev
		delete(s.rows, key(r.Table, r.Row))
	}
	return nil
}

// key identifica una fila por sus valores, usando la codificación del códec
// para no depender del formato interno.
func key(table string, row storage.Tuple) string {
	var b strings.Builder
	b.WriteString(table)
	for _, v := range row {
		buf, _ := storage.EncodeRow(storage.Tuple{v})
		b.WriteString("|")
		b.WriteString(string(buf))
	}
	return b.String()
}

func ins(txID uint64, table string, rid storage.RID, row storage.Tuple) Record {
	return Record{TxID: txID, Op: OpInsert, Table: table, RID: rid, Row: row}
}

func walPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "wal.log")
}

func TestWALRoundTrip(t *testing.T) {
	path := walPath(t)
	m, _, err := NewManager(path)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	tx, err := m.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	rec := ins(tx.ID, "users", storage.RID{File: 0, Page: 2, Slot: 5},
		storage.Tuple{int32(7), "ana", nil})
	if err := m.Add(tx, rec); err != nil {
		t.Fatalf("add: %v", err)
	}
	upd := Record{TxID: tx.ID, Op: OpUpdate, Table: "users",
		RID:  storage.RID{Page: 2, Slot: 5},
		Row:  storage.Tuple{int32(7), "bob", int64(3)},
		Prev: storage.Tuple{int32(7), "ana", nil}, HasPrev: true}
	if err := m.Add(tx, upd); err != nil {
		t.Fatalf("add update: %v", err)
	}
	if err := m.Commit(tx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	m.Close()

	records, err := ReadLog(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(records) != 4 {
		t.Fatalf("registros: %d", len(records))
	}
	if records[0].Op != OpBegin || records[3].Op != OpCommit {
		t.Fatalf("orden: %v %v", records[0].Op, records[3].Op)
	}
	got := records[1]
	if got.Table != "users" || got.RID != (storage.RID{File: 0, Page: 2, Slot: 5}) {
		t.Fatalf("cabecera: %+v", got)
	}
	if got.Row[1] != "ana" {
		t.Fatalf("fila: %#v", got.Row)
	}
	if u := records[2]; !u.HasPrev || u.Prev[1] != "ana" || u.Row[1] != "bob" {
		t.Fatalf("update: %+v", u)
	}
	if records[1].LSN >= records[3].LSN {
		t.Fatal("los LSN no son monótonos")
	}
}

// TestWALRegistroATras es el escenario de corte: el último registro se quedó a
// medias y la lectura debe devolver solo los completos.
func TestWALRegistroATras(t *testing.T) {
	path := walPath(t)
	m, _, err := NewManager(path)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	tx, _ := m.Begin()
	for i := 0; i < 3; i++ {
		if err := m.Add(tx, ins(tx.ID, "t", storage.RID{}, storage.Tuple{int32(i)})); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	m.Close()

	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("abrir: %v", err)
	}
	rec, _ := ins(99, "t", storage.RID{}, storage.Tuple{int32(9)}).Encode()
	if _, err := f.Write(rec[:len(rec)-3]); err != nil { // registro cortado
		t.Fatalf("escribir: %v", err)
	}
	f.Close()

	records, err := ReadLog(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(records) != 4 { // BEGIN + 3 INSERT
		t.Fatalf("registros leídos: %d", len(records))
	}
}

// TestWALCRCDetecta corrupcion verifica que un byte alterado hace que el
// registro se ignore en lugar de devolver basura.
func TestWALCRCDetectaCorrupcion(t *testing.T) {
	path := walPath(t)
	m, _, _ := NewManager(path)
	tx, _ := m.Begin()
	if err := m.Add(tx, ins(tx.ID, "t", storage.RID{}, storage.Tuple{int32(1), "x"})); err != nil {
		t.Fatalf("add: %v", err)
	}
	m.Close()

	buf, _ := os.ReadFile(path)
	buf[len(buf)-3] ^= 0xFF
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatalf("escribir: %v", err)
	}
	records, err := ReadLog(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("debería quedarse solo el BEGIN: %d", len(records))
	}
}

func TestWALArchivoAjeno(t *testing.T) {
	path := walPath(t)
	if err := os.WriteFile(path, []byte("no soy un WAL"), 0o644); err != nil {
		t.Fatalf("escribir: %v", err)
	}
	if _, err := ReadLog(path); err == nil {
		t.Fatal("aceptó un archivo ajeno")
	}
}

func TestRollbackDeshaceEnOrdenInverso(t *testing.T) {
	path := walPath(t)
	m, _, err := NewManager(path)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	s := newStore()
	// Estado inicial: dos filas que la transacción va a tocar.
	s.rows[key("t", storage.Tuple{int32(1), "a"})] = storage.Tuple{int32(1), "a"}
	s.rows[key("t", storage.Tuple{int32(2), "z"})] = storage.Tuple{int32(2), "z"}
	tx, _ := m.Begin()

	// INSERT de una fila nueva.
	insRec := ins(tx.ID, "t", storage.RID{}, storage.Tuple{int32(3), "c"})
	s.apply(insRec)
	if err := m.Add(tx, insRec); err != nil {
		t.Fatalf("add: %v", err)
	}
	// UPDATE de una fila existente.
	upd := Record{TxID: tx.ID, Op: OpUpdate, Table: "t",
		Row:  storage.Tuple{int32(1), "b"},
		Prev: storage.Tuple{int32(1), "a"}, HasPrev: true}
	s.apply(upd)
	if err := m.Add(tx, upd); err != nil {
		t.Fatalf("add: %v", err)
	}
	// DELETE de otra fila existente.
	del := Record{TxID: tx.ID, Op: OpDelete, Table: "t", Row: storage.Tuple{int32(2), "z"}}
	s.apply(del)
	if err := m.Add(tx, del); err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(s.rows) != 2 {
		t.Fatalf("estado tras los cambios: %#v", s.rows)
	}

	var order []string
	err = m.Rollback(tx, func(r Record) error {
		order = append(order, r.Op.String())
		return s.undo(r)
	})
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if strings.Join(order, ",") != "DELETE,UPDATE,INSERT" {
		t.Fatalf("orden de deshacer: %v", order)
	}
	// Deshacer un DELETE recupera la fila, deshacer un INSERT la quita y el
	// UPDATE vuelve al valor anterior.
	if len(s.rows) != 2 {
		t.Fatalf("estado tras deshacer: %#v", s.rows)
	}
	if _, ok := s.rows[key("t", storage.Tuple{int32(1), "a"})]; !ok {
		t.Fatal("el update no volvió a su valor anterior")
	}
	if _, ok := s.rows[key("t", storage.Tuple{int32(2), "z"})]; !ok {
		t.Fatal("el delete no se revirtió")
	}
	if tx.Status != StatusAborted {
		t.Fatalf("estado: %v", tx.Status)
	}
	if m.Active() != nil {
		t.Fatal("sigue activa")
	}
	if st := m.Stats(); st.Undone != 3 || st.Aborted != 1 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestCommitDejaCambiosYStats(t *testing.T) {
	path := walPath(t)
	m, _, _ := NewManager(path)
	s := newStore()
	tx, _ := m.Begin()
	rec := ins(tx.ID, "t", storage.RID{}, storage.Tuple{int32(1)})
	s.apply(rec)
	if err := m.Add(tx, rec); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := m.Commit(tx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if len(s.rows) != 1 {
		t.Fatal("el commit no debe deshacer nada")
	}
	if tx.Status != StatusCommitted {
		t.Fatalf("estado: %v", tx.Status)
	}
	if st := m.Stats(); st.Committed != 1 || st.Undone != 0 {
		t.Fatalf("stats: %+v", st)
	}
	if err := m.Commit(tx); err == nil {
		t.Fatal("permitió confirmar dos veces")
	}
}

// TestVariasTransaccionesActivas: cada sesión del motor tiene su propia
// transacción, así que el gestor admite varias abiertas a la vez y el commit de
// una no afecta a la otra.
func TestVariasTransaccionesActivas(t *testing.T) {
	m, _, _ := NewManager(walPath(t))
	t1, err := m.Begin()
	if err != nil {
		t.Fatalf("begin 1: %v", err)
	}
	t2, err := m.Begin()
	if err != nil {
		t.Fatalf("begin 2: %v", err)
	}
	if t1.ID == t2.ID || m.ActiveCount() != 2 {
		t.Fatalf("se esperaban dos transacciones distintas, activas=%d", m.ActiveCount())
	}
	if err := m.Commit(t1); err != nil {
		t.Fatal(err)
	}
	if m.ActiveCount() != 1 || m.Active() != t2 || t2.Status != StatusActive {
		t.Fatalf("el commit de t1 no debe tocar a t2")
	}
	if err := m.Checkpoint(); err == nil {
		t.Fatal("no se puede hacer checkpoint con t2 abierta")
	}
}

func TestSavepointDeshaceParte(t *testing.T) {
	path := walPath(t)
	m, _, _ := NewManager(path)
	s := newStore()
	tx, _ := m.Begin()

	first := ins(tx.ID, "t", storage.RID{}, storage.Tuple{int32(1)})
	s.apply(first)
	_ = m.Add(tx, first)

	if err := m.Savepoint(tx, "sp1"); err != nil {
		t.Fatalf("savepoint: %v", err)
	}
	second := ins(tx.ID, "t", storage.RID{}, storage.Tuple{int32(2)})
	s.apply(second)
	_ = m.Add(tx, second)
	third := ins(tx.ID, "t", storage.RID{}, storage.Tuple{int32(3)})
	s.apply(third)
	_ = m.Add(tx, third)

	n, err := m.RollbackTo(tx, "sp1", s.undo)
	if err != nil {
		t.Fatalf("rollback to: %v", err)
	}
	if n != 2 {
		t.Fatalf("deshizo %d cambios", n)
	}
	if len(s.rows) != 1 {
		t.Fatalf("filas: %#v", s.rows)
	}
	// La transacción sigue viva y puede confirmar lo que quedó.
	if m.Active() != tx || tx.Status != StatusActive {
		t.Fatal("el rollback a savepoint cerró la transacción")
	}
	if err := m.Commit(tx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if err := m.Savepoint(nil, "x"); err == nil {
		t.Fatal("savepoint sin transacción")
	}
	m2, _, _ := NewManager(walPath(t))
	tx2, _ := m2.Begin()
	if _, err := m2.RollbackTo(tx2, "nope", s.undo); err == nil {
		t.Fatal("savepoint desconocido aceptado")
	}
	if err := m2.Release(tx2, "nope"); err == nil {
		t.Fatal("release de savepoint desconocido aceptado")
	}
}

// TestRecoverDeshaceTransaccionesSinCommit es el test clave: simula un corte
// abrupto dejando el WAL con una transacción confirmada y otra abierta.
func TestRecoverDeshaceTransaccionesSinCommit(t *testing.T) {
	path := walPath(t)
	m, _, _ := NewManager(path)
	s := newStore()

	// Transacción 1: confirmada, sus cambios se quedan.
	tx1, _ := m.Begin()
	r1 := ins(tx1.ID, "t", storage.RID{}, storage.Tuple{int32(1), "ana"})
	s.apply(r1)
	_ = m.Add(tx1, r1)
	if err := m.Commit(tx1); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Transacción 2: se queda abierta (el proceso se corta aquí).
	tx2, _ := m.Begin()
	r2 := ins(tx2.ID, "t", storage.RID{}, storage.Tuple{int32(2), "bob"})
	s.apply(r2)
	_ = m.Add(tx2, r2)
	r3 := Record{TxID: tx2.ID, Op: OpUpdate, Table: "t",
		Row:  storage.Tuple{int32(1), "ana-mod"},
		Prev: storage.Tuple{int32(1), "ana"}, HasPrev: true}
	s.apply(r3)
	_ = m.Add(tx2, r3)
	m.Close() // sin commit ni rollback de tx2

	if len(s.rows) != 2 {
		t.Fatalf("precondición: %#v", s.rows)
	}

	undone, err := Recover(path, s.undo)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if undone != 2 {
		t.Fatalf("deshizo %d registros", undone)
	}
	if len(s.rows) != 1 {
		t.Fatalf("tras recuperar: %#v", s.rows)
	}
	for _, row := range s.rows {
		if row[1] != "ana" {
			t.Fatalf("el update no se revirtió: %#v", row)
		}
	}
}

// TestRecoverIgnoraRollback evita deshacer dos veces una transacción que ya se
// había deshecho antes del corte.
func TestRecoverIgnoraRollback(t *testing.T) {
	path := walPath(t)
	m, _, _ := NewManager(path)
	s := newStore()
	tx, _ := m.Begin()
	r := ins(tx.ID, "t", storage.RID{}, storage.Tuple{int32(1)})
	s.apply(r)
	_ = m.Add(tx, r)
	if err := m.Rollback(tx, s.undo); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	m.Close()

	calls := 0
	if _, err := Recover(path, func(Record) error { calls++; return nil }); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if calls != 0 {
		t.Fatalf("deshizo %d veces", calls)
	}
}

// TestNewManagerTrunca el log: un cierre limpio deja el WAL listo para empezar
// de cero, y las estadísticas informan de lo que había.
func TestNewManagerTrunca(t *testing.T) {
	path := walPath(t)
	m, _, _ := NewManager(path)
	tx, _ := m.Begin()
	_ = m.Add(tx, ins(tx.ID, "t", storage.RID{}, storage.Tuple{int32(1)}))
	_ = m.Commit(tx)
	m.Close()

	m2, st, err := NewManager(path)
	if err != nil {
		t.Fatalf("reabrir: %v", err)
	}
	defer m2.Close()
	if st.Committed != 1 {
		t.Fatalf("stats heredadas: %+v", st)
	}
	// Sin checkpoint el log conserva lo que había.
	if info, _ := os.Stat(path); info != nil && info.Size() <= logFileHeaderSize {
		t.Fatalf("el log se truncó sin pedirlo: %d bytes", info.Size())
	}
	if err := m2.Checkpoint(); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() != logFileHeaderSize {
		t.Fatalf("el log no quedó truncado: %d bytes", info.Size())
	}
	// Y el LSN sigue desde 1 tras el checkpoint.
	tx2, _ := m2.Begin()
	if tx2.ID <= tx.ID {
		t.Fatalf("el ID de transacción no avanzó: %d <= %d", tx2.ID, tx.ID)
	}
	recs, _ := ReadLog(path)
	if recs[0].LSN != 1 {
		t.Fatalf("LSN=%d", recs[0].LSN)
	}
}

// TestCheckpointConTransaccionActiva protege el log: si alguien hace checkpoint
// con una transacción en curso, sus registros dejarían de ser recuperables.
func TestCheckpointConTransaccionActiva(t *testing.T) {
	m, _, _ := NewManager(walPath(t))
	m.Begin()
	if err := m.Checkpoint(); err == nil {
		t.Fatal("hizo checkpoint con una transacción activa")
	}
}

func TestUndoConErrorSePropaga(t *testing.T) {
	m, _, _ := NewManager(walPath(t))
	tx, _ := m.Begin()
	_ = m.Add(tx, ins(tx.ID, "t", storage.RID{}, storage.Tuple{int32(1)}))
	want := errFallo
	if err := m.Rollback(tx, func(Record) error { return want }); err != want {
		t.Fatalf("rollback: %v", err)
	}
	// Aun así la transacción queda cerrada y registrada como abortada.
	if m.Active() != nil || tx.Status != StatusAborted {
		t.Fatalf("estado: %v %v", m.Active(), tx.Status)
	}
}

var errFallo = errPrueba("fallo al deshacer")

type errPrueba string

func (e errPrueba) Error() string { return string(e) }

func TestLocksSinLockManager(t *testing.T) {
	tx := &Tx{ID: 1, Status: StatusActive, Locks: NewLocks(nil)}
	if err := tx.Locks.Lock("users:id=1", lockmanager.Exclusive); err != nil {
		t.Fatalf("lock: %v", err)
	}
	// Repetir el mismo bloqueo no falla, y pedir un share sobre un recurso ya
	// tomado en exclusiva por la misma transacción tampoco.
	if err := tx.Locks.Lock("users:id=1", lockmanager.Exclusive); err != nil {
		t.Fatalf("lock repetido: %v", err)
	}
	if err := tx.Locks.Lock("users:id=1", lockmanager.Shared); err != nil {
		t.Fatalf("lock compartido: %v", err)
	}
	if got := tx.Locks.Held(); len(got) != 1 || got[0] != "users:id=1" {
		t.Fatalf("held: %#v", got)
	}
	tx.Locks.ReleaseAll()
	if len(tx.Locks.Held()) != 0 {
		t.Fatal("no liberó")
	}
	if err := tx.Locks.Lock("", lockmanager.Exclusive); err == nil {
		t.Fatal("aceptó un recurso vacío")
	}
}
