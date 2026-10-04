package bplus

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
)

const (
	DefaultIndexPageSize        = 4096
	metaPageID                  = PageID(0)
	firstNodePageID             = PageID(1)
	invalidPageID               = PageID(^uint32(0))
	indexVersion         uint32 = 1
)

var (
	ErrInvalidOrder   = errors.New("bplus: order must be >= 3")
	ErrNotFound       = errors.New("bplus: entry not found")
	ErrDuplicateEntry = errors.New("bplus: duplicate key/value entry")
	ErrInvalidRange   = errors.New("bplus: invalid range")
	ErrCorruptTree    = errors.New("bplus: invariant violation")
	ErrPageOverflow   = errors.New("bplus: node does not fit in index page")
	ErrClosedTree     = errors.New("bplus: tree is closed")
	ErrOrderMismatch  = errors.New("bplus: persisted order differs from requested order")
)

var indexMagic = [8]byte{'B', 'P', 'L', 'U', 'S', '0', '1', 0}

type Ordered interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr |
		~string
}

type PageID uint32

func (p PageID) valid() bool { return p != invalidPageID }

// Entry representa un par clave/valor del índice. Si una clave tiene varios
// valores, Items devuelve una Entry por valor.
type Entry[K Ordered, V comparable] struct {
	Key   K
	Value V
}

// Bucket: clave junto con todos sus valores asociados.
type Bucket[K Ordered, V comparable] struct {
	Key    K
	Values []V
}

type TreeStats struct {
	Order         int
	Height        int
	NodeCount     int
	InternalNodes int
	LeafNodes     int
	DistinctKeys  int
	Values        int
	PageSize      int
	FilePages     uint32
}

// diskNode es la representación lógica de una página del B+.
type diskNode[K Ordered, V comparable] struct {
	ID       PageID
	Leaf     bool
	Parent   PageID
	Next     PageID
	Prev     PageID
	Keys     []K
	Values   [][]V
	Children []PageID
}

type treeMeta struct {
	Order        uint32
	PageSize     uint32
	Root         PageID
	FirstLeaf    PageID
	NextPageID   PageID
	FreeHead     PageID
	DistinctKeys uint64
	ValueCount   uint64
}

// Tree es un B+ persistente paginado.
//
// La página 0 contiene metadata; las páginas >= 1 contienen nodos. Los nodos
// se cargan desde disco únicamente cuando una operación los necesita. Puede
// existir memoria temporal durante una operación, pero el estado durable del
// índice vive en el archivo indicado por path.
type Tree[K Ordered, V comparable] struct {
	mu sync.RWMutex

	file     *os.File
	path     string
	pageSize int
	meta     treeMeta
	closed   bool
	removeOnClose bool
}

// Create crea un índice B+ persistente nuevo y trunca path si ya existía.
func Create[K Ordered, V comparable](path string, order int) (*Tree[K, V], error) {
	return CreateWithPageSize[K, V](path, order, DefaultIndexPageSize)
}

func CreateWithPageSize[K Ordered, V comparable](path string, order, pageSize int) (*Tree[K, V], error) {
	if order < 3 {
		return nil, ErrInvalidOrder
	}
	if pageSize < 512 {
		return nil, fmt.Errorf("bplus: page size too small: %d", pageSize)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}

	t := &Tree[K, V]{
		file:     f,
		path:     path,
		pageSize: pageSize,
		meta: treeMeta{
			Order:      uint32(order),
			PageSize:   uint32(pageSize),
			Root:       firstNodePageID,
			FirstLeaf:  firstNodePageID,
			NextPageID: firstNodePageID + 1,
			FreeHead:   invalidPageID,
		},
	}

	root := &diskNode[K, V]{
		ID:     firstNodePageID,
		Leaf:   true,
		Parent: invalidPageID,
		Next:   invalidPageID,
		Prev:   invalidPageID,
	}
	if err := t.writeMetaLocked(); err != nil {
		f.Close()
		return nil, err
	}
	if err := t.writeNodeLocked(root); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return nil, err
	}
	return t, nil
}

// Open abre un B+ existente. No escanea HeapFile/SeqFile ni reconstruye el
// árbol: basta con leer la metadata y acceder a root por PageID.
func Open[K Ordered, V comparable](path string) (*Tree[K, V], error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	t := &Tree[K, V]{file: f, path: path}
	if err := t.readMetaLocked(); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := t.readNodeLocked(t.meta.Root); err != nil {
		f.Close()
		return nil, err
	}
	return t, nil
}

// OpenOrCreate abre path si existe. Si no existe, crea un B+ nuevo con order
func OpenOrCreate[K Ordered, V comparable](path string, order int) (*Tree[K, V], error) {
	t, err := Open[K, V](path)
	if err == nil {
		if t.Order() != order {
			t.Close()
			return nil, fmt.Errorf("%w: disk=%d requested=%d", ErrOrderMismatch, t.Order(), order)
		}
		return t, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return Create[K, V](path, order)
}

// file-backed, // pero usa un archivo temporal que se elimina al cerrar
func New[K Ordered, V comparable](order int) (*Tree[K, V], error) {
	if order < 3 {
		return nil, ErrInvalidOrder
	}
	f, err := os.CreateTemp("", "dbms-go-bplus-*.idx")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		os.Remove(path)
		return nil, err
	}
	t, err := Create[K, V](path, order)
	if err != nil {
		os.Remove(path)
		return nil, err
	}
	t.removeOnClose = true
	return t, nil
}

func MustNew[K Ordered, V comparable](order int) *Tree[K, V] {
	t, err := New[K, V](order)
	if err != nil {
		panic(err)
	}
	return t
}

func (t *Tree[K, V]) Path() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.path
}

func (t *Tree[K, V]) Order() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return int(t.meta.Order)
}

func (t *Tree[K, V]) PageSize() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.pageSize
}

func (t *Tree[K, V]) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return int(t.meta.ValueCount)
}

func (t *Tree[K, V]) DistinctLen() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return int(t.meta.DistinctKeys)
}

func (t *Tree[K, V]) Sync() error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if err := t.ensureOpenLocked(); err != nil {
		return err
	}
	return t.file.Sync()
}

