package transaction

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var (
	// ErrWALCorrupt indica un WAL ilegible o con un registro dañado.
	ErrWALCorrupt = errors.New("transaction: WAL corrupto")
	// ErrNoTransaction indica que no hay ninguna transacción activa.
	ErrNoTransaction = errors.New("transaction: no hay transacción activa")
	// ErrTransactionExists indica que ya hay una transacción activa.
	ErrTransactionExists = errors.New("transaction: ya hay una transacción activa")
	// ErrSavepoint indica un savepoint desconocido.
	ErrSavepoint = errors.New("transaction: savepoint desconocido")
)

// Status es el estado de una transacción.
type Status uint8

const (
	// StatusActive es una transacción abierta (en fase de crecimiento).
	StatusActive Status = iota + 1
	// StatusCommitted es una transacción confirmada: sus cambios son definitivos.
	StatusCommitted
	// StatusAborted es una transacción deshecha: sus cambios se revirtieron.
	StatusAborted
)

func (s Status) String() string {
	switch s {
	case StatusActive:
		return "active"
	case StatusCommitted:
		return "committed"
	case StatusAborted:
		return "aborted"
	}
	return "unknown"
}

// UndoFunc revierte un cambio registrado en el WAL. La implementa el motor, que
// es quien sabe cómo borrar una fila de un heap file o de un secuencial.
type UndoFunc func(Record) error

// Tx es una transacción abierta y los cambios que ha registrado.
type Tx struct {
	// ID es el identificador de la transacción (lo asigna el gestor).
	ID uint64
	// Status es su estado actual.
	Status Status

	// entries son los cambios registrados, en orden de ejecución. Se guardan en
	// memoria además de en el WAL porque el rollback necesita recorrerlos hacia
	// atrás sin releer el archivo entero.
	entries []Record
	// savepoints asocia un nombre con la posición en entries desde la que se
	// puede volver atrás.
	savepoints map[string]int
	// Locks son los bloqueos lógicos que la transacción tiene abiertos. Con 2PL
	// estricto se mantienen hasta el commit o el rollback.
	Locks *Locks
}

// Changes devuelve los registros de cambio de la transacción.
func (t *Tx) Changes() []Record { return t.entries }

// Savepoints lista los nombres de los savepoint activos.
func (t *Tx) Savepoints() []string {
	out := make([]string, 0, len(t.savepoints))
	for name := range t.savepoints {
		out = append(out, name)
	}
	return out
}

// Manager es el gestor de transacciones del motor: escribe el WAL y decide qué
// se confirma y qué se deshace.
type Manager struct {
	mu     sync.Mutex
	path   string
	file   *os.File
	nextID uint64
	lsn    uint64
	// active son las transacciones abiertas. Hay una por sesión: el motor
	// admite varias sesiones concurrentes y los bloqueos (2PL) las aíslan.
	active map[uint64]*Tx
	// stats lleva la cuenta de lo que escribe este proceso, para el panel.
	stats Stats
	// found es lo que había en el WAL cuando se abrió.
	found Stats
}

// Stats resume la actividad de un WAL.
type Stats struct {
	// Committed son las transacciones confirmadas.
	Committed int
	// Aborted son las transacciones deshechas.
	Aborted int
	// Undone son los cambios revertidos por rollback o por recuperación.
	Undone int
	// Records son los registros escritos en el WAL.
	Records int
	// Bytes son los bytes escritos en el WAL.
	Bytes int
}

// NewManager crea (o reabre) el gestor sobre el WAL en path y devuelve las
// estadísticas de lo que encontró en el archivo. Las estadísticas del gestor
// empiezan a cero: cuentan solo lo que escribe este proceso.
//
// El log NO se trunca aquí: si el proceso se cortó, sus registros son lo único
// que permite deshacer lo que quedó a medias. Quien decide es el motor, que
// llama a Recover y, si todo quedó en orden, a Checkpoint.
func NewManager(path string) (*Manager, Stats, error) {
	var st Stats
	m := &Manager{path: path}

	records, err := ReadLog(path)
	if err != nil {
		return nil, st, err
	}
	for _, r := range records {
		switch r.Op {
		case OpBegin:
			if r.TxID > m.nextID {
				m.nextID = r.TxID
			}
		case OpCommit:
			st.Committed++
		case OpRollback:
			st.Aborted++
		}
		st.Records++
		st.Bytes += walRecordOverhead
	}

	m.found = st
	if err := m.open(); err != nil {
		return nil, st, err
	}
	return m, st, nil
}

// Checkpoint trunca el WAL: a partir de aquí no queda nada que recuperar, así
// que el log puede empezar de cero. Solo debe llamarse cuando no hay ninguna
// transacción abierta.
func (m *Manager) Checkpoint() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.file == nil {
		return nil
	}
	if len(m.active) > 0 {
		return fmt.Errorf("transaction: no se puede hacer checkpoint con una transacción activa")
	}
	m.lsn = 0
	return m.truncate()
}

