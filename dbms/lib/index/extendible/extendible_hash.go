// Package extendible implementa un índice de Hashing Extensible (Dinámico)
// persistente en disco.
//
// El directorio se mantiene temporalmente en RAM como caché, pero su estado
// durable se guarda en el archivo .idx. Cada bucket se almacena en una cadena
// de páginas identificadas por PageID; por tanto, cerrar y volver a abrir el
// proceso NO requiere reconstruir el índice desde el HeapFile.
package extendible

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"sync"

	"github.com/dbms-go/v2/dbms/lib/index/common"
	"github.com/dbms-go/v2/dbms/lib/storage"
	"github.com/dbms-go/v2/dbms/lib/storage/heap"
)

const (
	// MaxGlobalDepth conserva el límite de la implementación original.
	MaxGlobalDepth = 24

	DefaultPageSize = 4096
	indexVersion    = uint32(1)

	metaPageID      = PageID(0)
	firstDataPageID = PageID(1)
	invalidPageID   = PageID(^uint32(0))

	pageHeaderSize = 12
	pageKindFree   = byte(0)
	pageKindDir    = byte(1)
	pageKindBucket = byte(2)
)

// Options conserva la configuración del índice en memoria y permite ajustar
// el tamaño de página al crear un índice temporal con NewWithOptions.
type Options struct {
	BucketSize int
	Unique     bool
	MaxDepth   int
	PageSize   int
}

var (
	ErrCorruptIndex       = errors.New("extendible: corrupt index")
	ErrClosedIndex        = errors.New("extendible: index is closed")
	ErrBucketSizeMismatch = errors.New("extendible: persisted bucket size differs from requested bucket size")
	ErrPageSizeTooSmall   = errors.New("extendible: page size too small")
)

var indexMagic = [8]byte{'E', 'X', 'H', 'A', 'S', 'H', '0', '1'}

type PageID uint32

func (p PageID) valid() bool { return p != invalidPageID }

// bucket es la representación lógica de una cubeta. No contiene punteros a
// memoria ni offsets físicos: la relación con el directorio se realiza mediante
// el PageID de la primera página de su cadena.
type bucket struct {
	LocalDepth int
	Entries    map[string][]storage.RID // clave codificada -> lista de RID
}

func newBucket(localDepth int) *bucket {
	return &bucket{
		LocalDepth: localDepth,
		Entries:    make(map[string][]storage.RID),
	}
}

func (b *bucket) recordCount() int {
	count := 0
	for _, rids := range b.Entries {
		count += len(rids)
	}
	return count
}

func (b *bucket) distinctKeyCount() int { return len(b.Entries) }

// Index implementa common.Index usando Hashing Extensible persistente.
//
// Layout del archivo:
//   - página 0: metadata del índice;
//   - páginas >= 1: cadenas de páginas para el directorio y para los buckets.
//
// El directorio también queda cargado en RAM para no releerlo en cada Search,
// pero se escribe a disco cada vez que cambia (por ejemplo, durante un split).
// Los buckets se leen desde disco cuando una operación los necesita.
type Index struct {
	mu sync.RWMutex

	file     *os.File
	path     string
	pageSize int
	closed   bool

	globalDepth int
	bucketSize  int
	unique      bool
	maxDepth    int
	directory   []PageID // caché del directorio persistido

	directoryHead PageID
	nextPageID    PageID
	freeHead      PageID

	// New/NewFromStorage usan un archivo temporal para conservar compatibilidad
	// con la API antigua. Los índices durables deben usar Create/Open.
	removeOnClose bool
}

var _ common.Index = (*Index)(nil)

// Create crea un índice persistente nuevo y trunca path si ya existía.
func Create(path string, bucketSize int) (*Index, error) {
	return CreateWithPageSize(path, bucketSize, DefaultPageSize)
}

func CreateWithPageSize(path string, bucketSize, pageSize int) (*Index, error) {
	if bucketSize < 1 {
		bucketSize = 1
	}
	if pageSize < 512 || pageSize <= pageHeaderSize {
		return nil, fmt.Errorf("%w: %d", ErrPageSizeTooSmall, pageSize)
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}

	idx := &Index{
		file:          f,
		path:          path,
		pageSize:      pageSize,
		globalDepth:   1,
		bucketSize:    bucketSize,
		maxDepth:      MaxGlobalDepth,
		nextPageID:    firstDataPageID,
		freeHead:      invalidPageID,
		directoryHead: invalidPageID,
	}

	if err := idx.initializeEmptyLocked(); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return idx, nil
}

