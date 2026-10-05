package sql

import (
	"errors"
	"fmt"
	"strings"

	"github.com/dbms-go/v2/dbms/lib/dsl/ast"
	"github.com/dbms-go/v2/dbms/lib/lockmanager"
	"github.com/dbms-go/v2/dbms/lib/storage"
	"github.com/dbms-go/v2/dbms/lib/transaction"
)

// Control de transacciones del motor.
//
// Cada cambio de datos se escribe antes en el WAL (write-ahead) y después se
// aplica al almacenamiento. Si la transacción confirma, el motor fuerza el
// registro de commit a disco; si se deshace, recorre los cambios en orden
// inverso y los revierte uno a uno. Si el proceso se corta a mitad, el siguiente
// arranque deshace con Recover todo lo que no tenga commit.
//
// El motor es de una sola sesión, así que hay como mucho una transacción activa:
// los bloqueos lógicos (2PL estricto) se piden al lockmanager por clave
// tocada y se liberan al confirmar o al deshacer.

// execBegin abre una transacción.
func (e *Engine) execBegin(_ *ast.BeginTransaction) (*Result, error) {
	if e.txs == nil {
		return nil, fmt.Errorf("sql: el motor no tiene WAL abierto")
	}
	tx, err := e.txs.Begin()
	if err != nil {
		if err == transaction.ErrTransactionExists {
			return nil, fmt.Errorf("sql: ya hay una transacción activa (id %d)", e.active.ID)
		}
		return nil, err
	}
	lockTx := e.lockTxs.Begin()
	tx.Locks = transaction.NewLocks(lockTx)
	e.active = tx
	e.activeLk = lockTx

	return &Result{
		Message: fmt.Sprintf("transacción %d iniciada", tx.ID),
		Plan: []Step{{
			Kind:   StepBegin,
			Detail: fmt.Sprintf("write-ahead log: %s", e.walPath()),
		}},
	}, nil
}

// execCommit confirma la transacción activa.
func (e *Engine) execCommit(_ *ast.Commit) (*Result, error) {
	tx := e.active
	if tx == nil {
		return nil, transaction.ErrNoTransaction
	}
	changes := len(tx.Changes())
	if err := e.txs.Commit(tx); err != nil {
		return nil, err
	}
	e.finishTx(tx, true)

	st := e.txs.Stats()
	return &Result{
		Message: fmt.Sprintf("transacción %d confirmada: %d cambio(s) en el WAL (%d registro(s), %d B)",
			tx.ID, changes, st.Records, st.Bytes),
		Affected: changes,
		Plan: []Step{{
			Kind:   StepCommit,
			Detail: fmt.Sprintf("commit forzado a disco: %d cambio(s) definitivos", changes),
			Rows:   changes,
		}},
	}, nil
}

// execRollback deshace la transacción activa o, si la sentencia trae un
// savepoint, solo los cambios posteriores a él.
func (e *Engine) execRollback(n *ast.Rollback) (*Result, error) {
	tx := e.active
	if tx == nil {
		return nil, transaction.ErrNoTransaction
	}

	if n.Savepoint != nil && *n.Savepoint != "" {
		undone, err := e.txs.RollbackTo(tx, *n.Savepoint, e.undoRecord)
		if err != nil {
			return nil, err
		}
		return &Result{
			Message: fmt.Sprintf("transacción %d: %d cambio(s) deshechos hasta el savepoint %s",
				tx.ID, undone, *n.Savepoint),
			Affected: undone,
			Plan: []Step{{
				Kind:   StepRollback,
				Detail: fmt.Sprintf("rollback al savepoint %s: %d cambio(s) revertidos", *n.Savepoint, undone),
				Rows:   undone,
			}},
		}, nil
	}

	changes := len(tx.Changes())
	if err := e.txs.Rollback(tx, e.undoRecord); err != nil {
		return nil, err
	}
	e.finishTx(tx, false)

	return &Result{
		Message:  fmt.Sprintf("transacción %d deshecha: %d cambio(s) revertidos", tx.ID, changes),
		Affected: changes,
		Plan: []Step{{
			Kind:   StepRollback,
			Detail: fmt.Sprintf("rollback completo: %d cambio(s) revertidos", changes),
			Rows:   changes,
		}},
	}, nil
}