func (t *Tree[K, V]) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	if err := t.writeMetaLocked(); err != nil {
		return err
	}
	if err := t.file.Sync(); err != nil {
		return err
	}
	path := t.path
	remove := t.removeOnClose
	if err := t.file.Close(); err != nil {
		return err
	}
	t.closed = true
	if remove {
		return os.Remove(path)
	}
	return nil
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

func (t *Tree[K, V]) maxLeafKeysLocked() int { return int(t.meta.Order) - 1 }
func (t *Tree[K, V]) minLeafKeysLocked() int { return int(t.meta.Order) / 2 }
func (t *Tree[K, V]) minInternalChildrenLocked() int {
	return (int(t.meta.Order) + 1) / 2
}

func (t *Tree[K, V]) ensureOpenLocked() error {
	if t.closed || t.file == nil {
		return ErrClosedTree
	}
	return nil
}

func (t *Tree[K, V]) pageOffset(id PageID) int64 {
	return int64(id) * int64(t.pageSize)
}

func (t *Tree[K, V]) readRawPageLocked(id PageID) ([]byte, error) {
	if err := t.ensureOpenLocked(); err != nil {
		return nil, err
	}
	if !id.valid() || id >= t.meta.NextPageID {
		return nil, fmt.Errorf("%w: invalid page id %d", ErrCorruptTree, id)
	}
	buf := make([]byte, t.pageSize)
	n, err := t.file.ReadAt(buf, t.pageOffset(id))
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if n != t.pageSize {
		return nil, fmt.Errorf("%w: short page %d read: %d/%d", ErrCorruptTree, id, n, t.pageSize)
	}
	return buf, nil
}

func (t *Tree[K, V]) writeRawPageLocked(id PageID, buf []byte) error {
	if err := t.ensureOpenLocked(); err != nil {
		return err
	}
	if len(buf) != t.pageSize {
		return fmt.Errorf("bplus: invalid raw page length %d", len(buf))
	}
	n, err := t.file.WriteAt(buf, t.pageOffset(id))
	if err != nil {
		return err
	}
	if n != len(buf) {
		return io.ErrShortWrite
	}
	return nil
}

func (t *Tree[K, V]) readMetaLocked() error {
	if t.file == nil {
		return ErrClosedTree
	}
	// Primero se leen los campos fijos necesarios para conocer pageSize.
	head := make([]byte, 64)
	n, err := t.file.ReadAt(head, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if n < 56 {
		return fmt.Errorf("%w: index metadata too short", ErrCorruptTree)
	}
	var magic [8]byte
	copy(magic[:], head[0:8])
	if magic != indexMagic {
		return fmt.Errorf("%w: invalid index magic", ErrCorruptTree)
	}
	version := binary.LittleEndian.Uint32(head[8:12])
	if version != indexVersion {
		return fmt.Errorf("%w: unsupported index version %d", ErrCorruptTree, version)
	}
	pageSize := binary.LittleEndian.Uint32(head[12:16])
	if pageSize < 512 {
		return fmt.Errorf("%w: invalid page size %d", ErrCorruptTree, pageSize)
	}
	t.pageSize = int(pageSize)
	t.meta.PageSize = pageSize
	t.meta.Order = binary.LittleEndian.Uint32(head[16:20])
	t.meta.Root = PageID(binary.LittleEndian.Uint32(head[20:24]))
	t.meta.FirstLeaf = PageID(binary.LittleEndian.Uint32(head[24:28]))
	t.meta.NextPageID = PageID(binary.LittleEndian.Uint32(head[28:32]))
	t.meta.FreeHead = PageID(binary.LittleEndian.Uint32(head[32:36]))
	t.meta.DistinctKeys = binary.LittleEndian.Uint64(head[40:48])
	t.meta.ValueCount = binary.LittleEndian.Uint64(head[48:56])

	if t.meta.Order < 3 || !t.meta.Root.valid() || t.meta.Root >= t.meta.NextPageID {
		return fmt.Errorf("%w: invalid metadata", ErrCorruptTree)
	}
	info, err := t.file.Stat()
	if err != nil {
		return err
	}
	if info.Size()%int64(t.pageSize) != 0 {
		return fmt.Errorf("%w: file size %d is not multiple of page size %d", ErrCorruptTree, info.Size(), t.pageSize)
	}
	if info.Size() < int64(t.meta.NextPageID)*int64(t.pageSize) {
		return fmt.Errorf("%w: metadata references pages past EOF", ErrCorruptTree)
	}
	return nil
}

func (t *Tree[K, V]) writeMetaLocked() error {
	if err := t.ensureOpenLocked(); err != nil {
		return err
	}
	buf := make([]byte, t.pageSize)
	copy(buf[0:8], indexMagic[:])
	binary.LittleEndian.PutUint32(buf[8:12], indexVersion)
	binary.LittleEndian.PutUint32(buf[12:16], uint32(t.pageSize))
	binary.LittleEndian.PutUint32(buf[16:20], t.meta.Order)
	binary.LittleEndian.PutUint32(buf[20:24], uint32(t.meta.Root))
	binary.LittleEndian.PutUint32(buf[24:28], uint32(t.meta.FirstLeaf))
	binary.LittleEndian.PutUint32(buf[28:32], uint32(t.meta.NextPageID))
	binary.LittleEndian.PutUint32(buf[32:36], uint32(t.meta.FreeHead))
	binary.LittleEndian.PutUint64(buf[40:48], t.meta.DistinctKeys)
	binary.LittleEndian.PutUint64(buf[48:56], t.meta.ValueCount)
	n, err := t.file.WriteAt(buf, 0)
	if err != nil {
		return err
	}
	if n != len(buf) {
		return io.ErrShortWrite
	}
	return nil
}

func (t *Tree[K, V]) encodeNodeLocked(n *diskNode[K, V]) ([]byte, error) {
	var payload bytes.Buffer
	enc := gob.NewEncoder(&payload)
	if err := enc.Encode(n); err != nil {
		return nil, err
	}
	if payload.Len()+4 > t.pageSize {
		return nil, fmt.Errorf("%w: page=%d encoded=%d capacity=%d; reduce order or duplicate bucket size", ErrPageOverflow, n.ID, payload.Len()+4, t.pageSize)
	}
	page := make([]byte, t.pageSize)
	binary.LittleEndian.PutUint32(page[0:4], uint32(payload.Len()))
	copy(page[4:], payload.Bytes())
	return page, nil
}

func (t *Tree[K, V]) writeNodeLocked(n *diskNode[K, V]) error {
	if n == nil || !n.ID.valid() || n.ID == metaPageID {
		return fmt.Errorf("%w: invalid node page", ErrCorruptTree)
	}
	buf, err := t.encodeNodeLocked(n)
	if err != nil {
		return err
	}
	return t.writeRawPageLocked(n.ID, buf)
}

func (t *Tree[K, V]) readNodeLocked(id PageID) (*diskNode[K, V], error) {
	if id == metaPageID {
		return nil, fmt.Errorf("%w: metadata page used as node", ErrCorruptTree)
	}
	buf, err := t.readRawPageLocked(id)
	if err != nil {
		return nil, err
	}
	length := int(binary.LittleEndian.Uint32(buf[0:4]))
	if length <= 0 || length > len(buf)-4 {
		return nil, fmt.Errorf("%w: invalid node payload length on page %d", ErrCorruptTree, id)
	}
	var n diskNode[K, V]
	dec := gob.NewDecoder(bytes.NewReader(buf[4 : 4+length]))
	if err := dec.Decode(&n); err != nil {
		return nil, fmt.Errorf("%w: decode page %d: %v", ErrCorruptTree, id, err)
	}
	if n.ID != id {
		return nil, fmt.Errorf("%w: node id mismatch page=%d node=%d", ErrCorruptTree, id, n.ID)
	}
	return &n, nil
}

func (t *Tree[K, V]) allocPageLocked() (PageID, error) {
	if t.meta.FreeHead.valid() {
		id := t.meta.FreeHead
		buf, err := t.readRawPageLocked(id)
		if err != nil {
			return 0, err
		}
		t.meta.FreeHead = PageID(binary.LittleEndian.Uint32(buf[0:4]))
		return id, nil
	}
	id := t.meta.NextPageID
	t.meta.NextPageID++
	return id, nil
}

func (t *Tree[K, V]) freePageLocked(id PageID) error {
	if !id.valid() || id == metaPageID || id == t.meta.Root {
		return fmt.Errorf("%w: cannot free page %d", ErrCorruptTree, id)
	}
	buf := make([]byte, t.pageSize)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(t.meta.FreeHead))
	if err := t.writeRawPageLocked(id, buf); err != nil {
		return err
	}
	t.meta.FreeHead = id
	return nil
}