// Open abre un índice existente directamente desde el archivo .idx.
// No escanea el HeapFile ni llama a Rebuild.
func Open(path string) (*Index, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}

	idx := &Index{file: f, path: path}
	if err := idx.readMetaLocked(); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := idx.loadDirectoryLocked(); err != nil {
		_ = f.Close()
		return nil, err
	}
	idx.maxDepth = MaxGlobalDepth
	if err := idx.validateLocked(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return idx, nil
}

// OpenOrCreate abre path si ya existe; si no existe, crea el índice.
func OpenOrCreate(path string, bucketSize int) (*Index, error) {
	idx, err := Open(path)
	if err == nil {
		if idx.bucketSize != max(bucketSize, 1) {
			_ = idx.Close()
			return nil, fmt.Errorf("%w: disk=%d requested=%d", ErrBucketSizeMismatch, idx.bucketSize, max(bucketSize, 1))
		}
		return idx, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return Create(path, bucketSize)
}

// New conserva la firma antigua. Ahora también es file-backed, pero usa un
// archivo temporal que se elimina al cerrar. Para persistencia real usa
// Create/Open/OpenOrCreate con una ruta explícita.
func New(bucketSize int) *Index {
	idx, err := NewWithOptions(Options{BucketSize: bucketSize})
	if err != nil {
		panic(err)
	}
	return idx
}

// NewWithOptions crea un índice temporal usando las opciones compatibles con
// la implementación anterior. Para un índice durable use Create/Open.
func NewWithOptions(opts Options) (*Index, error) {
	if opts.BucketSize == 0 {
		opts.BucketSize = 4
	}
	if opts.BucketSize < 1 || opts.MaxDepth > MaxGlobalDepth || opts.MaxDepth < 0 {
		return nil, fmt.Errorf("%w: invalid options", storage.ErrBadOptions)
	}
	if opts.MaxDepth == 0 {
		opts.MaxDepth = MaxGlobalDepth
	}
	pageSize := opts.PageSize
	if pageSize == 0 {
		pageSize = DefaultPageSize
	}
	f, err := os.CreateTemp("", "dbms-go-extendible-*.idx")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	idx, err := CreateWithPageSize(path, opts.BucketSize, pageSize)
	if err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	idx.unique = opts.Unique
	idx.maxDepth = opts.MaxDepth
	idx.removeOnClose = true
	return idx, nil
}

func (idx *Index) Path() string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.path
}

func (idx *Index) PageSize() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.pageSize
}

func (idx *Index) BucketSize() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.bucketSize
}

func (idx *Index) Close() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if idx.closed {
		return nil
	}
	if err := idx.persistDirectoryLocked(); err != nil {
		return err
	}
	if err := idx.writeMetaLocked(); err != nil {
		return err
	}
	if err := idx.file.Sync(); err != nil {
		return err
	}
	if err := idx.file.Close(); err != nil {
		return err
	}
	idx.closed = true
	if idx.removeOnClose {
		return os.Remove(idx.path)
	}
	return nil
}

func (idx *Index) Sync() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if err := idx.ensureOpenLocked(); err != nil {
		return err
	}
	if err := idx.writeMetaLocked(); err != nil {
		return err
	}
	return idx.file.Sync()
}

func encodeKey(key any) (string, uint32, error) {
	data, err := storage.EncodeKey(key)
	if err != nil {
		return "", 0, err
	}
	return string(data), hashBytes(data), nil
}

func hashBytes(data []byte) uint32 {
	h := fnv.New32a()
	_, _ = h.Write(data)
	return h.Sum32()
}

func (idx *Index) directoryIndexLocked(hash uint32) uint32 {
	mask := uint32(1)<<uint(idx.globalDepth) - 1
	return hash & mask
}

