package bplus

import (
	"bytes"
	"fmt"
	"sync"
)

// Tree is an in-memory B+ tree. order is the maximum number of children of an
// internal node, therefore a node contains at most order-1 keys.
//
// Duplicate keys are supported as long as Value differs. Exact duplicate
// (key,value) pairs are ignored by Insert.
type Tree[V comparable] struct {
	mu    sync.RWMutex
	root  *node[V]
	order int
	size  int
}

type TreeStats struct {
	Order   int
	Entries int
	Height  int
	Leaves  int
	Nodes   int
}

func New[V comparable](order int) (*Tree[V], error) {
	if order < 3 {
		return nil, ErrInvalidOrder
	}
	return &Tree[V]{order: order, root: newLeaf[V]()}, nil
}

func (t *Tree[V]) maxKeys() int { return t.order - 1 }

func lowerBound(keys [][]byte, key []byte) int {
	lo, hi := 0, len(keys)
	for lo < hi {
		m := lo + (hi-lo)/2
		if bytes.Compare(keys[m], key) < 0 {
			lo = m + 1
		} else {
			hi = m
		}
	}
	return lo
}

func upperBound(keys [][]byte, key []byte) int {
	lo, hi := 0, len(keys)
	for lo < hi {
		m := lo + (hi-lo)/2
		if bytes.Compare(keys[m], key) <= 0 {
			lo = m + 1
		} else {
			hi = m
		}
	}
	return lo
}

func (t *Tree[V]) findLeaf(key []byte) *node[V] {
	n := t.root
	for !n.leaf {
		n = n.children[upperBound(n.keys, key)]
	}
	return n
}

func (t *Tree[V]) Search(key []byte) []V {
	t.mu.RLock()
	defer t.mu.RUnlock()

	leaf := t.findLeaf(key)
	i := lowerBound(leaf.keys, key)
	if i == len(leaf.keys) || !bytes.Equal(leaf.keys[i], key) {
		return nil
	}
	return append([]V(nil), leaf.buckets[i]...)
}

func (t *Tree[V]) Contains(key []byte, value V) bool {
	for _, v := range t.Search(key) {
		if v == value {
			return true
		}
	}
	return false
}

func (t *Tree[V]) Insert(key []byte, value V) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.insertUnlocked(key, value)
}

func (t *Tree[V]) insertUnlocked(key []byte, value V) error {
	if key == nil {
		key = []byte{}
	}

	// Keep a path of internal nodes and selected child positions.
	path := make([]*node[V], 0, 8)
	childPos := make([]int, 0, 8)
	n := t.root
	for !n.leaf {
		idx := upperBound(n.keys, key)
		path = append(path, n)
		childPos = append(childPos, idx)
		n = n.children[idx]
	}

	pos := lowerBound(n.keys, key)
	if pos < len(n.keys) && bytes.Equal(n.keys[pos], key) {
		for _, existing := range n.buckets[pos] {
			if existing == value {
				return nil
			}
		}
		n.buckets[pos] = append(n.buckets[pos], value)
		t.size++
		return nil
	}

	n.keys = append(n.keys, nil)
	copy(n.keys[pos+1:], n.keys[pos:])
	n.keys[pos] = cloneKey(key)
	n.buckets = append(n.buckets, nil)
	copy(n.buckets[pos+1:], n.buckets[pos:])
	n.buckets[pos] = []V{value}
	t.size++

	if len(n.keys) <= t.maxKeys() {
		t.refreshAncestors(path, childPos)
		return nil
	}

	right, sep := t.splitLeaf(n)
	return t.propagateSplit(path, childPos, n, right, sep)
}

func (t *Tree[V]) splitLeaf(left *node[V]) (*node[V], []byte) {
	mid := len(left.keys) / 2
	right := newLeaf[V]()
	right.keys = append(right.keys, left.keys[mid:]...)
	right.buckets = append(right.buckets, left.buckets[mid:]...)
	left.keys = left.keys[:mid]
	left.buckets = left.buckets[:mid]
	right.next = left.next
	left.next = right
	return right, cloneKey(right.keys[0])
}

func (t *Tree[V]) splitInternal(left *node[V]) (*node[V], []byte) {
	mid := len(left.keys) / 2
	promote := cloneKey(left.keys[mid])
	right := newInternal[V]()
	right.keys = append(right.keys, left.keys[mid+1:]...)
	right.children = append(right.children, left.children[mid+1:]...)
	left.keys = left.keys[:mid]
	left.children = left.children[:mid+1]
	return right, promote
}

