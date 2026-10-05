package sql

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dbms-go/v2/dbms/lib/dsl/ast"
	"github.com/dbms-go/v2/dbms/lib/lockmanager"
	"github.com/dbms-go/v2/dbms/lib/transaction"
)

// Sesiones y control de concurrencia.
//
// Cada usuario (o hilo) trabaja con su propia Session, que tiene como mucho una
// transacción abierta. Varias sesiones pueden ejecutar sentencias a la vez; el
// motor las aísla con bloqueo en dos fases estricto (2PL estricto):
//
//  1. Antes de ejecutar una sentencia, la sesión pide un bloqueo sobre cada
//     tabla que toca: compartido (S) para leer, exclusivo (X) para escribir o
//     para DDL. Si otra transacción tiene un bloqueo incompatible, la sesión
//     espera, sin bloquear al resto del motor.
//  2. Dentro de una transacción los bloqueos se conservan hasta COMMIT o
//     ROLLBACK, así que nadie ve ni pisa cambios sin confirmar. Fuera de una
//     transacción (autocommit) se liberan al terminar la sentencia.
//  3. Si esperar un bloqueo cerraría un ciclo en el grafo de espera (deadlock),
//     el lockmanager rechaza la petición: la transacción que la hizo es la
//     víctima, se deshace entera y el cliente puede reintentarla.
//
// Con los bloqueos ya concedidos, la sentencia se ejecuta bajo stmtMu, que la
// hace atómica frente a las de otras sesiones (las estructuras en memoria de
// los índices no son concurrentes).

// Session es una conexión al motor con su propia transacción.
type Session struct {
	e        *Engine
	active   *transaction.Tx
	activeLk *lockmanager.Transaction
}

// NewSession abre una sesión nueva sobre el motor. Una sesión no debe usarse
// desde dos goroutines a la vez; para concurrencia, una sesión por goroutine.
func (e *Engine) NewSession() *Session {
	s := &Session{e: e}
	e.sessionsMu.Lock()
	e.sessions[s] = struct{}{}
	e.sessionsMu.Unlock()
	return s
}

// Close deshace la transacción que la sesión tenga abierta y la desregistra.
func (s *Session) Close() {
	e := s.e
	if s.active != nil {
		e.stmtMu.Lock()
		s.swapIn()
		_, _ = e.execRollback(&ast.Rollback{})
		s.swapOut()
		e.stmtMu.Unlock()
	}
	e.sessionsMu.Lock()
	delete(e.sessions, s)
	e.sessionsMu.Unlock()
}

// InTransaction indica si la sesión tiene una transacción abierta.
func (s *Session) InTransaction() bool { return s.active != nil }

// TransactionID devuelve el identificador de la transacción (0 si no hay).
func (s *Session) TransactionID() uint64 {
	if s.active == nil {
		return 0
	}
	return s.active.ID
}

// Locks devuelve los bloqueos de fila de la transacción de la sesión.
func (s *Session) Locks() []string {
	if s.active == nil {
		return nil
	}
	return s.active.Locks.Held()
}

func (s *Session) swapIn() {
	s.e.active, s.e.activeLk = s.active, s.activeLk
}

func (s *Session) swapOut() {
	s.active, s.activeLk = s.e.active, s.e.activeLk
	s.e.active, s.e.activeLk = nil, nil
}

// tableLock es un bloqueo de tabla que pide una sentencia.
type tableLock struct {
	table string
	mode  lockmanager.LockMode
}

// tableLockKey es el recurso del lockmanager para una tabla completa. Lleva un
// prefijo distinto al de las filas ("tabla:pk") para que no colisionen.
func tableLockKey(table string) string {
	return "table#" + strings.ToLower(table)
}

// statementLocks deduce de la sentencia qué tablas toca y en qué modo. Se
// ordenan por nombre para que dos sentencias que tocan las mismas tablas las
// pidan en el mismo orden.
func statementLocks(root ast.ASTNode) []tableLock {
	modes := map[string]lockmanager.LockMode{}
	add := func(name string, mode lockmanager.LockMode) {
		if name == "" {
			return
		}
		key := strings.ToLower(name)
		if prev, ok := modes[key]; ok && prev == lockmanager.Exclusive {
			return
		}
		modes[key] = mode
	}
	switch n := root.(type) {
	case *ast.Select:
		add(n.From.Name, lockmanager.Shared)
		for _, j := range n.Join {
			add(j.Table.Name, lockmanager.Shared)
		}
	case *ast.Insert:
		add(n.Table.Name, lockmanager.Exclusive)
	case *ast.Update:
		add(n.Table.Name, lockmanager.Exclusive)
	case *ast.Delete:
		add(n.From.Name, lockmanager.Exclusive)
	case *ast.CreateTable:
		add(n.Name, lockmanager.Exclusive)
	case *ast.CreateIndex:
		add(n.Table, lockmanager.Exclusive)
	case *ast.DropTable:
		add(n.Name, lockmanager.Exclusive)
	case *ast.TruncateTable:
		add(n.Name, lockmanager.Exclusive)
	case *ast.AlterTable:
		add(n.Name, lockmanager.Exclusive)
	}
	out := make([]tableLock, 0, len(modes))
	for name, mode := range modes {
		out = append(out, tableLock{table: name, mode: mode})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].table < out[j].table })
	return out
}

