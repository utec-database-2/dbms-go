
package bplus

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

var (
	ErrInvalidOrder   = errors.New("bplus: order must be >= 3")
	ErrNotFound       = errors.New("bplus: entry not found")
	ErrDuplicateEntry = errors.New("bplus: duplicate key/value entry")
	ErrInvalidRange   = errors.New("bplus: invalid range")
	ErrCorruptTree    = errors.New("bplus: invariant violation")
)

type Ordered interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr |
		~string
}

// Entry representa un par clave/valor del índice. Si una clave tiene varios
// valores, Items devuelve una Entry por valor.
type Entry[K Ordered, V comparable] struct {
	Key   K
	Value V
}

// Bucket representa una clave junto con todos sus valores asociados.
type Bucket[K Ordered, V comparable] struct {
	Key    K
	Values []V
}

// TreeStats expone métricas útiles para validación y experimentos.
type TreeStats struct {
	Order         int
	Height        int
	NodeCount     int
	InternalNodes int
	LeafNodes     int
	DistinctKeys  int
	Values        int
}

type node[K Ordered, V comparable] struct {
	leaf bool

	// En hojas: keys[i] corresponde a values[i].
	// En internos: keys[i] == mínimo del hijo i+1.
	keys     []K
	values   [][]V
	children []*node[K, V]

	parent *node[K, V]
	next   *node[K, V]
	prev   *node[K, V]
}


//   - un nodo interno tiene como máximo order hijos;
//   - una hoja tiene como máximo order-1 claves.
//
// Los duplicados se representan como buckets: una clave aparece una sola vez
// en la hoja y puede tener varios valores distintos.
type Tree[K Ordered, V comparable] struct {
	mu sync.RWMutex

	order int
	root  *node[K, V]

	distinctKeys int
	valueCount   int
}

func New[K Ordered, V comparable](order int) (*Tree[K, V], error) {
	if order < 3 {
		return nil, ErrInvalidOrder
	}
	return &Tree[K, V]{
		order: order,
		root:  &node[K, V]{leaf: true},
	}, nil
}

func MustNew[K Ordered, V comparable](order int) *Tree[K, V] {
	t, err := New[K, V](order)
	if err != nil {
		panic(err)
	}
	return t
}

func (t *Tree[K, V]) Order() int { return t.order }

func (t *Tree[K, V]) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.valueCount
}

func (t *Tree[K, V]) DistinctLen() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.distinctKeys
}

