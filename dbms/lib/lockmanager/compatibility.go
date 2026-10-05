package lockmanager

func compatible(txnID TransactionID, granted []Lock, requested LockMode) bool {
	for _, lock := range granted {
		if lock.TxnID == txnID {
			continue
		}
		if lock.Mode == Shared && requested == Shared {
			continue
		}
		return false
	}
	return true
}

func findLock(locks []Lock, txnID TransactionID) (int, bool) {
	for i, lock := range locks {
		if lock.TxnID == txnID {
			return i, true
		}
	}
	return -1, false
}
