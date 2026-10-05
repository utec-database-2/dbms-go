package transaction

import (
	"fmt"
	"sort"
	"sync"

	"github.com/dbms-go/v2/dbms/lib/lockmanager"
)

// Locks son los bloqueos lógicos que una transacción mantiene abiertos.
//
// El motor pide un bloqueo por clave tocada ("tabla:columna=valor") al
// lockmanager, que decide si se concede o si hay que esperar. Aquí solo se
// lleva la cuenta de qué se pidió, para poder liberarlo todo al confirmar o
// al deshacer: con 2PL estricto los bloqueos no se sueltan antes de tiempo.
type Locks struct {
	mu   sync.Mutex
	tx   *lockmanager.Transaction
	held map[string]lockmanager.LockMode
}

// NewLocks crea un conjunto de bloqueos para la transacción del lockmanager tx.
// Si tx es nil los bloqueos solo se registran (útil en pruebas o cuando el motor
// corre sin gestor de bloqueos).
func NewLocks(tx *lockmanager.Transaction) *Locks {
	return &Locks{tx: tx, held: make(map[string]lockmanager.LockMode)}
}

// Lock pide un bloqueo sobre resource. Repetir el mismo recurso con el mismo
// modo es un no-op: el protocolo 2PL concede el bloqueo una sola vez.
func (l *Locks) Lock(resource string, mode lockmanager.LockMode) error {
	if resource == "" {
		return fmt.Errorf("transaction: recurso de bloqueo vacío")
	}
	l.mu.Lock()
	prev, ok := l.held[resource]
	if ok && (prev == mode || mode == lockmanager.Shared) {
		l.mu.Unlock()
		return nil
	}
	l.mu.Unlock()

	if l.tx != nil {
		if err := l.tx.Lock(resource, mode); err != nil {
			return err
		}
	}

	l.mu.Lock()
	l.held[resource] = mode
	l.mu.Unlock()
	return nil
}

// Held lista los recursos bloqueados, ordenado, para el panel y las pruebas.
func (l *Locks) Held() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0, len(l.held))
	for res := range l.held {
		out = append(out, res)
	}
	sort.Strings(out)
	return out
}

// ReleaseAll suelta todos los bloqueos de la transacción.
func (l *Locks) ReleaseAll() {
	l.mu.Lock()
	resources := make([]string, 0, len(l.held))
	for res := range l.held {
		resources = append(resources, res)
	}
	l.held = make(map[string]lockmanager.LockMode)
	l.mu.Unlock()

	if l.tx == nil {
		return
	}
	sort.Strings(resources)
	for _, res := range resources {
		// El lockmanager puede rechazar el desbloqueo si la transacción ya
		// terminó, y en ese caso ya no hay nada que soltar.
		_ = l.tx.Unlock(res)
	}
}
