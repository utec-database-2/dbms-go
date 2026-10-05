package lockmanager

type WaitForGraph struct {
	edges map[TransactionID]map[TransactionID]struct{}
}

func NewWaitForGraph() *WaitForGraph {
	return &WaitForGraph{edges: make(map[TransactionID]map[TransactionID]struct{})}
}

func (g *WaitForGraph) AddEdge(from, to TransactionID) {
	if from == to {
		return
	}
	if g.edges[from] == nil {
		g.edges[from] = make(map[TransactionID]struct{})
	}
	g.edges[from][to] = struct{}{}
}

func (g *WaitForGraph) HasCycle() bool {
	visited := make(map[TransactionID]bool)
	stack := make(map[TransactionID]bool)

	var dfs func(TransactionID) bool
	dfs = func(node TransactionID) bool {
		if stack[node] {
			return true
		}
		if visited[node] {
			return false
		}
		visited[node] = true
		stack[node] = true
		for next := range g.edges[node] {
			if dfs(next) {
				return true
			}
		}
		stack[node] = false
		return false
	}

	for node := range g.edges {
		if dfs(node) {
			return true
		}
	}
	return false
}

func (g *WaitForGraph) Edges() map[TransactionID][]TransactionID {
	result := make(map[TransactionID][]TransactionID, len(g.edges))
	for from, targets := range g.edges {
		for to := range targets {
			result[from] = append(result[from], to)
		}
	}
	return result
}
