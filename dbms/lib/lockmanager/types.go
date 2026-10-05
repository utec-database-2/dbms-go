package lockmanager

type TransactionID uint64

type LockMode int

const (
	Shared LockMode = iota
	Exclusive
)

func (m LockMode) String() string {
	switch m {
	case Shared:
		return "S"
	case Exclusive:
		return "X"
	default:
		return "UNKNOWN"
	}
}

type TransactionState int

const (
	Growing TransactionState = iota
	Shrinking
	Committed
	Aborted
)

func (s TransactionState) String() string {
	switch s {
	case Growing:
		return "GROWING"
	case Shrinking:
		return "SHRINKING"
	case Committed:
		return "COMMITTED"
	case Aborted:
		return "ABORTED"
	default:
		return "UNKNOWN"
	}
}