func lowerBound[K Ordered](keys []K, key K) int {
	lo, hi := 0, len(keys)
	for lo < hi {
		mid := lo + (hi-lo)/2
		if keys[mid] < key {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// childIndex aplica upper_bound porque keys[i] es el mínimo del hijo i+1.
// Si key == separador debemos bajar al hijo derecho.
func childIndex[K Ordered](keys []K, key K) int {
	lo, hi := 0, len(keys)
	for lo < hi {
		mid := lo + (hi-lo)/2
		if key < keys[mid] {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return lo
}

func cloneSlice[T any](in []T) []T {
	if len(in) == 0 {
		return nil
	}
	out := make([]T, len(in))
	copy(out, in)
	return out
}

func insertAt[T any](s []T, idx int, v T) []T {
	s = append(s, v)
	copy(s[idx+1:], s[idx:len(s)-1])
	s[idx] = v
	return s
}

func removeAt[T any](s []T, idx int) []T {
	copy(s[idx:], s[idx+1:])
	var zero T
	s[len(s)-1] = zero
	return s[:len(s)-1]
}

func containsValue[V comparable](values []V, value V) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func (t *Tree[K, V]) findLeafLocked(key K) *node[K, V] {
	n := t.root
	for !n.leaf {
		n = n.children[childIndex(n.keys, key)]
	}
	return n
}

func (t *Tree[K, V]) leftmostLeafLocked() *node[K, V] {
	n := t.root
	for !n.leaf {
		n = n.children[0]
	}
	return n
}

func minKey[K Ordered, V comparable](n *node[K, V]) (K, bool) {
	for !n.leaf {
		if len(n.children) == 0 {
			var zero K
			return zero, false
		}
		n = n.children[0]
	}
	if len(n.keys) == 0 {
		var zero K
		return zero, false
	}
	return n.keys[0], true
}

func (t *Tree[K, V]) refreshNodeKeysLocked(n *node[K, V]) error {
	if n == nil || n.leaf {
		return nil
	}
	if len(n.children) == 0 {
		n.keys = nil
		return nil
	}
	keys := make([]K, len(n.children)-1)
	for i := 1; i < len(n.children); i++ {
		k, ok := minKey(n.children[i])
		if !ok {
			return fmt.Errorf("%w: internal child %d has no minimum key", ErrCorruptTree, i)
		}
		keys[i-1] = k
	}
	n.keys = keys
	return nil
}

func (t *Tree[K, V]) refreshAncestorsLocked(n *node[K, V]) error {
	for n != nil {
		if err := t.refreshNodeKeysLocked(n); err != nil {
			return err
		}
		n = n.parent
	}
	return nil
}

func (t *Tree[K, V]) maxLeafKeys() int { return t.order - 1 }

func (t *Tree[K, V]) minLeafKeys() int {
	// ceil((order-1)/2)
	return t.order / 2
}

func (t *Tree[K, V]) minInternalChildren() int {
	// ceil(order/2)
	return (t.order + 1) / 2
}

func (t *Tree[K, V]) Search(key K) []V {
	t.mu.RLock()
	defer t.mu.RUnlock()

	leaf := t.findLeafLocked(key)
	i := lowerBound(leaf.keys, key)
	if i >= len(leaf.keys) || leaf.keys[i] != key {
		return nil
	}
	return cloneSlice(leaf.values[i])
}

func (t *Tree[K, V]) Contains(key K, value V) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	leaf := t.findLeafLocked(key)
	i := lowerBound(leaf.keys, key)
	return i < len(leaf.keys) && leaf.keys[i] == key && containsValue(leaf.values[i], value)
}

func (t *Tree[K, V]) Insert(key K, value V) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.insertLocked(key, value)
}

func (t *Tree[K, V]) insertLocked(key K, value V) error {
	leaf := t.findLeafLocked(key)
	i := lowerBound(leaf.keys, key)

	if i < len(leaf.keys) && leaf.keys[i] == key {
		if containsValue(leaf.values[i], value) {
			return ErrDuplicateEntry
		}
		leaf.values[i] = append(leaf.values[i], value)
		t.valueCount++
		return nil
	}

	leaf.keys = insertAt(leaf.keys, i, key)
	leaf.values = insertAt(leaf.values, i, []V{value})
	t.distinctKeys++
	t.valueCount++

	if len(leaf.keys) <= t.maxLeafKeys() {
		return t.refreshAncestorsLocked(leaf.parent)
	}
	return t.splitLeafLocked(leaf)
}

func (t *Tree[K, V]) splitLeafLocked(leaf *node[K, V]) error {
	split := (len(leaf.keys) + 1) / 2
	right := &node[K, V]{
		leaf:   true,
		keys:   cloneSlice(leaf.keys[split:]),
		values: append([][]V(nil), leaf.values[split:]...),
		parent: leaf.parent,
		next:   leaf.next,
		prev:   leaf,
	}
	// Copiar buckets para que los backing arrays no queden compartidos.
	for i := range right.values {
		right.values[i] = cloneSlice(right.values[i])
	}

	leaf.keys = leaf.keys[:split]
	leaf.values = leaf.values[:split]
	if leaf.next != nil {
		leaf.next.prev = right
	}
	leaf.next = right

	return t.insertSiblingLocked(leaf, right)
}

func (t *Tree[K, V]) childPositionLocked(parent, child *node[K, V]) int {
	for i, c := range parent.children {
		if c == child {
			return i
		}
	}
	return -1
}

func (t *Tree[K, V]) insertSiblingLocked(left, right *node[K, V]) error {
	if left.parent == nil {
		root := &node[K, V]{leaf: false, children: []*node[K, V]{left, right}}
		left.parent = root
		right.parent = root
		t.root = root
		return t.refreshNodeKeysLocked(root)
	}

	parent := left.parent
	pos := t.childPositionLocked(parent, left)
	if pos < 0 {
		return fmt.Errorf("%w: split child not present in parent", ErrCorruptTree)
	}
	parent.children = insertAt(parent.children, pos+1, right)
	right.parent = parent
	if err := t.refreshNodeKeysLocked(parent); err != nil {
		return err
	}

	if len(parent.children) > t.order {
		return t.splitInternalLocked(parent)
	}
	return t.refreshAncestorsLocked(parent.parent)
}

func (t *Tree[K, V]) splitInternalLocked(n *node[K, V]) error {
	split := (len(n.children) + 1) / 2
	rightChildren := append([]*node[K, V](nil), n.children[split:]...)
	right := &node[K, V]{leaf: false, children: rightChildren, parent: n.parent}
	n.children = n.children[:split]

	for _, c := range right.children {
		c.parent = right
	}
	if err := t.refreshNodeKeysLocked(n); err != nil {
		return err
	}
	if err := t.refreshNodeKeysLocked(right); err != nil {
		return err
	}
	return t.insertSiblingLocked(n, right)
}

func (t *Tree[K, V]) Delete(key K, value V) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	leaf := t.findLeafLocked(key)
	i := lowerBound(leaf.keys, key)
	if i >= len(leaf.keys) || leaf.keys[i] != key {
		return ErrNotFound
	}

	bucket := leaf.values[i]
	vi := -1
	for j, v := range bucket {
		if v == value {
			vi = j
			break
		}
	}
	if vi < 0 {
		return ErrNotFound
	}

	bucket = removeAt(bucket, vi)
	t.valueCount--
	if len(bucket) > 0 {
		leaf.values[i] = bucket
		return nil
	}

	leaf.keys = removeAt(leaf.keys, i)
	leaf.values = removeAt(leaf.values, i)
	t.distinctKeys--
	return t.afterLeafKeyRemovalLocked(leaf)
}

// DeleteKey elimina todos los valores asociados a key y devuelve cuántos había.
func (t *Tree[K, V]) DeleteKey(key K) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	leaf := t.findLeafLocked(key)
	i := lowerBound(leaf.keys, key)
	if i >= len(leaf.keys) || leaf.keys[i] != key {
		return 0, ErrNotFound
	}
	removed := len(leaf.values[i])
	leaf.keys = removeAt(leaf.keys, i)
	leaf.values = removeAt(leaf.values, i)
	t.distinctKeys--
	t.valueCount -= removed
	if err := t.afterLeafKeyRemovalLocked(leaf); err != nil {
		return 0, err
	}
	return removed, nil
}

func (t *Tree[K, V]) afterLeafKeyRemovalLocked(leaf *node[K, V]) error {
	if leaf == t.root {
		return nil
	}
	if len(leaf.keys) >= t.minLeafKeys() {
		return t.refreshAncestorsLocked(leaf.parent)
	}
	return t.rebalanceLeafLocked(leaf)
}

func (t *Tree[K, V]) rebalanceLeafLocked(leaf *node[K, V]) error {
	parent := leaf.parent
	pos := t.childPositionLocked(parent, leaf)
	if pos < 0 {
		return fmt.Errorf("%w: leaf absent from parent", ErrCorruptTree)
	}

	var left, right *node[K, V]
	if pos > 0 {
		left = parent.children[pos-1]
	}
	if pos+1 < len(parent.children) {
		right = parent.children[pos+1]
	}

	if left != nil && len(left.keys) > t.minLeafKeys() {
		k := left.keys[len(left.keys)-1]
		vals := left.values[len(left.values)-1]
		left.keys = left.keys[:len(left.keys)-1]
		left.values = left.values[:len(left.values)-1]
		leaf.keys = insertAt(leaf.keys, 0, k)
		leaf.values = insertAt(leaf.values, 0, vals)
		return t.refreshAncestorsLocked(parent)
	}

	if right != nil && len(right.keys) > t.minLeafKeys() {
		k := right.keys[0]
		vals := right.values[0]
		right.keys = removeAt(right.keys, 0)
		right.values = removeAt(right.values, 0)
		leaf.keys = append(leaf.keys, k)
		leaf.values = append(leaf.values, vals)
		return t.refreshAncestorsLocked(parent)
	}

	if left != nil {
		left.keys = append(left.keys, leaf.keys...)
		left.values = append(left.values, leaf.values...)
		left.next = leaf.next
		if leaf.next != nil {
			leaf.next.prev = left
		}
		parent.children = removeAt(parent.children, pos)
		if err := t.refreshNodeKeysLocked(parent); err != nil {
			return err
		}
		return t.rebalanceInternalLocked(parent)
	}

	if right != nil {
		leaf.keys = append(leaf.keys, right.keys...)
		leaf.values = append(leaf.values, right.values...)
		leaf.next = right.next
		if right.next != nil {
			right.next.prev = leaf
		}
		parent.children = removeAt(parent.children, pos+1)
		if err := t.refreshNodeKeysLocked(parent); err != nil {
			return err
		}
		return t.rebalanceInternalLocked(parent)
	}

	return fmt.Errorf("%w: underfull leaf has no sibling", ErrCorruptTree)
}

func (t *Tree[K, V]) rebalanceInternalLocked(n *node[K, V]) error {
	if n == t.root {
		switch len(n.children) {
		case 0:
			t.root = &node[K, V]{leaf: true}
		case 1:
			child := n.children[0]
			child.parent = nil
			t.root = child
		default:
			if err := t.refreshNodeKeysLocked(n); err != nil {
				return err
			}
		}
		return nil
	}

	if len(n.children) >= t.minInternalChildren() {
		if err := t.refreshNodeKeysLocked(n); err != nil {
			return err
		}
		return t.refreshAncestorsLocked(n.parent)
	}

	parent := n.parent
	pos := t.childPositionLocked(parent, n)
	if pos < 0 {
		return fmt.Errorf("%w: internal node absent from parent", ErrCorruptTree)
	}

	var left, right *node[K, V]
	if pos > 0 {
		left = parent.children[pos-1]
	}
	if pos+1 < len(parent.children) {
		right = parent.children[pos+1]
	}

	if left != nil && len(left.children) > t.minInternalChildren() {
		child := left.children[len(left.children)-1]
		left.children = left.children[:len(left.children)-1]
		n.children = insertAt(n.children, 0, child)
		child.parent = n
		if err := t.refreshNodeKeysLocked(left); err != nil {
			return err
		}
		if err := t.refreshNodeKeysLocked(n); err != nil {
			return err
		}
		return t.refreshAncestorsLocked(parent)
	}

	if right != nil && len(right.children) > t.minInternalChildren() {
		child := right.children[0]
		right.children = removeAt(right.children, 0)
		n.children = append(n.children, child)
		child.parent = n
		if err := t.refreshNodeKeysLocked(right); err != nil {
			return err
		}
		if err := t.refreshNodeKeysLocked(n); err != nil {
			return err
		}
		return t.refreshAncestorsLocked(parent)
	}

	if left != nil {
		left.children = append(left.children, n.children...)
		for _, c := range n.children {
			c.parent = left
		}
		parent.children = removeAt(parent.children, pos)
		if err := t.refreshNodeKeysLocked(left); err != nil {
			return err
		}
		if err := t.refreshNodeKeysLocked(parent); err != nil {
			return err
		}
		return t.rebalanceInternalLocked(parent)
	}

	if right != nil {
		n.children = append(n.children, right.children...)
		for _, c := range right.children {
			c.parent = n
		}
		parent.children = removeAt(parent.children, pos+1)
		if err := t.refreshNodeKeysLocked(n); err != nil {
			return err
		}
		if err := t.refreshNodeKeysLocked(parent); err != nil {
			return err
		}
		return t.rebalanceInternalLocked(parent)
	}

	return fmt.Errorf("%w: underfull internal node has no sibling", ErrCorruptTree)
}

func (t *Tree[K, V]) RangeSearch(low, high K) ([]Entry[K, V], error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if high < low {
		return nil, ErrInvalidRange
	}

	leaf := t.findLeafLocked(low)
	out := make([]Entry[K, V], 0)
	first := true
	for leaf != nil {
		start := 0
		if first {
			start = lowerBound(leaf.keys, low)
			first = false
		}
		for i := start; i < len(leaf.keys); i++ {
			k := leaf.keys[i]
			if high < k {
				return out, nil
			}
			if k < low {
				continue
			}
			for _, v := range leaf.values[i] {
				out = append(out, Entry[K, V]{Key: k, Value: v})
			}
		}
		leaf = leaf.next
	}
	return out, nil
}

func (t *Tree[K, V]) Items() []Entry[K, V] {
	t.mu.RLock()
	defer t.mu.RUnlock()

	out := make([]Entry[K, V], 0, t.valueCount)
	for leaf := t.leftmostLeafLocked(); leaf != nil; leaf = leaf.next {
		for i, k := range leaf.keys {
			for _, v := range leaf.values[i] {
				out = append(out, Entry[K, V]{Key: k, Value: v})
			}
		}
	}
	return out
}

func (t *Tree[K, V]) Buckets() []Bucket[K, V] {
	t.mu.RLock()
	defer t.mu.RUnlock()

	out := make([]Bucket[K, V], 0, t.distinctKeys)
	for leaf := t.leftmostLeafLocked(); leaf != nil; leaf = leaf.next {
		for i, k := range leaf.keys {
			out = append(out, Bucket[K, V]{Key: k, Values: cloneSlice(leaf.values[i])})
		}
	}
	return out
}

// BulkLoad reemplaza el árbol usando entries. Ordena una copia de la entrada,
// agrupa claves duplicadas y construye el árbol de abajo hacia arriba.
// Es ideal para reconstruir índices al abrir los archivos persistentes.
func (t *Tree[K, V]) BulkLoad(entries []Entry[K, V]) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	copied := cloneSlice(entries)
	sort.SliceStable(copied, func(i, j int) bool {
		return copied[i].Key < copied[j].Key
	})

	if len(copied) == 0 {
		t.root = &node[K, V]{leaf: true}
		t.distinctKeys = 0
		t.valueCount = 0
		return nil
	}

	keys := make([]K, 0)
	buckets := make([][]V, 0)
	for _, e := range copied {
		if len(keys) == 0 || keys[len(keys)-1] != e.Key {
			keys = append(keys, e.Key)
			buckets = append(buckets, []V{e.Value})
			continue
		}
		b := buckets[len(buckets)-1]
		if containsValue(b, e.Value) {
			return ErrDuplicateEntry
		}
		buckets[len(buckets)-1] = append(b, e.Value)
	}

	leafSizes := balancedGroupSizes(len(keys), t.maxLeafKeys())
	leaves := make([]*node[K, V], 0, len(leafSizes))
	off := 0
	for _, sz := range leafSizes {
		leaf := &node[K, V]{
			leaf:   true,
			keys:   cloneSlice(keys[off : off+sz]),
			values: make([][]V, sz),
		}
		for i := 0; i < sz; i++ {
			leaf.values[i] = cloneSlice(buckets[off+i])
		}
		if len(leaves) > 0 {
			prev := leaves[len(leaves)-1]
			prev.next = leaf
			leaf.prev = prev
		}
		leaves = append(leaves, leaf)
		off += sz
	}

	level := leaves
	for len(level) > 1 {
		if len(level) <= t.order {
			root := &node[K, V]{leaf: false, children: append([]*node[K, V](nil), level...)}
			for _, c := range root.children {
				c.parent = root
			}
			if err := t.refreshNodeKeysLocked(root); err != nil {
				return err
			}
			level = []*node[K, V]{root}
			break
		}
		sizes := balancedGroupSizes(len(level), t.order)
		next := make([]*node[K, V], 0, len(sizes))
		pos := 0
		for _, sz := range sizes {
			n := &node[K, V]{leaf: false, children: append([]*node[K, V](nil), level[pos:pos+sz]...)}
			for _, c := range n.children {
				c.parent = n
			}
			if err := t.refreshNodeKeysLocked(n); err != nil {
				return err
			}
			next = append(next, n)
			pos += sz
		}
		level = next
	}

	t.root = level[0]
	t.root.parent = nil
	t.distinctKeys = len(keys)
	t.valueCount = len(copied)
	return nil
}

