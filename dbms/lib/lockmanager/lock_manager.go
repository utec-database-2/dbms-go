package lockmanager

type LockManager struct {
	table *LockTable
}

func NewLockManager() *LockManager {
	return &LockManager{table: NewLockTable()}
}

func (lm *LockManager) Lock(txnID TransactionID, resource string, mode LockMode) error {
	return lm.table.Lock(txnID, resource, mode)
}

func (lm *LockManager) Unlock(txnID TransactionID, resource string) bool {
	return lm.table.Unlock(txnID, resource)
}

func (lm *LockManager) UnlockAll(txnID TransactionID) {
	lm.table.UnlockAll(txnID)
}

func (lm *LockManager) AbortTransaction(txnID TransactionID) {
	lm.table.RemoveWaiting(txnID)
	lm.table.UnlockAll(txnID)
}

func (lm *LockManager) Snapshot() map[string]ResourceLock {
	return lm.table.Snapshot()
}
