package lockmanager

import "sync"

type Transaction struct {
	mu sync.Mutex

	ID    TransactionID
	State TransactionState

	lm *LockManager
}

func (tx *Transaction) Lock(resource string, mode LockMode) error {
	tx.mu.Lock()
	if tx.State == Committed || tx.State == Aborted {
		tx.mu.Unlock()
		return ErrTransactionFinished
	}
	if tx.State != Growing {
		tx.mu.Unlock()
		return ErrTransactionNotGrowing
	}
	tx.mu.Unlock()

	return tx.lm.Lock(tx.ID, resource, mode)
}

func (tx *Transaction) Unlock(resource string) error {
	tx.mu.Lock()
	if tx.State == Committed || tx.State == Aborted {
		tx.mu.Unlock()
		return ErrTransactionFinished
	}

	// First unlock starts the shrinking phase under 2PL.
	if tx.State == Growing {
		tx.State = Shrinking
	}
	tx.mu.Unlock()

	if !tx.lm.Unlock(tx.ID, resource) {
		return ErrLockNotHeld
	}
	return nil
}

func (tx *Transaction) Commit() error {
	tx.mu.Lock()
	if tx.State == Committed || tx.State == Aborted {
		tx.mu.Unlock()
		return ErrTransactionFinished
	}
	tx.State = Committed
	tx.mu.Unlock()

	tx.lm.UnlockAll(tx.ID)
	return nil
}

func (tx *Transaction) Abort() error {
	tx.mu.Lock()
	if tx.State == Committed || tx.State == Aborted {
		tx.mu.Unlock()
		return ErrTransactionFinished
	}
	tx.State = Aborted
	tx.mu.Unlock()

	tx.lm.AbortTransaction(tx.ID)
	return nil
}

func (tx *Transaction) GetState() TransactionState {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	return tx.State
}