func balancedGroupSizes(total, max int) []int {
	if total <= 0 {
		return nil
	}
	groups := (total + max - 1) / max
	base := total / groups
	extra := total % groups
	sizes := make([]int, groups)
	for i := 0; i < groups; i++ {
		sizes[i] = base
		if i < extra {
			sizes[i]++
		}
	}
	return sizes
}

func (t *Tree[K, V]) Stats() TreeStats {
	t.mu.RLock()
	defer t.mu.RUnlock()

	stats := TreeStats{Order: t.order, DistinctKeys: t.distinctKeys, Values: t.valueCount}
	if t.root == nil {
		return stats
	}
	type qitem struct {
		n     *node[K, V]
		depth int
	}
	q := []qitem{{t.root, 1}}
	for len(q) > 0 {
		it := q[0]
		q = q[1:]
		stats.NodeCount++
		if it.depth > stats.Height {
			stats.Height = it.depth
		}
		if it.n.leaf {
			stats.LeafNodes++
		} else {
			stats.InternalNodes++
			for _, c := range it.n.children {
				q = append(q, qitem{c, it.depth + 1})
			}
		}
	}
	return stats
}

func (t *Tree[K, V]) Validate() error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.validateLocked()
}

func (t *Tree[K, V]) validateLocked() error {
	if t.root == nil {
		return fmt.Errorf("%w: nil root", ErrCorruptTree)
	}
	if t.root.parent != nil {
		return fmt.Errorf("%w: root has parent", ErrCorruptTree)
	}

	leafDepth := -1
	distinct := 0
	values := 0
	visited := make(map[*node[K, V]]bool)
	leavesDFS := make([]*node[K, V], 0)

	var walk func(*node[K, V], int) error
	walk = func(n *node[K, V], depth int) error {
		if n == nil {
			return fmt.Errorf("%w: nil node", ErrCorruptTree)
		}
		if visited[n] {
			return fmt.Errorf("%w: node cycle", ErrCorruptTree)
		}
		visited[n] = true

		for i := 1; i < len(n.keys); i++ {
			if !(n.keys[i-1] < n.keys[i]) {
				return fmt.Errorf("%w: unsorted or duplicate node keys", ErrCorruptTree)
			}
		}

		if n.leaf {
			if len(n.children) != 0 {
				return fmt.Errorf("%w: leaf has children", ErrCorruptTree)
			}
			if len(n.keys) != len(n.values) {
				return fmt.Errorf("%w: leaf key/value length mismatch", ErrCorruptTree)
			}
			if n != t.root {
				if len(n.keys) < t.minLeafKeys() || len(n.keys) > t.maxLeafKeys() {
					return fmt.Errorf("%w: leaf occupancy %d outside [%d,%d]", ErrCorruptTree, len(n.keys), t.minLeafKeys(), t.maxLeafKeys())
				}
			} else if len(n.keys) > t.maxLeafKeys() {
				return fmt.Errorf("%w: root leaf overflow", ErrCorruptTree)
			}
			for _, b := range n.values {
				if len(b) == 0 {
					return fmt.Errorf("%w: empty bucket", ErrCorruptTree)
				}
				seen := make(map[V]struct{}, len(b))
				for _, v := range b {
					if _, ok := seen[v]; ok {
						return fmt.Errorf("%w: duplicate value inside bucket", ErrCorruptTree)
					}
					seen[v] = struct{}{}
					values++
				}
			}
			distinct += len(n.keys)
			if leafDepth < 0 {
				leafDepth = depth
			} else if leafDepth != depth {
				return fmt.Errorf("%w: leaves at different depths", ErrCorruptTree)
			}
			leavesDFS = append(leavesDFS, n)
			return nil
		}

		if len(n.values) != 0 {
			return fmt.Errorf("%w: internal node has leaf values", ErrCorruptTree)
		}
		if len(n.children) != len(n.keys)+1 {
			return fmt.Errorf("%w: internal children/keys mismatch", ErrCorruptTree)
		}
		if n == t.root {
			if len(n.children) < 2 || len(n.children) > t.order {
				return fmt.Errorf("%w: root internal child count %d", ErrCorruptTree, len(n.children))
			}
		} else if len(n.children) < t.minInternalChildren() || len(n.children) > t.order {
			return fmt.Errorf("%w: internal occupancy %d outside [%d,%d]", ErrCorruptTree, len(n.children), t.minInternalChildren(), t.order)
		}

		for i, c := range n.children {
			if c.parent != n {
				return fmt.Errorf("%w: child %d has wrong parent", ErrCorruptTree, i)
			}
			if i > 0 {
				expected, ok := minKey(c)
				if !ok || n.keys[i-1] != expected {
					return fmt.Errorf("%w: separator %d incorrect", ErrCorruptTree, i-1)
				}
			}
			if err := walk(c, depth+1); err != nil {
				return err
			}
		}
		return nil
	}

	if err := walk(t.root, 0); err != nil {
		return err
	}
	if distinct != t.distinctKeys || values != t.valueCount {
		return fmt.Errorf("%w: counters distinct=%d/%d values=%d/%d", ErrCorruptTree, distinct, t.distinctKeys, values, t.valueCount)
	}

	// Verifica que los enlaces next/prev representen exactamente las hojas DFS.
	linked := make([]*node[K, V], 0, len(leavesDFS))
	var prev *node[K, V]
	for leaf := t.leftmostLeafLocked(); leaf != nil; leaf = leaf.next {
		if leaf.prev != prev {
			return fmt.Errorf("%w: broken leaf prev link", ErrCorruptTree)
		}
		linked = append(linked, leaf)
		prev = leaf
		if len(linked) > len(leavesDFS) {
			return fmt.Errorf("%w: cycle in leaf links", ErrCorruptTree)
		}
	}
	if len(linked) != len(leavesDFS) {
		return fmt.Errorf("%w: linked leaf count mismatch", ErrCorruptTree)
	}
	for i := range linked {
		if linked[i] != leavesDFS[i] {
			return fmt.Errorf("%w: leaf link order differs from tree order", ErrCorruptTree)
		}
	}
	return nil
}