func (t *Tree[K, V]) findLeafIDLocked(key K) (PageID, error) {
	id := t.meta.Root
	for {
		n, err := t.readNodeLocked(id)
		if err != nil {
			return 0, err
		}
		if n.Leaf {
			return id, nil
		}
		idx := childIndex(n.Keys, key)
		if idx < 0 || idx >= len(n.Children) {
			return 0, fmt.Errorf("%w: child index %d out of range on page %d", ErrCorruptTree, idx, id)
		}
		id = n.Children[idx]
	}
}

func (t *Tree[K, V]) minKeyLocked(id PageID) (K, bool, error) {
	for {
		n, err := t.readNodeLocked(id)
		if err != nil {
			var zero K
			return zero, false, err
		}
		if n.Leaf {
			if len(n.Keys) == 0 {
				var zero K
				return zero, false, nil
			}
			return n.Keys[0], true, nil
		}
		if len(n.Children) == 0 {
			var zero K
			return zero, false, nil
		}
		id = n.Children[0]
	}
}

func (t *Tree[K, V]) refreshNodeKeysLocked(n *diskNode[K, V]) error {
	if n == nil || n.Leaf {
		return nil
	}
	if len(n.Children) == 0 {
		n.Keys = nil
		return nil
	}
	keys := make([]K, len(n.Children)-1)
	for i := 1; i < len(n.Children); i++ {
		k, ok, err := t.minKeyLocked(n.Children[i])
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: internal child %d on page %d has no minimum key", ErrCorruptTree, i, n.ID)
		}
		keys[i-1] = k
	}
	n.Keys = keys
	return nil
}

func (t *Tree[K, V]) refreshAncestorsLocked(id PageID) error {
	for id.valid() {
		n, err := t.readNodeLocked(id)
		if err != nil {
			return err
		}
		if !n.Leaf {
			if err := t.refreshNodeKeysLocked(n); err != nil {
				return err
			}
			if err := t.writeNodeLocked(n); err != nil {
				return err
			}
		}
		id = n.Parent
	}
	return nil
}


func (t *Tree[K, V]) SearchE(key K) ([]V, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if err := t.ensureOpenLocked(); err != nil {
		return nil, err
	}
	leafID, err := t.findLeafIDLocked(key)
	if err != nil {
		return nil, err
	}
	leaf, err := t.readNodeLocked(leafID)
	if err != nil {
		return nil, err
	}
	i := lowerBound(leaf.Keys, key)
	if i >= len(leaf.Keys) || leaf.Keys[i] != key {
		return nil, nil
	}
	return cloneSlice(leaf.Values[i]), nil
}

func (t *Tree[K, V]) Search(key K) []V {
	out, _ := t.SearchE(key)
	return out
}

func (t *Tree[K, V]) ContainsE(key K, value V) (bool, error) {
	vals, err := t.SearchE(key)
	if err != nil {
		return false, err
	}
	return containsValue(vals, value), nil
}

func (t *Tree[K, V]) Contains(key K, value V) bool {
	ok, _ := t.ContainsE(key, value)
	return ok
}

func (t *Tree[K, V]) Insert(key K, value V) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureOpenLocked(); err != nil {
		return err
	}

	leafID, err := t.findLeafIDLocked(key)
	if err != nil {
		return err
	}
	leaf, err := t.readNodeLocked(leafID)
	if err != nil {
		return err
	}
	i := lowerBound(leaf.Keys, key)
	if i < len(leaf.Keys) && leaf.Keys[i] == key {
		if containsValue(leaf.Values[i], value) {
			return ErrDuplicateEntry
		}
		leaf.Values[i] = append(leaf.Values[i], value)
		if err := t.writeNodeLocked(leaf); err != nil {
			return err
		}
		t.meta.ValueCount++
		if err := t.writeMetaLocked(); err != nil {
			return err
		}
		return t.file.Sync()
	}

	leaf.Keys = insertAt(leaf.Keys, i, key)
	leaf.Values = insertAt(leaf.Values, i, []V{value})
	t.meta.DistinctKeys++
	t.meta.ValueCount++

	if len(leaf.Keys) <= t.maxLeafKeysLocked() {
		if err := t.writeNodeLocked(leaf); err != nil {
			return err
		}
		if err := t.refreshAncestorsLocked(leaf.Parent); err != nil {
			return err
		}
	} else if err := t.splitLeafLocked(leaf); err != nil {
		return err
	}
	if err := t.writeMetaLocked(); err != nil {
		return err
	}
	return t.file.Sync()
}

