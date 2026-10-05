package lockmanager

import "sync"

type TransactionManager struct {
	mu sync.Mutex

	nextID TransactionID
	lm     *LockManager

	transactions map[TransactionID]*Transaction
}

func NewTransactionManager(lm *LockManager) *TransactionManager {
	return &TransactionManager{
		lm:           lm,
		transactions: make(map[TransactionID]*Transaction),
	}
}

func (tm *TransactionManager) Begin() *Transaction {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	tm.nextID++
	tx := &Transaction{ID: tm.nextID, State: Growing, lm: tm.lm}
	tm.transactions[tx.ID] = tx
	return tx
}

func (tm *TransactionManager) Get(id TransactionID) (*Transaction, bool) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tx, ok := tm.transactions[id]
	return tx, ok
}

func (tm *TransactionManager) Remove(id TransactionID) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	delete(tm.transactions, id)
}

// LockManager devuelve el gestor de bloqueos que comparten las transacciones.
func (tm *TransactionManager) LockManager() *LockManager { return tm.lm }
