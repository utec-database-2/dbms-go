package lockmanager

func (lt *LockTable) BuildWaitForGraph() *WaitForGraph {
	lt.mu.Lock()
	defer lt.mu.Unlock()

	graph := NewWaitForGraph()

	for _, rl := range lt.table {
		for _, request := range rl.Waiting {
			for _, lock := range rl.Granted {
				if lock.TxnID == request.TxnID {
					continue
				}
				if lock.Mode == Shared && request.Mode == Shared {
					continue
				}
				graph.AddEdge(request.TxnID, lock.TxnID)
			}
		}
	}

	return graph
}

func (lm *LockManager) HasDeadlock() bool {
	return lm.table.BuildWaitForGraph().HasCycle()
}
