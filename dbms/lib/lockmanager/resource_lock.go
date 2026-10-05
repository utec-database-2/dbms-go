package lockmanager

type ResourceLock struct {
	Granted []Lock
	Waiting []*LockRequest
}