func (t *Tree[V]) propagateSplit(path []*node[V], childPos []int, left, right *node[V], sep []byte) error {
	for level := len(path) - 1; level >= 0; level-- {
		parent := path[level]
		pos := childPos[level]

		parent.keys = append(parent.keys, nil)
		copy(parent.keys[pos+1:], parent.keys[pos:])
		parent.keys[pos] = cloneKey(sep)

		parent.children = append(parent.children, nil)
		copy(parent.children[pos+2:], parent.children[pos+1:])
		parent.children[pos+1] = right

		if len(parent.keys) <= t.maxKeys() {
			t.refreshAncestors(path[:level], childPos[:level])
			return nil
		}

		left = parent
		right, sep = t.splitInternal(parent)
	}

	root := newInternal[V]()
	root.keys = [][]byte{cloneKey(sep)}
	root.children = []*node[V]{left, right}
	t.root = root
	return nil
}

func firstKey[V comparable](n *node[V]) []byte {
	for !n.leaf {
		n = n.children[0]
	}
	if len(n.keys) == 0 {
		return nil
	}
	return n.keys[0]
}

func (t *Tree[V]) refreshAncestors(path []*node[V], childPos []int) {
	for level := len(path) - 1; level >= 0; level-- {
		parent := path[level]
		pos := childPos[level]
		if pos > 0 {
			parent.keys[pos-1] = cloneKey(firstKey(parent.children[pos]))
		}
	}
}

// RangeSearch returns entries with low <= key <= high in key order.
func (t *Tree[V]) RangeSearch(low, high []byte) ([]Entry[V], error) {
	if bytes.Compare(low, high) > 0 {
		return nil, ErrInvalidRange
	}
	t.mu.RLock()
	defer t.mu.RUnlock()

	leaf := t.findLeaf(low)
	out := make([]Entry[V], 0)
	for leaf != nil {
		for i, k := range leaf.keys {
			if bytes.Compare(k, low) < 0 {
				continue
			}
			if bytes.Compare(k, high) > 0 {
				return out, nil
			}
			for _, value := range leaf.buckets[i] {
				out = append(out, Entry[V]{Key: cloneKey(k), Value: value})
			}
		}
		leaf = leaf.next
	}
	return out, nil
}

func (t *Tree[V]) Items() []Entry[V] {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.itemsUnlocked()
}

func (t *Tree[V]) itemsUnlocked() []Entry[V] {
	n := t.root
	for !n.leaf {
		n = n.children[0]
	}
	out := make([]Entry[V], 0, t.size)
	for n != nil {
		for i, k := range n.keys {
			for _, value := range n.buckets[i] {
				out = append(out, Entry[V]{Key: cloneKey(k), Value: value})
			}
		}
		n = n.next
	}
	return out
}

// Delete removes exactly one (key,value). For correctness and simplicity the
// tree is rebuilt from the remaining ordered entries. Insert/search/range keep
// the normal logarithmic B+ behavior; deletion is O(n), which is explicit here
// and can later be replaced by redistribution/merge without changing callers.
func (t *Tree[V]) Delete(key []byte, value V) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	items := t.itemsUnlocked()
	found := false
	kept := items[:0]
	for _, e := range items {
		if !found && bytes.Equal(e.Key, key) && e.Value == value {
			found = true
			continue
		}
		kept = append(kept, e)
	}
	if !found {
		return ErrNotFound
	}
	return t.bulkLoadUnlocked(kept)
}

func (t *Tree[V]) BulkLoad(entries []Entry[V]) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.bulkLoadUnlocked(entries)
}

func (t *Tree[V]) bulkLoadUnlocked(entries []Entry[V]) error {
	t.root = newLeaf[V]()
	t.size = 0
	for _, e := range entries {
		if err := t.insertUnlocked(e.Key, e.Value); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tree[V]) Stats() TreeStats {
	t.mu.RLock()
	defer t.mu.RUnlock()

	s := TreeStats{Order: t.order, Entries: t.size}
	var walk func(*node[V], int)
	walk = func(n *node[V], depth int) {
		s.Nodes++
		if depth > s.Height {
			s.Height = depth
		}
		if n.leaf {
			s.Leaves++
			return
		}
		for _, c := range n.children {
			walk(c, depth+1)
		}
	}
	walk(t.root, 1)
	return s
}

func (t *Tree[V]) String() string {
	s := t.Stats()
	return fmt.Sprintf("B+Tree(order=%d entries=%d height=%d leaves=%d)", s.Order, s.Entries, s.Height, s.Leaves)
}
