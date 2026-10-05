package lockmanager

import "errors"

var (
	ErrTransactionAborted    = errors.New("transaction aborted")
	ErrTransactionFinished   = errors.New("transaction already committed or aborted")
	ErrTransactionNotGrowing = errors.New("transaction cannot acquire locks outside growing phase")
	ErrLockNotHeld           = errors.New("transaction does not hold the requested lock")
	// ErrDeadlock indica que esperar el bloqueo cerraría un ciclo en el grafo
	// de espera. La transacción que lo pidió es la víctima: debe abortar.
	ErrDeadlock = errors.New("deadlock detectado")
)
