package lockmanager

import "sync"

type LockTable struct {
	mu    sync.Mutex
	table map[string]*ResourceLock
}

func NewLockTable() *LockTable {
	return &LockTable{table: make(map[string]*ResourceLock)}
}

func (lt *LockTable) getOrCreate(resource string) *ResourceLock {
	rl, ok := lt.table[resource]
	if !ok {
		rl = &ResourceLock{}
		lt.table[resource] = rl
	}
	return rl
}

// Lock requests a logical database lock. If it cannot be granted immediately,
// the request is queued and the calling goroutine waits until it is granted or aborted.
func (lt *LockTable) Lock(txnID TransactionID, resource string, mode LockMode) error {
	lt.mu.Lock()
	rl := lt.getOrCreate(resource)

	if index, exists := findLock(rl.Granted, txnID); exists {
		current := rl.Granted[index]

		if current.Mode == Exclusive || current.Mode == mode {
			lt.mu.Unlock()
			return nil
		}

		// S -> X upgrade. It may be granted only if no other transaction conflicts.
		if current.Mode == Shared && mode == Exclusive && compatible(txnID, rl.Granted, Exclusive) && len(rl.Waiting) == 0 {
			rl.Granted[index].Mode = Exclusive
			lt.mu.Unlock()
			return nil
		}
	}

	// Fairness: once someone is waiting, new compatible requests join the queue
	// instead of bypassing older requests.
	if compatible(txnID, rl.Granted, mode) && len(rl.Waiting) == 0 {
		rl.Granted = append(rl.Granted, Lock{TxnID: txnID, Mode: mode})
		lt.mu.Unlock()
		return nil
	}

	request := newLockRequest(txnID, mode)
	rl.Waiting = append(rl.Waiting, request)
	lt.mu.Unlock()

	select {
	case <-request.granted:
		return nil
	case <-request.aborted:
		return ErrTransactionAborted
	}
}

func (lt *LockTable) Unlock(txnID TransactionID, resource string) bool {
	lt.mu.Lock()
	defer lt.mu.Unlock()

	rl, exists := lt.table[resource]
	if !exists {
		return false
	}

	index, exists := findLock(rl.Granted, txnID)
	if !exists {
		return false
	}

	rl.Granted = append(rl.Granted[:index], rl.Granted[index+1:]...)
	lt.processWaitingQueue(resource, rl)
	lt.cleanup(resource, rl)
	return true
}

func (lt *LockTable) UnlockAll(txnID TransactionID) {
	lt.mu.Lock()
	defer lt.mu.Unlock()

	for resource, rl := range lt.table {
		filtered := rl.Granted[:0]
		for _, lock := range rl.Granted {
			if lock.TxnID != txnID {
				filtered = append(filtered, lock)
			}
		}
		rl.Granted = filtered
		lt.processWaitingQueue(resource, rl)
		lt.cleanup(resource, rl)
	}
}

// RemoveWaiting removes all queued requests belonging to a transaction and wakes
// those requests with an abort result. It does not release already granted locks.
func (lt *LockTable) RemoveWaiting(txnID TransactionID) {
	lt.mu.Lock()
	defer lt.mu.Unlock()

	for resource, rl := range lt.table {
		filtered := rl.Waiting[:0]
		for _, request := range rl.Waiting {
			if request.TxnID == txnID {
				close(request.aborted)
				continue
			}
			filtered = append(filtered, request)
		}
		rl.Waiting = filtered
		lt.cleanup(resource, rl)
	}
}

func (lt *LockTable) processWaitingQueue(resource string, rl *ResourceLock) {
	for len(rl.Waiting) > 0 {
		request := rl.Waiting[0]

		if !compatible(request.TxnID, rl.Granted, request.Mode) {
			return
		}

		rl.Waiting = rl.Waiting[1:]

		index, exists := findLock(rl.Granted, request.TxnID)
		if exists {
			rl.Granted[index].Mode = request.Mode
		} else {
			rl.Granted = append(rl.Granted, Lock{TxnID: request.TxnID, Mode: request.Mode})
		}

		close(request.granted)

		// An X lock is exclusive, so nothing after it can be granted now.
		if request.Mode == Exclusive {
			return
		}
	}
}

func (lt *LockTable) cleanup(resource string, rl *ResourceLock) {
	if len(rl.Granted) == 0 && len(rl.Waiting) == 0 {
		delete(lt.table, resource)
	}
}

// Snapshot returns a copy suitable for debugging/tests. It does not expose internal slices.
func (lt *LockTable) Snapshot() map[string]ResourceLock {
	lt.mu.Lock()
	defer lt.mu.Unlock()

	result := make(map[string]ResourceLock, len(lt.table))
	for resource, rl := range lt.table {
		copyRL := ResourceLock{}
		copyRL.Granted = append([]Lock(nil), rl.Granted...)
		for _, req := range rl.Waiting {
			copyRL.Waiting = append(copyRL.Waiting, &LockRequest{TxnID: req.TxnID, Mode: req.Mode})
		}
		result[resource] = copyRL
	}
	return result
}