func (t *Tree[K, V]) splitLeafLocked(leaf *diskNode[K, V]) error {
	split := (len(leaf.Keys) + 1) / 2
	rightID, err := t.allocPageLocked()
	if err != nil {
		return err
	}
	right := &diskNode[K, V]{
		ID:     rightID,
		Leaf:   true,
		Parent: leaf.Parent,
		Next:   leaf.Next,
		Prev:   leaf.ID,
		Keys:   cloneSlice(leaf.Keys[split:]),
		Values: append([][]V(nil), leaf.Values[split:]...),
	}
	for i := range right.Values {
		right.Values[i] = cloneSlice(right.Values[i])
	}
	leaf.Keys = leaf.Keys[:split]
	leaf.Values = leaf.Values[:split]
	oldNext := leaf.Next
	leaf.Next = right.ID

	if oldNext.valid() {
		next, err := t.readNodeLocked(oldNext)
		if err != nil {
			return err
		}
		next.Prev = right.ID
		if err := t.writeNodeLocked(next); err != nil {
			return err
		}
	}
	if err := t.writeNodeLocked(leaf); err != nil {
		return err
	}
	if err := t.writeNodeLocked(right); err != nil {
		return err
	}
	return t.insertSiblingLocked(leaf.ID, right.ID, leaf.Parent)
}

func childPosition(children []PageID, child PageID) int {
	for i, id := range children {
		if id == child {
			return i
		}
	}
	return -1
}

func (t *Tree[K, V]) insertSiblingLocked(leftID, rightID, parentID PageID) error {
	left, err := t.readNodeLocked(leftID)
	if err != nil {
		return err
	}
	right, err := t.readNodeLocked(rightID)
	if err != nil {
		return err
	}

	if !parentID.valid() {
		rootID, err := t.allocPageLocked()
		if err != nil {
			return err
		}
		root := &diskNode[K, V]{
			ID:       rootID,
			Leaf:     false,
			Parent:   invalidPageID,
			Next:     invalidPageID,
			Prev:     invalidPageID,
			Children: []PageID{leftID, rightID},
		}
		left.Parent = rootID
		right.Parent = rootID
		if err := t.writeNodeLocked(left); err != nil {
			return err
		}
		if err := t.writeNodeLocked(right); err != nil {
			return err
		}
		if err := t.refreshNodeKeysLocked(root); err != nil {
			return err
		}
		if err := t.writeNodeLocked(root); err != nil {
			return err
		}
		t.meta.Root = rootID
		return nil
	}

	parent, err := t.readNodeLocked(parentID)
	if err != nil {
		return err
	}
	pos := childPosition(parent.Children, leftID)
	if pos < 0 {
		return fmt.Errorf("%w: split child %d absent from parent %d", ErrCorruptTree, leftID, parentID)
	}
	parent.Children = insertAt(parent.Children, pos+1, rightID)
	right.Parent = parent.ID
	if err := t.writeNodeLocked(right); err != nil {
		return err
	}
	if err := t.refreshNodeKeysLocked(parent); err != nil {
		return err
	}
	if err := t.writeNodeLocked(parent); err != nil {
		return err
	}

	if len(parent.Children) > int(t.meta.Order) {
		return t.splitInternalLocked(parent)
	}
	return t.refreshAncestorsLocked(parent.Parent)
}

func (t *Tree[K, V]) splitInternalLocked(n *diskNode[K, V]) error {
	split := (len(n.Children) + 1) / 2
	rightID, err := t.allocPageLocked()
	if err != nil {
		return err
	}
	rightChildren := cloneSlice(n.Children[split:])
	right := &diskNode[K, V]{
		ID:       rightID,
		Leaf:     false,
		Parent:   n.Parent,
		Next:     invalidPageID,
		Prev:     invalidPageID,
		Children: rightChildren,
	}
	n.Children = n.Children[:split]

	for _, childID := range right.Children {
		child, err := t.readNodeLocked(childID)
		if err != nil {
			return err
		}
		child.Parent = right.ID
		if err := t.writeNodeLocked(child); err != nil {
			return err
		}
	}
	if err := t.refreshNodeKeysLocked(n); err != nil {
		return err
	}
	if err := t.refreshNodeKeysLocked(right); err != nil {
		return err
	}
	if err := t.writeNodeLocked(n); err != nil {
		return err
	}
	if err := t.writeNodeLocked(right); err != nil {
		return err
	}
	return t.insertSiblingLocked(n.ID, right.ID, n.Parent)
}

func (t *Tree[K, V]) Delete(key K, value V) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureOpenLocked(); err != nil {
		return err
	}
	leafID, err := t.findLeafIDLocked(key)
	if err != nil {
		return err
	}
	leaf, err := t.readNodeLocked(leafID)
	if err != nil {
		return err
	}
	i := lowerBound(leaf.Keys, key)
	if i >= len(leaf.Keys) || leaf.Keys[i] != key {
		return ErrNotFound
	}
	bucket := leaf.Values[i]
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
	t.meta.ValueCount--
	if len(bucket) > 0 {
		leaf.Values[i] = bucket
		if err := t.writeNodeLocked(leaf); err != nil {
			return err
		}
		if err := t.writeMetaLocked(); err != nil {
			return err
		}
		return t.file.Sync()
	}

	leaf.Keys = removeAt(leaf.Keys, i)
	leaf.Values = removeAt(leaf.Values, i)
	t.meta.DistinctKeys--
	if err := t.writeNodeLocked(leaf); err != nil {
		return err
	}
	if err := t.afterLeafKeyRemovalLocked(leaf); err != nil {
		return err
	}
	if err := t.writeMetaLocked(); err != nil {
		return err
	}
	return t.file.Sync()
}