func (idx *Index) Insert(key any, rid storage.RID) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if err := idx.ensureOpenLocked(); err != nil {
		return err
	}

	encodedKey, hash, err := encodeKey(key)
	if err != nil {
		return err
	}
	dirIndex := idx.directoryIndexLocked(hash)
	bucketHead := idx.directory[dirIndex]
	b, err := idx.readBucketLocked(bucketHead)
	if err != nil {
		return err
	}
	if rids, ok := b.Entries[encodedKey]; ok && idx.unique {
		for _, existing := range rids {
			if existing != rid {
				return fmt.Errorf("%w: %v", storage.ErrDuplicateKey, key)
			}
		}
	}
	b.Entries[encodedKey] = append(b.Entries[encodedKey], rid)

	for {
		if b.recordCount() <= idx.bucketSize || b.distinctKeyCount() <= 1 || b.LocalDepth >= idx.maxDepth {
			if _, err := idx.writeBucketLocked(bucketHead, b); err != nil {
				return err
			}
			return idx.writeMetaLocked()
		}

		oldDepth := b.LocalDepth
		if oldDepth == idx.globalDepth {
			if idx.globalDepth >= idx.maxDepth {
				if _, err := idx.writeBucketLocked(bucketHead, b); err != nil {
					return err
				}
				return idx.writeMetaLocked()
			}
			oldLen := len(idx.directory)
			idx.directory = append(idx.directory, make([]PageID, oldLen)...)
			copy(idx.directory[oldLen:], idx.directory[:oldLen])
			idx.globalDepth++
		}

		left := newBucket(oldDepth + 1)
		right := newBucket(oldDepth + 1)
		splitBit := uint32(1) << uint(oldDepth)
		for k, rids := range b.Entries {
			if hashBytes([]byte(k))&splitBit == 0 {
				left.Entries[k] = rids
			} else {
				right.Entries[k] = rids
			}
		}

		leftHead, err := idx.writeBucketLocked(bucketHead, left)
		if err != nil {
			return err
		}
		rightHead, err := idx.writeBucketLocked(invalidPageID, right)
		if err != nil {
			return err
		}

		// Todas las posiciones que antes apuntaban al bucket dividido se
		// redirigen según el nuevo bit de profundidad local.
		for i, head := range idx.directory {
			if head != bucketHead {
				continue
			}
			if uint32(i)&splitBit == 0 {
				idx.directory[i] = leftHead
			} else {
				idx.directory[i] = rightHead
			}
		}

		if err := idx.persistDirectoryLocked(); err != nil {
			return err
		}
		if err := idx.writeMetaLocked(); err != nil {
			return err
		}

		// La clave insertada puede seguir en un bucket sobrecargado; continúa
		// dividiendo solo ese bucket hasta que sea válido o se alcance el límite.
		dirIndex = idx.directoryIndexLocked(hash)
		bucketHead = idx.directory[dirIndex]
		b, err = idx.readBucketLocked(bucketHead)
		if err != nil {
			return err
		}
	}
}

