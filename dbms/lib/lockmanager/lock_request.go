package lockmanager

type LockRequest struct {
	TxnID TransactionID
	Mode  LockMode

	granted chan struct{}
	aborted chan struct{}
}

func newLockRequest(txnID TransactionID, mode LockMode) *LockRequest {
	return &LockRequest{
		TxnID:   txnID,
		Mode:    mode,
		granted: make(chan struct{}),
		aborted: make(chan struct{}),
	}
}