func (t *Tree[K, V]) DeleteKey(key K) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureOpenLocked(); err != nil {
		return 0, err
	}
	leafID, err := t.findLeafIDLocked(key)
	if err != nil {
		return 0, err
	}
	leaf, err := t.readNodeLocked(leafID)
	if err != nil {
		return 0, err
	}
	i := lowerBound(leaf.Keys, key)
	if i >= len(leaf.Keys) || leaf.Keys[i] != key {
		return 0, ErrNotFound
	}
	removed := len(leaf.Values[i])
	leaf.Keys = removeAt(leaf.Keys, i)
	leaf.Values = removeAt(leaf.Values, i)
	t.meta.DistinctKeys--
	t.meta.ValueCount -= uint64(removed)
	if err := t.writeNodeLocked(leaf); err != nil {
		return 0, err
	}
	if err := t.afterLeafKeyRemovalLocked(leaf); err != nil {
		return 0, err
	}
	if err := t.writeMetaLocked(); err != nil {
		return 0, err
	}
	if err := t.file.Sync(); err != nil {
		return 0, err
	}
	return removed, nil
}

func (t *Tree[K, V]) afterLeafKeyRemovalLocked(leaf *diskNode[K, V]) error {
	if leaf.ID == t.meta.Root {
		return nil
	}
	if len(leaf.Keys) >= t.minLeafKeysLocked() {
		return t.refreshAncestorsLocked(leaf.Parent)
	}
	return t.rebalanceLeafLocked(leaf)
}

func (t *Tree[K, V]) rebalanceLeafLocked(leaf *diskNode[K, V]) error {
	parent, err := t.readNodeLocked(leaf.Parent)
	if err != nil {
		return err
	}
	pos := childPosition(parent.Children, leaf.ID)
	if pos < 0 {
		return fmt.Errorf("%w: leaf %d absent from parent %d", ErrCorruptTree, leaf.ID, parent.ID)
	}

	var left, right *diskNode[K, V]
	if pos > 0 {
		left, err = t.readNodeLocked(parent.Children[pos-1])
		if err != nil {
			return err
		}
	}
	if pos+1 < len(parent.Children) {
		right, err = t.readNodeLocked(parent.Children[pos+1])
		if err != nil {
			return err
		}
	}

	if left != nil && len(left.Keys) > t.minLeafKeysLocked() {
		k := left.Keys[len(left.Keys)-1]
		vals := left.Values[len(left.Values)-1]
		left.Keys = left.Keys[:len(left.Keys)-1]
		left.Values = left.Values[:len(left.Values)-1]
		leaf.Keys = insertAt(leaf.Keys, 0, k)
		leaf.Values = insertAt(leaf.Values, 0, vals)
		if err := t.writeNodeLocked(left); err != nil {
			return err
		}
		if err := t.writeNodeLocked(leaf); err != nil {
			return err
		}
		if err := t.refreshNodeKeysLocked(parent); err != nil {
			return err
		}
		if err := t.writeNodeLocked(parent); err != nil {
			return err
		}
		return t.refreshAncestorsLocked(parent.Parent)
	}

	if right != nil && len(right.Keys) > t.minLeafKeysLocked() {
		k := right.Keys[0]
		vals := right.Values[0]
		right.Keys = removeAt(right.Keys, 0)
		right.Values = removeAt(right.Values, 0)
		leaf.Keys = append(leaf.Keys, k)
		leaf.Values = append(leaf.Values, vals)
		if err := t.writeNodeLocked(right); err != nil {
			return err
		}
		if err := t.writeNodeLocked(leaf); err != nil {
			return err
		}
		if err := t.refreshNodeKeysLocked(parent); err != nil {
			return err
		}
		if err := t.writeNodeLocked(parent); err != nil {
			return err
		}
		return t.refreshAncestorsLocked(parent.Parent)
	}

	if left != nil {
		left.Keys = append(left.Keys, leaf.Keys...)
		left.Values = append(left.Values, leaf.Values...)
		left.Next = leaf.Next
		if leaf.Next.valid() {
			next, err := t.readNodeLocked(leaf.Next)
			if err != nil {
				return err
			}
			next.Prev = left.ID
			if err := t.writeNodeLocked(next); err != nil {
				return err
			}
		}
		parent.Children = removeAt(parent.Children, pos)
		if err := t.writeNodeLocked(left); err != nil {
			return err
		}
		if err := t.refreshNodeKeysLocked(parent); err != nil && len(parent.Children) > 0 {
			return err
		}
		if err := t.writeNodeLocked(parent); err != nil {
			return err
		}
		if err := t.freePageLocked(leaf.ID); err != nil {
			return err
		}
		return t.rebalanceInternalLocked(parent)
	}

	if right != nil {
		leaf.Keys = append(leaf.Keys, right.Keys...)
		leaf.Values = append(leaf.Values, right.Values...)
		leaf.Next = right.Next
		if right.Next.valid() {
			next, err := t.readNodeLocked(right.Next)
			if err != nil {
				return err
			}
			next.Prev = leaf.ID
			if err := t.writeNodeLocked(next); err != nil {
				return err
			}
		}
		parent.Children = removeAt(parent.Children, pos+1)
		if err := t.writeNodeLocked(leaf); err != nil {
			return err
		}
		if err := t.refreshNodeKeysLocked(parent); err != nil && len(parent.Children) > 0 {
			return err
		}
		if err := t.writeNodeLocked(parent); err != nil {
			return err
		}
		if err := t.freePageLocked(right.ID); err != nil {
			return err
		}
		return t.rebalanceInternalLocked(parent)
	}

	return fmt.Errorf("%w: underfull leaf %d has no sibling", ErrCorruptTree, leaf.ID)
}