// execSavepoint marca un punto de retorno dentro de la transacción activa.
func (e *Engine) execSavepoint(n *ast.Savepoint) (*Result, error) {
	if e.active == nil {
		return nil, transaction.ErrNoTransaction
	}
	if err := e.txs.Savepoint(e.active, n.Name); err != nil {
		return nil, err
	}
	return &Result{
		Message: fmt.Sprintf("savepoint %s creado en la transacción %d", n.Name, e.active.ID),
	}, nil
}

// execRelease descarta un savepoint sin deshacer nada.
func (e *Engine) execRelease(n *ast.Release) (*Result, error) {
	if e.active == nil {
		return nil, transaction.ErrNoTransaction
	}
	if err := e.txs.Release(e.active, n.Name); err != nil {
		return nil, err
	}
	return &Result{
		Message: fmt.Sprintf("savepoint %s liberado", n.Name),
	}, nil
}

// finishTx libera los bloqueos de la transacción y la saca del gestor.
func (e *Engine) finishTx(tx *transaction.Tx, commit bool) {
	tx.Locks.ReleaseAll()
	if e.activeLk != nil {
		if commit {
			_ = e.activeLk.Commit()
		} else {
			_ = e.activeLk.Abort()
		}
		e.lockTxs.Remove(e.activeLk.ID)
	}
	if e.active == tx {
		e.active = nil
		e.activeLk = nil
	}
}

// logChange registra un cambio de datos en el WAL. Sin transacción activa el
// cambio es autocomitido: se aplica y ya está.
func (e *Engine) logChange(rec transaction.Record) error {
	if e.active == nil || e.txs == nil {
		return nil
	}
	return e.txs.Add(e.active, rec)
}

// undoRecord revierte un cambio del WAL. La implementa el motor porque es quien
// sabe cómo escribir y borrar filas en un heap o en un secuencial.
func (e *Engine) undoRecord(rec transaction.Record) error {
	tbl, ok := e.cat.get(rec.Table)
	if !ok {
		// La tabla ya no existe (se borró después del crash): sus filas no
		// están en ninguna parte, así que no hay nada que deshacer.
		e.recovery.Skipped++
		return nil
	}
	if err := tbl.applyUndo(rec); err != nil {
		return fmt.Errorf("sql: no se pudo deshacer un %s en %s: %w", rec.Op, rec.Table, err)
	}
	return nil
}

// recover deshace lo que quedó a medias en el arranque anterior y hace
// checkpoint del WAL.
func (e *Engine) recover() error {
	reverted, err := transaction.Recover(e.walPath(), func(rec transaction.Record) error {
		if _, ok := e.cat.get(rec.Table); !ok {
			e.recovery.Skipped++
			return nil
		}
		return e.undoRecord(rec)
	})
	if err != nil {
		return fmt.Errorf("sql: recuperación del WAL fallida: %w", err)
	}
	// Recover cuenta como revertidos todos los registros que procesó, incluso los
	// saltados, así que se restan para informar solo de los cambios reales.
	e.recovery.Undone = reverted - e.recovery.Skipped
	if e.txs != nil {
		if err := e.txs.Checkpoint(); err != nil {
			return err
		}
	}
	return nil
}

// InTransaction indica si hay una transacción abierta.
func (e *Engine) InTransaction() bool { return e.active != nil }

// TransactionID devuelve el identificador de la transacción activa (0 si no hay).
func (e *Engine) TransactionID() uint64 {
	if e.active == nil {
		return 0
	}
	return e.active.ID
}

// Recovery devuelve lo que hizo la recuperación del WAL al abrir la base.
func (e *Engine) Recovery() RecoveryInfo { return e.recovery }

// WALPath devuelve la ruta del write-ahead log.
func (e *Engine) WALPath() string { return e.walPath() }

// Locks devuelve los bloqueos que tiene la transacción activa.
func (e *Engine) Locks() []string {
	if e.active == nil {
		return nil
	}
	return e.active.Locks.Held()
}

