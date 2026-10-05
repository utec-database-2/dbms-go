package lockmanager

// BuildWaitForGraph arma el grafo de espera: una arista T1 -> T2 significa que
// T1 espera un bloqueo que T2 tiene (o que T2 pidió antes en la misma cola).
func (lt *LockTable) BuildWaitForGraph() *WaitForGraph {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	return lt.buildWaitForGraphLocked()
}

// buildWaitForGraphLocked es BuildWaitForGraph con lt.mu ya tomado.
func (lt *LockTable) buildWaitForGraphLocked() *WaitForGraph {
	graph := NewWaitForGraph()

	for _, rl := range lt.table {
		for i, request := range rl.Waiting {
			for _, lock := range rl.Granted {
				if lock.TxnID == request.TxnID {
					continue
				}
				if lock.Mode == Shared && request.Mode == Shared {
					continue
				}
				graph.AddEdge(request.TxnID, lock.TxnID)
			}
			// La cola es FIFO: una petición también espera a las incompatibles
			// que llegaron antes que ella.
			for _, earlier := range rl.Waiting[:i] {
				if earlier.TxnID == request.TxnID {
					continue
				}
				if earlier.Mode == Shared && request.Mode == Shared {
					continue
				}
				graph.AddEdge(request.TxnID, earlier.TxnID)
			}
		}
	}

	return graph
}

func (lm *LockManager) HasDeadlock() bool {
	return lm.table.BuildWaitForGraph().HasCycle()
}