func (t *Tree[K, V]) rebalanceInternalLocked(n *diskNode[K, V]) error {
	if n.ID == t.meta.Root {
		switch len(n.Children) {
		case 0:
			// reutilizamos la página raíz como hoja vacía.
			n.Leaf = true
			n.Keys = nil
			n.Values = nil
			n.Children = nil
			n.Parent = invalidPageID
			n.Next = invalidPageID
			n.Prev = invalidPageID
			t.meta.FirstLeaf = n.ID
			return t.writeNodeLocked(n)
		case 1:
			child, err := t.readNodeLocked(n.Children[0])
			if err != nil {
				return err
			}
			oldRoot := n.ID
			child.Parent = invalidPageID
			if err := t.writeNodeLocked(child); err != nil {
				return err
			}
			t.meta.Root = child.ID
			if child.Leaf {
				t.meta.FirstLeaf = child.ID
			}
			return t.freePageLocked(oldRoot)
		default:
			if err := t.refreshNodeKeysLocked(n); err != nil {
				return err
			}
			return t.writeNodeLocked(n)
		}
	}

	if len(n.Children) >= t.minInternalChildrenLocked() {
		if err := t.refreshNodeKeysLocked(n); err != nil {
			return err
		}
		if err := t.writeNodeLocked(n); err != nil {
			return err
		}
		return t.refreshAncestorsLocked(n.Parent)
	}

	parent, err := t.readNodeLocked(n.Parent)
	if err != nil {
		return err
	}
	pos := childPosition(parent.Children, n.ID)
	if pos < 0 {
		return fmt.Errorf("%w: internal node %d absent from parent %d", ErrCorruptTree, n.ID, parent.ID)
	}

	var left, right *diskNode[K, V]
	if pos > 0 {
		left, err = t.readNodeLocked(parent.Children[pos-1])
		if err != nil {
			return err
		}
	}
	if pos+1 < len(parent.Children) {
		right, err = t.readNodeLocked(parent.Children[pos+1])
		if err != nil {
			return err
		}
	}

	if left != nil && len(left.Children) > t.minInternalChildrenLocked() {
		childID := left.Children[len(left.Children)-1]
		left.Children = left.Children[:len(left.Children)-1]
		n.Children = insertAt(n.Children, 0, childID)
		child, err := t.readNodeLocked(childID)
		if err != nil {
			return err
		}
		child.Parent = n.ID
		if err := t.writeNodeLocked(child); err != nil {
			return err
		}
		if err := t.refreshNodeKeysLocked(left); err != nil {
			return err
		}
		if err := t.refreshNodeKeysLocked(n); err != nil {
			return err
		}
		if err := t.writeNodeLocked(left); err != nil {
			return err
		}
		if err := t.writeNodeLocked(n); err != nil {
			return err
		}
		if err := t.refreshNodeKeysLocked(parent); err != nil {
			return err
		}
		if err := t.writeNodeLocked(parent); err != nil {
			return err
		}
		return t.refreshAncestorsLocked(parent.Parent)
	}

	if right != nil && len(right.Children) > t.minInternalChildrenLocked() {
		childID := right.Children[0]
		right.Children = removeAt(right.Children, 0)
		n.Children = append(n.Children, childID)
		child, err := t.readNodeLocked(childID)
		if err != nil {
			return err
		}
		child.Parent = n.ID
		if err := t.writeNodeLocked(child); err != nil {
			return err
		}
		if err := t.refreshNodeKeysLocked(right); err != nil {
			return err
		}
		if err := t.refreshNodeKeysLocked(n); err != nil {
			return err
		}
		if err := t.writeNodeLocked(right); err != nil {
			return err
		}
		if err := t.writeNodeLocked(n); err != nil {
			return err
		}
		if err := t.refreshNodeKeysLocked(parent); err != nil {
			return err
		}
		if err := t.writeNodeLocked(parent); err != nil {
			return err
		}
		return t.refreshAncestorsLocked(parent.Parent)
	}

	if left != nil {
		left.Children = append(left.Children, n.Children...)
		for _, childID := range n.Children {
			child, err := t.readNodeLocked(childID)
			if err != nil {
				return err
			}
			child.Parent = left.ID
			if err := t.writeNodeLocked(child); err != nil {
				return err
			}
		}
		parent.Children = removeAt(parent.Children, pos)
		if err := t.refreshNodeKeysLocked(left); err != nil {
			return err
		}
		if err := t.writeNodeLocked(left); err != nil {
			return err
		}
		if err := t.refreshNodeKeysLocked(parent); err != nil && len(parent.Children) > 0 {
			return err
		}
		if err := t.writeNodeLocked(parent); err != nil {
			return err
		}
		if err := t.freePageLocked(n.ID); err != nil {
			return err
		}
		return t.rebalanceInternalLocked(parent)
	}

	if right != nil {
		n.Children = append(n.Children, right.Children...)
		for _, childID := range right.Children {
			child, err := t.readNodeLocked(childID)
			if err != nil {
				return err
			}
			child.Parent = n.ID
			if err := t.writeNodeLocked(child); err != nil {
				return err
			}
		}
		parent.Children = removeAt(parent.Children, pos+1)
		if err := t.refreshNodeKeysLocked(n); err != nil {
			return err
		}
		if err := t.writeNodeLocked(n); err != nil {
			return err
		}
		if err := t.refreshNodeKeysLocked(parent); err != nil && len(parent.Children) > 0 {
			return err
		}
		if err := t.writeNodeLocked(parent); err != nil {
			return err
		}
		if err := t.freePageLocked(right.ID); err != nil {
			return err
		}
		return t.rebalanceInternalLocked(parent)
	}

	return fmt.Errorf("%w: underfull internal node %d has no sibling", ErrCorruptTree, n.ID)
}

func (t *Tree[K, V]) RangeSearch(low, high K) ([]Entry[K, V], error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if err := t.ensureOpenLocked(); err != nil {
		return nil, err
	}
	if high < low {
		return nil, ErrInvalidRange
	}
	leafID, err := t.findLeafIDLocked(low)
	if err != nil {
		return nil, err
	}
	out := make([]Entry[K, V], 0)
	first := true
	for leafID.valid() {
		leaf, err := t.readNodeLocked(leafID)
		if err != nil {
			return nil, err
		}
		if !leaf.Leaf {
			return nil, fmt.Errorf("%w: leaf chain contains internal page %d", ErrCorruptTree, leafID)
		}
		start := 0
		if first {
			start = lowerBound(leaf.Keys, low)
			first = false
		}
		for i := start; i < len(leaf.Keys); i++ {
			k := leaf.Keys[i]
			if high < k {
				return out, nil
			}
			if k < low {
				continue
			}
			for _, v := range leaf.Values[i] {
				out = append(out, Entry[K, V]{Key: k, Value: v})
			}
		}
		leafID = leaf.Next
	}
	return out, nil
}