func (idx *Index) Search(key any) ([]storage.RID, error) {
	encodedKey, hash, err := encodeKey(key)
	if err != nil {
		return nil, err
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if err := idx.ensureOpenLocked(); err != nil {
		return nil, err
	}

	i := idx.directoryIndexLocked(hash)
	b, err := idx.readBucketLocked(idx.directory[i])
	if err != nil {
		return nil, err
	}
	rids := b.Entries[encodedKey]
	out := make([]storage.RID, len(rids))
	copy(out, rids)
	return out, nil
}

func (idx *Index) RangeSearch(keyMin, keyMax any) ([]storage.RID, error) {
	return nil, common.ErrRangeNotSupported
}

func (idx *Index) Delete(key any, rid storage.RID) (bool, error) {
	encodedKey, hash, err := encodeKey(key)
	if err != nil {
		return false, err
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if err := idx.ensureOpenLocked(); err != nil {
		return false, err
	}

	i := idx.directoryIndexLocked(hash)
	head := idx.directory[i]
	b, err := idx.readBucketLocked(head)
	if err != nil {
		return false, err
	}
	rids, exists := b.Entries[encodedKey]
	if !exists {
		return false, nil
	}
	for j, r := range rids {
		if r != rid {
			continue
		}
		b.Entries[encodedKey] = append(rids[:j], rids[j+1:]...)
		if len(b.Entries[encodedKey]) == 0 {
			delete(b.Entries, encodedKey)
		}
		if _, err := idx.writeBucketLocked(head, b); err != nil {
			return false, err
		}
		if err := idx.writeMetaLocked(); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func (idx *Index) SupportsRange() bool { return false }

func (idx *Index) GlobalDepth() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.globalDepth
}

// Stats expone métricas útiles para los experimentos del proyecto.
type Stats struct {
	GlobalDepth    int
	BucketSize     int
	DirectorySlots int
	UniqueBuckets  int
	Records        int
	PageSize       int
	FilePages      uint32
	FreePages      uint32
	FileBytes      int64
}

func (idx *Index) Stats() Stats {
	stats, _ := idx.StatsWithError()
	return stats
}

// StatsWithError devuelve métricas y propaga errores de lectura del archivo.
func (idx *Index) StatsWithError() (Stats, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if err := idx.ensureOpenLocked(); err != nil {
		return Stats{}, err
	}

	unique := make(map[PageID]struct{})
	records := 0
	for _, p := range idx.directory {
		if _, ok := unique[p]; ok {
			continue
		}
		unique[p] = struct{}{}
		b, err := idx.readBucketLocked(p)
		if err != nil {
			return Stats{}, err
		}
		records += b.recordCount()
	}
	free, err := idx.countFreePagesLocked()
	if err != nil {
		return Stats{}, err
	}
	info, err := idx.file.Stat()
	if err != nil {
		return Stats{}, err
	}
	return Stats{
		GlobalDepth:    idx.globalDepth,
		BucketSize:     idx.bucketSize,
		DirectorySlots: len(idx.directory),
		UniqueBuckets:  len(unique),
		Records:        records,
		PageSize:       idx.pageSize,
		FilePages:      uint32(idx.nextPageID),
		FreePages:      free,
		FileBytes:      info.Size(),
	}, nil
}

// Validate verifica que el directorio y todos sus buckets sean consistentes.
func (idx *Index) Validate() error {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if err := idx.ensureOpenLocked(); err != nil {
		return err
	}
	return idx.validateLocked()
}

func (idx *Index) validateLocked() error {
	if idx.globalDepth < 1 || idx.globalDepth > MaxGlobalDepth {
		return fmt.Errorf("%w: invalid global depth %d", ErrCorruptIndex, idx.globalDepth)
	}
	expectedDirLen := 1 << uint(idx.globalDepth)
	if len(idx.directory) != expectedDirLen {
		return fmt.Errorf("%w: directory length=%d expected=%d", ErrCorruptIndex, len(idx.directory), expectedDirLen)
	}

	seen := make(map[PageID]*bucket)
	refs := make(map[PageID]int)
	for _, head := range idx.directory {
		if !head.valid() || head == metaPageID || head >= idx.nextPageID {
			return fmt.Errorf("%w: invalid bucket page %d", ErrCorruptIndex, head)
		}
		refs[head]++
		if _, ok := seen[head]; ok {
			continue
		}
		b, err := idx.readBucketLocked(head)
		if err != nil {
			return err
		}
		if b.LocalDepth < 1 || b.LocalDepth > idx.globalDepth {
			return fmt.Errorf("%w: bucket %d localDepth=%d globalDepth=%d", ErrCorruptIndex, head, b.LocalDepth, idx.globalDepth)
		}
		seen[head] = b
	}

	for head, b := range seen {
		expectedRefs := 1 << uint(idx.globalDepth-b.LocalDepth)
		if refs[head] != expectedRefs {
			return fmt.Errorf("%w: bucket %d refs=%d expected=%d", ErrCorruptIndex, head, refs[head], expectedRefs)
		}
		for key := range b.Entries {
			i := idx.directoryIndexLocked(hashBytes([]byte(key)))
			if idx.directory[i] != head {
				return fmt.Errorf("%w: key %v hashes to bucket %d, stored in %d", ErrCorruptIndex, key, idx.directory[i], head)
			}
		}
	}
	return nil
}

type keyExtractor = storage.KeyExtractor

// CreateFromStorage crea un índice durable en path y lo construye escaneando
// el HeapFile una única vez.
func CreateFromStorage(path string, bucketSize int, hf *heap.HeapFile, keyOf keyExtractor) (*Index, error) {
	if hf == nil {
		return nil, fmt.Errorf("extendible: heap file is nil")
	}
	if keyOf == nil {
		return nil, fmt.Errorf("extendible: key extractor is nil")
	}
	idx, err := Create(path, bucketSize)
	if err != nil {
		return nil, err
	}
	if err := idx.Rebuild(hf, keyOf); err != nil {
		_ = idx.Close()
		return nil, err
	}
	return idx, nil
}

// OpenOrCreateFromStorage abre el índice si ya existe. Solo si no existe lo
// crea y lo construye desde el HeapFile. De esta manera la reapertura normal
// no vuelve a recorrer todos los registros.
func OpenOrCreateFromStorage(path string, bucketSize int, hf *heap.HeapFile, keyOf keyExtractor) (*Index, error) {
	idx, err := Open(path)
	if err == nil {
		if idx.bucketSize != max(bucketSize, 1) {
			_ = idx.Close()
			return nil, fmt.Errorf("%w: disk=%d requested=%d", ErrBucketSizeMismatch, idx.bucketSize, max(bucketSize, 1))
		}
		return idx, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return CreateFromStorage(path, bucketSize, hf, keyOf)
}

// -----------------------------------------------------------------------------
// Persistencia paginada
// -----------------------------------------------------------------------------

func (idx *Index) ensureOpenLocked() error {
	if idx.closed || idx.file == nil {
		return ErrClosedIndex
	}
	return nil
}

func (idx *Index) initializeEmptyLocked() error {
	leftHead, err := idx.writeBucketLocked(invalidPageID, newBucket(1))
	if err != nil {
		return err
	}
	rightHead, err := idx.writeBucketLocked(invalidPageID, newBucket(1))
	if err != nil {
		return err
	}
	idx.directory = []PageID{leftHead, rightHead}
	if err := idx.persistDirectoryLocked(); err != nil {
		return err
	}
	return idx.writeMetaLocked()
}

func (idx *Index) resetLocked() error {
	if err := idx.file.Truncate(0); err != nil {
		return err
	}
	idx.globalDepth = 1
	idx.directory = nil
	idx.directoryHead = invalidPageID
	idx.nextPageID = firstDataPageID
	idx.freeHead = invalidPageID
	return idx.initializeEmptyLocked()
}

func (idx *Index) writeMetaLocked() error {
	if err := idx.ensureOpenLocked(); err != nil {
		return err
	}
	buf := make([]byte, idx.pageSize)
	copy(buf[0:8], indexMagic[:])
	binary.LittleEndian.PutUint32(buf[8:12], indexVersion)
	binary.LittleEndian.PutUint32(buf[12:16], uint32(idx.pageSize))
	binary.LittleEndian.PutUint32(buf[16:20], uint32(idx.globalDepth))
	binary.LittleEndian.PutUint32(buf[20:24], uint32(idx.bucketSize))
	binary.LittleEndian.PutUint32(buf[24:28], uint32(idx.directoryHead))
	binary.LittleEndian.PutUint32(buf[28:32], uint32(idx.nextPageID))
	binary.LittleEndian.PutUint32(buf[32:36], uint32(idx.freeHead))
	_, err := idx.file.WriteAt(buf, 0)
	return err
}

func (idx *Index) readMetaLocked() error {
	prefix := make([]byte, 64)
	if _, err := idx.file.ReadAt(prefix, 0); err != nil {
		return err
	}
	var magic [8]byte
	copy(magic[:], prefix[0:8])
	if magic != indexMagic {
		return fmt.Errorf("%w: invalid magic", ErrCorruptIndex)
	}
	version := binary.LittleEndian.Uint32(prefix[8:12])
	if version != indexVersion {
		return fmt.Errorf("%w: unsupported version %d", ErrCorruptIndex, version)
	}
	idx.pageSize = int(binary.LittleEndian.Uint32(prefix[12:16]))
	if idx.pageSize < 512 || idx.pageSize <= pageHeaderSize {
		return fmt.Errorf("%w: invalid page size %d", ErrCorruptIndex, idx.pageSize)
	}
	idx.globalDepth = int(binary.LittleEndian.Uint32(prefix[16:20]))
	idx.bucketSize = int(binary.LittleEndian.Uint32(prefix[20:24]))
	idx.directoryHead = PageID(binary.LittleEndian.Uint32(prefix[24:28]))
	idx.nextPageID = PageID(binary.LittleEndian.Uint32(prefix[28:32]))
	idx.freeHead = PageID(binary.LittleEndian.Uint32(prefix[32:36]))
	if idx.bucketSize < 1 || idx.nextPageID < firstDataPageID || !idx.directoryHead.valid() {
		return fmt.Errorf("%w: invalid metadata", ErrCorruptIndex)
	}
	return nil
}

func (idx *Index) pageOffset(pid PageID) int64 {
	return int64(pid) * int64(idx.pageSize)
}

type pageHeader struct {
	kind byte
	next PageID
	used uint32
}

func (idx *Index) readPageHeaderLocked(pid PageID) (pageHeader, error) {
	if !pid.valid() || pid == metaPageID || pid >= idx.nextPageID {
		return pageHeader{}, fmt.Errorf("%w: invalid page id %d", ErrCorruptIndex, pid)
	}
	buf := make([]byte, pageHeaderSize)
	if _, err := idx.file.ReadAt(buf, idx.pageOffset(pid)); err != nil {
		return pageHeader{}, err
	}
	return pageHeader{
		kind: buf[0],
		next: PageID(binary.LittleEndian.Uint32(buf[4:8])),
		used: binary.LittleEndian.Uint32(buf[8:12]),
	}, nil
}

func (idx *Index) writePageLocked(pid PageID, kind byte, next PageID, data []byte) error {
	capacity := idx.pageSize - pageHeaderSize
	if len(data) > capacity {
		return fmt.Errorf("%w: page payload %d > %d", ErrCorruptIndex, len(data), capacity)
	}
	buf := make([]byte, idx.pageSize)
	buf[0] = kind
	binary.LittleEndian.PutUint32(buf[4:8], uint32(next))
	binary.LittleEndian.PutUint32(buf[8:12], uint32(len(data)))
	copy(buf[pageHeaderSize:], data)
	_, err := idx.file.WriteAt(buf, idx.pageOffset(pid))
	return err
}

func (idx *Index) allocPageLocked() (PageID, error) {
	if idx.freeHead.valid() {
		pid := idx.freeHead
		h, err := idx.readPageHeaderLocked(pid)
		if err != nil {
			return invalidPageID, err
		}
		if h.kind != pageKindFree {
			return invalidPageID, fmt.Errorf("%w: free list page %d is kind %d", ErrCorruptIndex, pid, h.kind)
		}
		idx.freeHead = h.next
		return pid, nil
	}
	pid := idx.nextPageID
	idx.nextPageID++
	return pid, nil
}

func (idx *Index) freePageLocked(pid PageID) error {
	if !pid.valid() || pid == metaPageID || pid >= idx.nextPageID {
		return fmt.Errorf("%w: cannot free page %d", ErrCorruptIndex, pid)
	}
	if err := idx.writePageLocked(pid, pageKindFree, idx.freeHead, nil); err != nil {
		return err
	}
	idx.freeHead = pid
	return nil
}

func (idx *Index) gatherChainLocked(head PageID, expectedKind byte) ([]PageID, error) {
	if !head.valid() {
		return nil, nil
	}
	var pages []PageID
	seen := make(map[PageID]struct{})
	for p := head; p.valid(); {
		if _, ok := seen[p]; ok {
			return nil, fmt.Errorf("%w: cycle in page chain at %d", ErrCorruptIndex, p)
		}
		seen[p] = struct{}{}
		h, err := idx.readPageHeaderLocked(p)
		if err != nil {
			return nil, err
		}
		if h.kind != expectedKind {
			return nil, fmt.Errorf("%w: page %d kind=%d expected=%d", ErrCorruptIndex, p, h.kind, expectedKind)
		}
		pages = append(pages, p)
		p = h.next
	}
	return pages, nil
}

func (idx *Index) readObjectLocked(head PageID, expectedKind byte) ([]byte, error) {
	pages, err := idx.gatherChainLocked(head, expectedKind)
	if err != nil {
		return nil, err
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("%w: empty object chain", ErrCorruptIndex)
	}
	capacity := idx.pageSize - pageHeaderSize
	var out bytes.Buffer
	for _, p := range pages {
		buf := make([]byte, idx.pageSize)
		if _, err := idx.file.ReadAt(buf, idx.pageOffset(p)); err != nil {
			return nil, err
		}
		used := binary.LittleEndian.Uint32(buf[8:12])
		if used > uint32(capacity) {
			return nil, fmt.Errorf("%w: page %d used=%d", ErrCorruptIndex, p, used)
		}
		_, _ = out.Write(buf[pageHeaderSize : pageHeaderSize+int(used)])
	}
	return out.Bytes(), nil
}

func (idx *Index) writeObjectLocked(existingHead PageID, kind byte, data []byte) (PageID, error) {
	capacity := idx.pageSize - pageHeaderSize
	needed := (len(data) + capacity - 1) / capacity
	if needed == 0 {
		needed = 1
	}

	existing, err := idx.gatherChainLocked(existingHead, kind)
	if err != nil {
		return invalidPageID, err
	}
	pages := append([]PageID(nil), existing...)
	for len(pages) < needed {
		p, err := idx.allocPageLocked()
		if err != nil {
			return invalidPageID, err
		}
		pages = append(pages, p)
	}

	usedPages := pages[:needed]
	for i, pid := range usedPages {
		start := i * capacity
		end := start + capacity
		if end > len(data) {
			end = len(data)
		}
		next := invalidPageID
		if i+1 < len(usedPages) {
			next = usedPages[i+1]
		}
		if err := idx.writePageLocked(pid, kind, next, data[start:end]); err != nil {
			return invalidPageID, err
		}
	}

	for _, pid := range pages[needed:] {
		if err := idx.freePageLocked(pid); err != nil {
			return invalidPageID, err
		}
	}
	return usedPages[0], nil
}

func encodeGob(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decodeGob(data []byte, v any) error {
	return gob.NewDecoder(bytes.NewReader(data)).Decode(v)
}

func (idx *Index) writeBucketLocked(head PageID, b *bucket) (PageID, error) {
	if b == nil || b.Entries == nil {
		return invalidPageID, fmt.Errorf("%w: nil bucket", ErrCorruptIndex)
	}
	data, err := encodeGob(b)
	if err != nil {
		return invalidPageID, fmt.Errorf("extendible: encode bucket: %w", err)
	}
	return idx.writeObjectLocked(head, pageKindBucket, data)
}

func (idx *Index) readBucketLocked(head PageID) (*bucket, error) {
	data, err := idx.readObjectLocked(head, pageKindBucket)
	if err != nil {
		return nil, err
	}
	var b bucket
	if err := decodeGob(data, &b); err != nil {
		return nil, fmt.Errorf("%w: decode bucket %d: %v", ErrCorruptIndex, head, err)
	}
	if b.Entries == nil {
		b.Entries = make(map[string][]storage.RID)
	}
	return &b, nil
}

func (idx *Index) persistDirectoryLocked() error {
	data, err := encodeGob(idx.directory)
	if err != nil {
		return fmt.Errorf("extendible: encode directory: %w", err)
	}
	head, err := idx.writeObjectLocked(idx.directoryHead, pageKindDir, data)
	if err != nil {
		return err
	}
	idx.directoryHead = head
	return nil
}

func (idx *Index) loadDirectoryLocked() error {
	data, err := idx.readObjectLocked(idx.directoryHead, pageKindDir)
	if err != nil {
		return err
	}
	var directory []PageID
	if err := decodeGob(data, &directory); err != nil {
		return fmt.Errorf("%w: decode directory: %v", ErrCorruptIndex, err)
	}
	idx.directory = directory
	return nil
}

func (idx *Index) countFreePagesLocked() (uint32, error) {
	var count uint32
	seen := make(map[PageID]struct{})
	for p := idx.freeHead; p.valid(); {
		if _, ok := seen[p]; ok {
			return 0, fmt.Errorf("%w: cycle in free list at %d", ErrCorruptIndex, p)
		}
		seen[p] = struct{}{}
		h, err := idx.readPageHeaderLocked(p)
		if err != nil {
			return 0, err
		}
		if h.kind != pageKindFree {
			return 0, fmt.Errorf("%w: free page %d has kind %d", ErrCorruptIndex, p, h.kind)
		}
		count++
		p = h.next
	}
	return count, nil
}

// max evita depender de una versión particular del helper predeclared en
// llamadas donde queremos dejar explícita la normalización del bucketSize.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
