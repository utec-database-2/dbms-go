package bplus

import (
	"bytes"
	"fmt"
)

func (t *Tree[V]) Validate() error {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.root == nil {
		return fmt.Errorf("bplus: nil root")
	}
	count := 0
	leafDepth := -1
	leaves := make([]*node[V], 0)

	var walk func(*node[V], int, bool) error
	walk = func(n *node[V], depth int, isRoot bool) error {
		if n == nil {
			return fmt.Errorf("bplus: nil child")
		}
		for i := 1; i < len(n.keys); i++ {
			if bytes.Compare(n.keys[i-1], n.keys[i]) > 0 {
				return fmt.Errorf("bplus: unsorted keys at depth %d", depth)
			}
		}
		if len(n.keys) > t.maxKeys() {
			return fmt.Errorf("bplus: node overflow: %d keys > %d", len(n.keys), t.maxKeys())
		}
		if n.leaf {
			if len(n.buckets) != len(n.keys) {
				return fmt.Errorf("bplus: leaf has %d keys but %d buckets", len(n.keys), len(n.buckets))
			}
			for i, bucket := range n.buckets {
				if len(bucket) == 0 {
					return fmt.Errorf("bplus: empty value bucket for leaf key %d", i)
				}
			}
			if leafDepth == -1 {
				leafDepth = depth
			} else if leafDepth != depth {
				return fmt.Errorf("bplus: leaves at different depths")
			}
			for _, bucket := range n.buckets {
				count += len(bucket)
			}
			leaves = append(leaves, n)
			return nil
		}
		if len(n.children) != len(n.keys)+1 {
			return fmt.Errorf("bplus: internal node has %d keys and %d children", len(n.keys), len(n.children))
		}
		if !isRoot && len(n.children) < 2 {
			return fmt.Errorf("bplus: internal node with fewer than two children")
		}
		for i := 1; i < len(n.children); i++ {
			fk := firstKey(n.children[i])
			if fk == nil || !bytes.Equal(n.keys[i-1], fk) {
				return fmt.Errorf("bplus: separator %d does not match right subtree minimum", i-1)
			}
		}
		for _, c := range n.children {
			if err := walk(c, depth+1, false); err != nil {
				return err
			}
		}
		return nil
	}

	if err := walk(t.root, 1, true); err != nil {
		return err
	}
	if count != t.size {
		return fmt.Errorf("bplus: size=%d but leaves contain %d entries", t.size, count)
	}
	for i := 0; i+1 < len(leaves); i++ {
		if leaves[i].next != leaves[i+1] {
			return fmt.Errorf("bplus: broken leaf chain")
		}
	}
	if len(leaves) > 0 && leaves[len(leaves)-1].next != nil {
		return fmt.Errorf("bplus: last leaf points to another leaf")
	}
	return nil
}