func (m *Manager) open() error {
	if dir := filepath.Dir(m.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("transaction: no se pudo crear el directorio del WAL: %w", err)
		}
	}
	f, err := os.OpenFile(m.path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("transaction: no se pudo abrir el WAL: %w", err)
	}
	m.file = f

	// Un log nuevo (o que quedó a medias de un truncate) arranca con la cabecera.
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("transaction: no se pudo inspeccionar el WAL: %w", err)
	}
	if info.Size() < logFileHeaderSize {
		if err := f.Truncate(0); err != nil {
			return fmt.Errorf("transaction: no se pudo preparar el WAL: %w", err)
		}
		var head [logFileHeaderSize]byte
		binaryPut(head[0:4], walMagic)
		binaryPut(head[4:8], FormatVersion)
		if _, err := f.WriteAt(head[:], 0); err != nil {
			return fmt.Errorf("transaction: no se pudo escribir la cabecera del WAL: %w", err)
		}
	}
	if _, err := f.Seek(0, 2); err != nil {
		return fmt.Errorf("transaction: no se pudo posicionar al final del WAL: %w", err)
	}
	return nil
}

// truncate deja el WAL vacío conservando su cabecera.
func (m *Manager) truncate() error {
	var head [logFileHeaderSize]byte
	binaryPut(head[0:4], walMagic)
	binaryPut(head[4:8], FormatVersion)
	if err := m.file.Truncate(0); err != nil {
		return fmt.Errorf("transaction: no se pudo truncar el WAL: %w", err)
	}
	if _, err := m.file.WriteAt(head[:], 0); err != nil {
		return fmt.Errorf("transaction: no se pudo escribir la cabecera del WAL: %w", err)
	}
	_, err := m.file.Seek(logFileHeaderSize, 0)
	return err
}

// Begin abre una transacción y escribe su registro de inicio.
func (m *Manager) Begin() (*Tx, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	tx := &Tx{
		ID:         m.nextID,
		Status:     StatusActive,
		savepoints: make(map[string]int),
		Locks:      NewLocks(nil),
	}
	rec := Record{LSN: m.nextLSN(), TxID: tx.ID, Op: OpBegin}
	if err := m.append(rec); err != nil {
		return nil, err
	}
	if m.active == nil {
		m.active = make(map[uint64]*Tx)
	}
	m.active[tx.ID] = tx
	return tx, nil
}

// Active devuelve la transacción abierta más reciente, si la hay.
func (m *Manager) Active() *Tx {
	m.mu.Lock()
	defer m.mu.Unlock()
	var last *Tx
	for _, tx := range m.active {
		if last == nil || tx.ID > last.ID {
			last = tx
		}
	}
	return last
}

// ActiveCount devuelve cuántas transacciones están abiertas.
func (m *Manager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.active)
}

// Add registra un cambio de la transacción activa en el WAL y en su lista de
// deshacer. Si no hay transacción activa no hace nada (autocommit).
func (m *Manager) Add(tx *Tx, rec Record) error {
	if tx == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rec.LSN = m.nextLSN()
	rec.TxID = tx.ID
	if err := m.append(rec); err != nil {
		return err
	}
	tx.entries = append(tx.entries, rec)
	return nil
}