func (t *Tree[K, V]) ItemsE() ([]Entry[K, V], error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if err := t.ensureOpenLocked(); err != nil {
		return nil, err
	}
	out := make([]Entry[K, V], 0, int(t.meta.ValueCount))
	for leafID := t.meta.FirstLeaf; leafID.valid(); {
		leaf, err := t.readNodeLocked(leafID)
		if err != nil {
			return nil, err
		}
		if !leaf.Leaf {
			return nil, fmt.Errorf("%w: first-leaf chain points to internal page %d", ErrCorruptTree, leafID)
		}
		for i, k := range leaf.Keys {
			for _, v := range leaf.Values[i] {
				out = append(out, Entry[K, V]{Key: k, Value: v})
			}
		}
		leafID = leaf.Next
	}
	return out, nil
}

func (t *Tree[K, V]) Items() []Entry[K, V] {
	out, _ := t.ItemsE()
	return out
}

func (t *Tree[K, V]) BucketsE() ([]Bucket[K, V], error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if err := t.ensureOpenLocked(); err != nil {
		return nil, err
	}
	out := make([]Bucket[K, V], 0, int(t.meta.DistinctKeys))
	for leafID := t.meta.FirstLeaf; leafID.valid(); {
		leaf, err := t.readNodeLocked(leafID)
		if err != nil {
			return nil, err
		}
		for i, k := range leaf.Keys {
			out = append(out, Bucket[K, V]{Key: k, Values: cloneSlice(leaf.Values[i])})
		}
		leafID = leaf.Next
	}
	return out, nil
}

func (t *Tree[K, V]) Buckets() []Bucket[K, V] {
	out, _ := t.BucketsE()
	return out
}

// BulkLoad reemplaza el archivo del índice con un árbol construido de abajo
// hacia arriba. Se usa para CREATE INDEX / REINDEX, no al abrir normalmente.
func (t *Tree[K, V]) BulkLoad(entries []Entry[K, V]) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureOpenLocked(); err != nil {
		return err
	}

	copied := cloneSlice(entries)
	sort.SliceStable(copied, func(i, j int) bool { return copied[i].Key < copied[j].Key })

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

	if err := t.file.Truncate(0); err != nil {
		return err
	}
	t.meta.Root = invalidPageID
	t.meta.FirstLeaf = invalidPageID
	t.meta.NextPageID = firstNodePageID
	t.meta.FreeHead = invalidPageID
	t.meta.DistinctKeys = uint64(len(keys))
	t.meta.ValueCount = uint64(len(copied))
	if err := t.writeMetaLocked(); err != nil {
		return err
	}

	if len(keys) == 0 {
		rootID, err := t.allocPageLocked()
		if err != nil {
			return err
		}
		root := &diskNode[K, V]{ID: rootID, Leaf: true, Parent: invalidPageID, Next: invalidPageID, Prev: invalidPageID}
		if err := t.writeNodeLocked(root); err != nil {
			return err
		}
		t.meta.Root = rootID
		t.meta.FirstLeaf = rootID
		if err := t.writeMetaLocked(); err != nil {
			return err
		}
		return t.file.Sync()
	}

	leafSizes := balancedGroupSizes(len(keys), t.maxLeafKeysLocked())
	leafIDs := make([]PageID, 0, len(leafSizes))
	leaves := make([]*diskNode[K, V], 0, len(leafSizes))
	off := 0
	for _, sz := range leafSizes {
		id, err := t.allocPageLocked()
		if err != nil {
			return err
		}
		leaf := &diskNode[K, V]{
			ID:     id,
			Leaf:   true,
			Parent: invalidPageID,
			Next:   invalidPageID,
			Prev:   invalidPageID,
			Keys:   cloneSlice(keys[off : off+sz]),
			Values: make([][]V, sz),
		}
		for i := 0; i < sz; i++ {
			leaf.Values[i] = cloneSlice(buckets[off+i])
		}
		leafIDs = append(leafIDs, id)
		leaves = append(leaves, leaf)
		off += sz
	}
	for i, leaf := range leaves {
		if i > 0 {
			leaf.Prev = leaves[i-1].ID
		}
		if i+1 < len(leaves) {
			leaf.Next = leaves[i+1].ID
		}
		if err := t.writeNodeLocked(leaf); err != nil {
			return err
		}
	}
	t.meta.FirstLeaf = leafIDs[0]

	level := leafIDs
	for len(level) > 1 {
		if len(level) <= int(t.meta.Order) {
			rootID, err := t.allocPageLocked()
			if err != nil {
				return err
			}
			root := &diskNode[K, V]{ID: rootID, Leaf: false, Parent: invalidPageID, Next: invalidPageID, Prev: invalidPageID, Children: cloneSlice(level)}
			for _, childID := range root.Children {
				child, err := t.readNodeLocked(childID)
				if err != nil {
					return err
				}
				child.Parent = rootID
				if err := t.writeNodeLocked(child); err != nil {
					return err
				}
			}
			if err := t.refreshNodeKeysLocked(root); err != nil {
				return err
			}
			if err := t.writeNodeLocked(root); err != nil {
				return err
			}
			level = []PageID{rootID}
			break
		}

		sizes := balancedGroupSizes(len(level), int(t.meta.Order))
		nextLevel := make([]PageID, 0, len(sizes))
		pos := 0
		for _, sz := range sizes {
			id, err := t.allocPageLocked()
			if err != nil {
				return err
			}
			children := cloneSlice(level[pos : pos+sz])
			n := &diskNode[K, V]{ID: id, Leaf: false, Parent: invalidPageID, Next: invalidPageID, Prev: invalidPageID, Children: children}
			for _, childID := range children {
				child, err := t.readNodeLocked(childID)
				if err != nil {
					return err
				}
				child.Parent = id
				if err := t.writeNodeLocked(child); err != nil {
					return err
				}
			}
			if err := t.refreshNodeKeysLocked(n); err != nil {
				return err
			}
			if err := t.writeNodeLocked(n); err != nil {
				return err
			}
			nextLevel = append(nextLevel, id)
			pos += sz
		}
		level = nextLevel
	}

	t.meta.Root = level[0]
	root, err := t.readNodeLocked(t.meta.Root)
	if err != nil {
		return err
	}
	root.Parent = invalidPageID
	if err := t.writeNodeLocked(root); err != nil {
		return err
	}
	if err := t.writeMetaLocked(); err != nil {
		return err
	}
	return t.file.Sync()
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

