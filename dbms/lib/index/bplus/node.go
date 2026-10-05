package bplus

type node[V comparable] struct {
	leaf bool

	// Internal node: keys[i] is the smallest key reachable through children[i+1].
	// Leaf node: keys[i] identifies buckets[i], which contains every value for
	// that key. Keeping duplicates in one bucket prevents one key from spanning
	// multiple leaves and makes exact lookup deterministic.
	keys [][]byte

	children []*node[V]
	buckets  [][]V

	// Leaves form a linked list for range scans.
	next *node[V]
}

func newLeaf[V comparable]() *node[V]     { return &node[V]{leaf: true} }
func newInternal[V comparable]() *node[V] { return &node[V]{leaf: false} }