// Savepoint marca un punto al que se puede volver con ROLLBACK TO.
func (m *Manager) Savepoint(tx *Tx, name string) error {
	if tx == nil {
		return ErrNoTransaction
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	tx.savepoints[name] = len(tx.entries)
	return nil
}

// RollbackTo deshace los cambios hechos desde el savepoint name, en orden
// inverso, y deja la transacción abierta. Los savepoint posteriores se
// descartan.
func (m *Manager) RollbackTo(tx *Tx, name string, undo UndoFunc) (int, error) {
	if tx == nil {
		return 0, ErrNoTransaction
	}
	m.mu.Lock()
	pos, ok := tx.savepoints[name]
	m.mu.Unlock()
	if !ok {
		return 0, fmt.Errorf("%w: %s", ErrSavepoint, name)
	}

	entries := tx.entries[pos:]
	for i := len(entries) - 1; i >= 0; i-- {
		if err := undo(entries[i]); err != nil {
			return 0, fmt.Errorf("transaction: no se pudo deshacer el cambio %d: %w", i, err)
		}
	}
	m.mu.Lock()
	tx.entries = tx.entries[:pos]
	for n := range tx.savepoints {
		if tx.savepoints[n] >= pos {
			delete(tx.savepoints, n)
		}
	}
	m.stats.Undone += len(entries)
	m.mu.Unlock()
	return len(entries), nil
}

// Release descarta un savepoint sin deshacer nada.
func (m *Manager) Release(tx *Tx, name string) error {
	if tx == nil {
		return ErrNoTransaction
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := tx.savepoints[name]; !ok {
		return fmt.Errorf("%w: %s", ErrSavepoint, name)
	}
	delete(tx.savepoints, name)
	return nil
}

// Commit confirma la transacción: escribe el registro de commit y lo fuerza a
// disco. A partir de ese momento los cambios son definitivos aunque el proceso
// se corte, porque el recovery ve el commit y no deshace nada.
func (m *Manager) Commit(tx *Tx) error {
	if tx == nil {
		return ErrNoTransaction
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if tx.Status != StatusActive {
		return fmt.Errorf("transaction: la transacción %d está %s", tx.ID, tx.Status)
	}
	rec := Record{LSN: m.nextLSN(), TxID: tx.ID, Op: OpCommit}
	if err := m.appendSync(rec); err != nil {
		return err
	}
	tx.Status = StatusCommitted
	tx.Locks.ReleaseAll()
	m.stats.Committed++
	delete(m.active, tx.ID)
	return nil
}

// Rollback deshace todos los cambios de la transacción en orden inverso y
// registra el rollback.
func (m *Manager) Rollback(tx *Tx, undo UndoFunc) error {
	if tx == nil {
		return ErrNoTransaction
	}
	m.mu.Lock()
	entries := tx.entries
	m.mu.Unlock()

	var firstErr error
	for i := len(entries) - 1; i >= 0; i-- {
		if err := undo(entries[i]); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	tx.entries = nil
	tx.savepoints = make(map[string]int)
	tx.Status = StatusAborted
	tx.Locks.ReleaseAll()
	m.stats.Undone += len(entries)
	m.stats.Aborted++
	rec := Record{LSN: m.nextLSN(), TxID: tx.ID, Op: OpRollback}
	if err := m.append(rec); err != nil && firstErr == nil {
		firstErr = err
	}
	delete(m.active, tx.ID)
	return firstErr
}

// Stats devuelve las estadísticas del gestor.
func (m *Manager) Stats() Stats {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stats
}

// Path devuelve la ruta del WAL.
func (m *Manager) Path() string { return m.path }

// Found devuelve las estadísticas de lo que había en el WAL al abrirlo.
func (m *Manager) Found() Stats {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.found
}

// Close cierra el archivo del WAL.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.file == nil {
		return nil
	}
	err := m.file.Close()
	m.file = nil
	return err
}

// append escribe un registro y lo deja en el buffer del sistema operativo.
func (m *Manager) append(rec Record) error { return m.write(rec, false) }

// appendSync escribe un registro y fuerza el WAL a disco (fsync). Se usa en
// COMMIT: es lo que garantiza que una transacción confirmada sobreviva a un
// corte de luz.
func (m *Manager) appendSync(rec Record) error { return m.write(rec, true) }

func (m *Manager) write(rec Record, sync bool) error {
	if m.file == nil {
		return fmt.Errorf("transaction: el WAL está cerrado")
	}
	buf, err := rec.Encode()
	if err != nil {
		return err
	}
	if _, err := m.file.Write(buf); err != nil {
		return fmt.Errorf("transaction: no se pudo escribir en el WAL: %w", err)
	}
	if sync {
		if err := m.file.Sync(); err != nil {
			return fmt.Errorf("transaction: no se pudo forzar el WAL: %w", err)
		}
	}
	m.stats.Records++
	m.stats.Bytes += len(buf)
	return nil
}

func (m *Manager) nextLSN() uint64 {
	m.lsn++
	return m.lsn
}

// Recover deshace las transacciones que el WAL registra sin commit ni
// rollback, que son exactamente las que estaban abiertas cuando el proceso se
// cortó. Se llama al montar el motor, antes de abrir las tablas.
//
// Devuelve cuántos registros revirtió.
func Recover(path string, undo UndoFunc) (int, error) {
	records, err := ReadLog(path)
	if err != nil {
		return 0, err
	}
	// Estado de cada transacción según lo que dejó el log.
	type txState struct {
		done bool
		ops  []Record
	}
	states := make(map[uint64]*txState)
	var order []uint64
	for _, r := range records {
		st, ok := states[r.TxID]
		if !ok {
			st = &txState{}
			states[r.TxID] = st
			order = append(order, r.TxID)
		}
		switch r.Op {
		case OpBegin:
			// nada que deshacer todavía
		case OpCommit, OpRollback:
			st.done = true
			st.ops = nil
		default:
			st.ops = append(st.ops, r)
		}
	}

	undone := 0
	// Se recorren en orden inverso de LSN para deshacer en orden inverso al de
	// ejecución, que es lo que exige el protocolo undo.
	for i := len(order) - 1; i >= 0; i-- {
		st := states[order[i]]
		if st.done {
			continue
		}
		for j := len(st.ops) - 1; j >= 0; j-- {
			if err := undo(st.ops[j]); err != nil {
				return undone, fmt.Errorf("transaction: no se pudo recuperar el cambio %s: %w", st.ops[j].Op, err)
			}
			undone++
		}
	}
	return undone, nil
}

// binaryPut escribe un uint32 en little endian.
func binaryPut(dst []byte, v uint32) {
	dst[0] = byte(v)
	dst[1] = byte(v >> 8)
	dst[2] = byte(v >> 16)
	dst[3] = byte(v >> 24)
}
