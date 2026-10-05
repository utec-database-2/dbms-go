package lockmanager

type Lock struct {
	TxnID TransactionID
	Mode  LockMode
}