func (t *Tree[K, V]) StatsE() (TreeStats, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if err := t.ensureOpenLocked(); err != nil {
		return TreeStats{}, err
	}
	stats := TreeStats{
		Order:        int(t.meta.Order),
		DistinctKeys: int(t.meta.DistinctKeys),
		Values:       int(t.meta.ValueCount),
		PageSize:     t.pageSize,
		FilePages:    uint32(t.meta.NextPageID),
	}
	type qitem struct {
		id    PageID
		depth int
	}
	q := []qitem{{t.meta.Root, 1}}
	visited := make(map[PageID]bool)
	for len(q) > 0 {
		it := q[0]
		q = q[1:]
		if visited[it.id] {
			return stats, fmt.Errorf("%w: cycle while computing stats", ErrCorruptTree)
		}
		visited[it.id] = true
		n, err := t.readNodeLocked(it.id)
		if err != nil {
			return stats, err
		}
		stats.NodeCount++
		if it.depth > stats.Height {
			stats.Height = it.depth
		}
		if n.Leaf {
			stats.LeafNodes++
		} else {
			stats.InternalNodes++
			for _, childID := range n.Children {
				q = append(q, qitem{id: childID, depth: it.depth + 1})
			}
		}
	}
	return stats, nil
}

func (t *Tree[K, V]) Stats() TreeStats {
	st, _ := t.StatsE()
	return st
}

func (t *Tree[K, V]) Validate() error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if err := t.ensureOpenLocked(); err != nil {
		return err
	}
	return t.validateLocked()
}

func (t *Tree[K, V]) validateLocked() error {
	if !t.meta.Root.valid() || !t.meta.FirstLeaf.valid() {
		return fmt.Errorf("%w: invalid root/first leaf", ErrCorruptTree)
	}

	leafDepth := -1
	distinct := 0
	values := 0
	visited := make(map[PageID]bool)
	leavesDFS := make([]PageID, 0)

	var walk func(PageID, PageID, int) error
	walk = func(id, expectedParent PageID, depth int) error {
		if visited[id] {
			return fmt.Errorf("%w: node cycle at page %d", ErrCorruptTree, id)
		}
		visited[id] = true
		n, err := t.readNodeLocked(id)
		if err != nil {
			return err
		}
		if n.Parent != expectedParent {
			return fmt.Errorf("%w: page %d parent=%d expected=%d", ErrCorruptTree, id, n.Parent, expectedParent)
		}
		for i := 1; i < len(n.Keys); i++ {
			if !(n.Keys[i-1] < n.Keys[i]) {
				return fmt.Errorf("%w: unsorted or duplicate node keys on page %d", ErrCorruptTree, id)
			}
		}

		if n.Leaf {
			if len(n.Children) != 0 {
				return fmt.Errorf("%w: leaf %d has children", ErrCorruptTree, id)
			}
			if len(n.Keys) != len(n.Values) {
				return fmt.Errorf("%w: leaf %d key/value length mismatch", ErrCorruptTree, id)
			}
			if id != t.meta.Root {
				if len(n.Keys) < t.minLeafKeysLocked() || len(n.Keys) > t.maxLeafKeysLocked() {
					return fmt.Errorf("%w: leaf %d occupancy %d outside [%d,%d]", ErrCorruptTree, id, len(n.Keys), t.minLeafKeysLocked(), t.maxLeafKeysLocked())
				}
			} else if len(n.Keys) > t.maxLeafKeysLocked() {
				return fmt.Errorf("%w: root leaf overflow", ErrCorruptTree)
			}
			for _, bucket := range n.Values {
				if len(bucket) == 0 {
					return fmt.Errorf("%w: empty bucket on page %d", ErrCorruptTree, id)
				}
				seen := make(map[V]struct{}, len(bucket))
				for _, v := range bucket {
					if _, ok := seen[v]; ok {
						return fmt.Errorf("%w: duplicate value inside bucket on page %d", ErrCorruptTree, id)
					}
					seen[v] = struct{}{}
					values++
				}
			}
			distinct += len(n.Keys)
			if leafDepth < 0 {
				leafDepth = depth
			} else if leafDepth != depth {
				return fmt.Errorf("%w: leaves at different depths", ErrCorruptTree)
			}
			leavesDFS = append(leavesDFS, id)
			return nil
		}

		if len(n.Values) != 0 {
			return fmt.Errorf("%w: internal page %d has leaf values", ErrCorruptTree, id)
		}
		if len(n.Children) != len(n.Keys)+1 {
			return fmt.Errorf("%w: internal page %d children/keys mismatch", ErrCorruptTree, id)
		}
		if id == t.meta.Root {
			if len(n.Children) < 2 || len(n.Children) > int(t.meta.Order) {
				return fmt.Errorf("%w: root internal child count %d", ErrCorruptTree, len(n.Children))
			}
		} else if len(n.Children) < t.minInternalChildrenLocked() || len(n.Children) > int(t.meta.Order) {
			return fmt.Errorf("%w: internal page %d occupancy %d outside [%d,%d]", ErrCorruptTree, id, len(n.Children), t.minInternalChildrenLocked(), t.meta.Order)
		}

		for i, childID := range n.Children {
			if i > 0 {
				expected, ok, err := t.minKeyLocked(childID)
				if err != nil {
					return err
				}
				if !ok || n.Keys[i-1] != expected {
					return fmt.Errorf("%w: separator %d incorrect on page %d", ErrCorruptTree, i-1, id)
				}
			}
			if err := walk(childID, id, depth+1); err != nil {
				return err
			}
		}
		return nil
	}

	if err := walk(t.meta.Root, invalidPageID, 0); err != nil {
		return err
	}
	if distinct != int(t.meta.DistinctKeys) || values != int(t.meta.ValueCount) {
		return fmt.Errorf("%w: counters distinct=%d/%d values=%d/%d", ErrCorruptTree, distinct, t.meta.DistinctKeys, values, t.meta.ValueCount)
	}

	linked := make([]PageID, 0, len(leavesDFS))
	prev := invalidPageID
	for id := t.meta.FirstLeaf; id.valid(); {
		leaf, err := t.readNodeLocked(id)
		if err != nil {
			return err
		}
		if !leaf.Leaf {
			return fmt.Errorf("%w: leaf chain contains internal page %d", ErrCorruptTree, id)
		}
		if leaf.Prev != prev {
			return fmt.Errorf("%w: broken leaf prev link at page %d", ErrCorruptTree, id)
		}
		linked = append(linked, id)
		prev = id
		id = leaf.Next
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