// lockKey es el recurso lógico que se bloquea: tabla y clave primaria del valor.
// Así dos transacciones que tocan filas distintas no se estorban.
func lockKey(table string, key any) string {
	return fmt.Sprintf("%s:%v", strings.ToLower(table), key)
}

// lockRowExclusive toma un bloqueo exclusivo sobre la clave primaria de una fila,
// que es la identidad lógica de la fila dentro de la tabla. Dos transacciones que
// tocan filas distintas no se estorban; la que toca la misma, espera.
func (e *Engine) lockRowExclusive(tbl *Table, row storage.Tuple) error {
	if e.active == nil || len(tbl.Schema.KeyCols) == 0 {
		return nil
	}
	pk := tbl.Schema.KeyCols[0]
	if pk >= len(row) {
		return nil
	}
	return e.active.Locks.Lock(lockKey(tbl.Name(), row[pk]), lockmanager.Exclusive)
}

// lockRowsShared toma bloqueos compartidos sobre las claves de las filas leídas,
// para que nadie las modifique mientras la transacción las está usando.
func (e *Engine) lockRowsShared(tbl *Table, rows []storage.Tuple) error {
	if e.active == nil || len(tbl.Schema.KeyCols) == 0 {
		return nil
	}
	pk := tbl.Schema.KeyCols[0]
	for _, row := range rows {
		if pk >= len(row) {
			continue
		}
		if err := e.active.Locks.Lock(lockKey(tbl.Name(), row[pk]), lockmanager.Shared); err != nil {
			return err
		}
	}
	return nil
}

// applyUndo revierte un cambio del WAL sobre una tabla.
//
// Se apoya en las operaciones normales de fila (insertRow, deleteRow), que ya
// mantienen storage e índices coherentes, en lugar de escribir a los archivos
// por debajo. En tablas no agrupadas la fila vuelve a un RID nuevo: es una
// consecuencia física del heap y no afecta a la tabla.
//
// El undo es idempotente a propósito. Como el registro se escribe antes de
// aplicar el cambio (write-ahead), un corte puede dejar un registro cuyo cambio
// nunca llegó a tocar los datos, y la recuperación puede volver a pasar por él.
// Por eso primero se mira si el valor nuevo está en la tabla: si no está, el
// cambio no se aplicó y no hay nada que revertir.
func (t *Table) applyUndo(rec transaction.Record) error {
	present, err := t.rowExists(rec.Row)
	if err != nil {
		return err
	}

	switch rec.Op {
	case transaction.OpInsert:
		if !present {
			return nil
		}
		return t.deleteRow(rec.Row)
	case transaction.OpDelete:
		if present {
			return nil
		}
		_, _, err := t.insertRow(rec.Row)
		return err
	case transaction.OpUpdate:
		if !present || !rec.HasPrev {
			// El update no llegó a aplicarse: la fila sigue con el valor previo.
			return nil
		}
		// Se borra el valor nuevo y se reinserta el anterior.
		if err := t.deleteRow(rec.Row); err != nil {
			return fmt.Errorf("no se pudo borrar el valor nuevo: %w", err)
		}
		if _, _, err := t.insertRow(rec.Prev); err != nil {
			return fmt.Errorf("no se pudo restaurar el valor anterior: %w", err)
		}
		return nil
	}
	return nil
}

// rowExists indica si una fila está en la tabla. Sin clave primaria no hay forma
// barata de saberlo, así que se asume que sí: la recuperación solo es
// concluyente en tablas con PK, que son las que el motor usa para identificar
// filas.
func (t *Table) rowExists(row storage.Tuple) (bool, error) {
	if row == nil || len(t.Schema.KeyCols) == 0 {
		return true, nil
	}
	pk := t.Schema.KeyCols[0]
	if pk >= len(row) {
		return false, nil
	}
	idx, ok := t.IndexOnColumn(pk)
	if !ok {
		return true, nil
	}
	rows, err := idx.SearchExact(row[pk])
	if err != nil {
		if errors.Is(err, storage.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	for _, r := range rows {
		if tupleEqual(r, row) {
			return true, nil
		}
	}
	return false, nil
}