// Exec parsea y ejecuta una sentencia en esta sesión.
func (s *Session) Exec(query string) (*Result, error) {
	e := s.e
	root, err := e.parse(query)
	if err != nil {
		return nil, err
	}
	if e.initErr != nil {
		return nil, e.initErr
	}

	// 1. Bloqueos de tabla. Dentro de una transacción los toma la propia
	// transacción (y los conserva hasta el final); en autocommit, una
	// transacción de bloqueo que solo dura esta sentencia.
	locks := statementLocks(root)
	holder := s.activeLk
	var auto *lockmanager.Transaction
	if holder == nil && len(locks) > 0 {
		auto = e.lockTxs.Begin()
		holder = auto
	}
	releaseAuto := func() {
		if auto != nil {
			_ = auto.Commit()
			e.lockTxs.Remove(auto.ID)
		}
	}
	start := time.Now()
	for _, l := range locks {
		if err := holder.Lock(tableLockKey(l.table), l.mode); err != nil {
			releaseAuto()
			return nil, s.lockFailed(l, err)
		}
	}
	waited := time.Since(start)

	// 2. Ejecución atómica de la sentencia.
	e.stmtMu.Lock()
	s.swapIn()
	res, err := e.execNode(root)
	s.swapOut()
	e.stmtMu.Unlock()
	releaseAuto()

	if res != nil && len(locks) > 0 {
		parts := make([]string, len(locks))
		for i, l := range locks {
			parts[i] = fmt.Sprintf("%s(%s)", l.table, l.mode)
		}
		scope := "hasta el fin de la sentencia (autocommit)"
		if s.active != nil {
			scope = fmt.Sprintf("hasta el COMMIT/ROLLBACK de la transacción %d (2PL estricto)", s.active.ID)
		}
		detail := fmt.Sprintf("bloqueos de tabla %s %s", strings.Join(parts, ", "), scope)
		if waited >= time.Millisecond {
			detail += fmt.Sprintf("; esperó %s a otra transacción", waited.Round(time.Millisecond))
		}
		res.Plan = append([]Step{{Kind: StepLock, Detail: detail}}, res.Plan...)
	}
	return res, err
}

// lockFailed maneja un bloqueo rechazado. Si fue por deadlock y la sesión
// estaba en una transacción, esa transacción es la víctima: se deshace entera
// para liberar sus bloqueos y que las demás avancen.
func (s *Session) lockFailed(l tableLock, err error) error {
	if s.active == nil {
		if errors.Is(err, lockmanager.ErrDeadlock) {
			return fmt.Errorf("sql: deadlock detectado al bloquear %s; la sentencia no se ejecutó, reinténtala: %w", l.table, err)
		}
		return fmt.Errorf("sql: no se pudo bloquear %s: %w", l.table, err)
	}
	e := s.e
	id := s.active.ID
	e.stmtMu.Lock()
	s.swapIn()
	_, rbErr := e.execRollback(&ast.Rollback{})
	s.swapOut()
	e.stmtMu.Unlock()
	if rbErr != nil {
		return fmt.Errorf("sql: %v al bloquear %s y el rollback falló: %w", err, l.table, rbErr)
	}
	if errors.Is(err, lockmanager.ErrDeadlock) {
		return fmt.Errorf("sql: deadlock detectado al bloquear %s(%s); la transacción %d fue elegida como víctima y se deshizo (ROLLBACK automático), reinténtala: %w",
			l.table, l.mode, id, err)
	}
	return fmt.Errorf("sql: no se pudo bloquear %s; la transacción %d se deshizo: %w", l.table, id, err)
}

// IsDeadlock indica si un error de Exec se debe a un deadlock (la transacción
// ya se deshizo y se puede reintentar).
func IsDeadlock(err error) bool { return errors.Is(err, lockmanager.ErrDeadlock) }
