// Package extendible implementa un índice de Hashing Extensible (Dinámico):
// un directorio de punteros a cubetas que se duplica solo cuando hace falta,
// y cubetas que se dividen (localDepth++) en vez de reescribir todo el
// índice en cada crecimiento. Al borrar, las cubetas "hermanas" vuelven a
// fusionarse y el directorio se reduce, de modo que el índice también encoge.
//
// Es un índice en memoria que se reconstruye desde el heap (ver Rebuild). Las
// claves se codifican con storage.EncodeKey, así que sirven int, int32,
// int64, bool, string, []byte y tuplas (claves compuestas) sin riesgo de
// panic por tipos no hasheables.
package extendible

import (
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/dbms-go/v2/dbms/lib/index/common"
	"github.com/dbms-go/v2/dbms/lib/storage"
)

// MaxGlobalDepth es la profundidad máxima permitida del directorio (2^24
// punteros, ~128 MB en el peor caso). hashKey produce 32 bits, pero la
// profundidad útil se alcanza mucho antes: crece ~log2(claves/BucketSize).
const MaxGlobalDepth = 24

const defaultBucketSize = 4

// Options configura el índice.
type Options struct {
	// BucketSize es la capacidad objetivo de RIDs por cubeta (por defecto 4).
	// Una cubeta puede excederla en dos casos que splitear no resuelve: una
	// sola clave con más RIDs que BucketSize, o claves distintas que
	// comparten todos los bits de hash hasta MaxDepth.
	BucketSize int
	// Unique rechaza con ErrDuplicateKey una clave que ya tiene otro RID
	// (para índices sobre la clave primaria). Reinsertar el mismo (clave,
	// RID) es siempre idempotente.
	Unique bool
	// MaxDepth acota el directorio; 0 usa MaxGlobalDepth.
	MaxDepth int
}

type entry struct {
	hash uint32
	rids []storage.RID
}

// bucket es una cubeta del directorio. records cuenta RIDs (no claves).
type bucket struct {
	localDepth int
	records    int
	entries    map[string]*entry // clave codificada -> RIDs (admite duplicados)
}

func newBucket(localDepth int) *bucket {
	return &bucket{localDepth: localDepth, entries: make(map[string]*entry)}
}

// Index implementa common.Index usando Hashing Extensible (Dinámico).
type Index struct {
	mu sync.RWMutex

	opts        Options
	globalDepth int
	directory   []*bucket
	keys        int // claves distintas
	records     int // RIDs en total

	// depthCount[d] es la cantidad de cubetas distintas con localDepth d. Con
	// él, saber si el directorio puede reducirse es O(1) en vez de recorrerlo
	// entero tras cada fusión (que con millones de posiciones domina el costo).
	depthCount [MaxGlobalDepth + 2]int
}

// Compile-time check: *Index debe satisfacer common.Index.
var _ common.Index = (*Index)(nil)

// New crea un índice con profundidad global inicial 1 (2 entradas de
// directorio apuntando a 2 cubetas con localDepth 1). bucketSize menor a 1
// se ajusta a 1: con bucketSize <= 0 cualquier insert dispararía splits
// infinitos hasta chocar con MaxGlobalDepth en vano.
func New(bucketSize int) *Index {
	idx, _ := NewWithOptions(Options{BucketSize: max(bucketSize, 1)})
	return idx
}

// NewWithOptions es New con opciones; valida los parámetros.
func NewWithOptions(opts Options) (*Index, error) {
	if opts.BucketSize == 0 {
		opts.BucketSize = defaultBucketSize
	}
	if opts.MaxDepth == 0 {
		opts.MaxDepth = MaxGlobalDepth
	}
	if opts.BucketSize < 0 || opts.MaxDepth < 1 || opts.MaxDepth > MaxGlobalDepth {
		return nil, fmt.Errorf("%w: BucketSize=%d MaxDepth=%d", storage.ErrBadOptions, opts.BucketSize, opts.MaxDepth)
	}
	idx := &Index{
		opts:        opts,
		globalDepth: 1,
		directory:   []*bucket{newBucket(1), newBucket(1)},
	}
	idx.depthCount[1] = 2
	return idx, nil
}

// hashEncoded: FNV-1a seguido del finalizador de MurmurHash3 (fmix32). FNV
// solo deja mal repartidos los bits bajos, que son justo los que usa el
// directorio; fmix32 es biyectivo y los mezcla.
func hashEncoded(b []byte) uint32 {
	h := uint32(2166136261)
	for _, c := range b {
		h ^= uint32(c)
		h *= 16777619
	}
	h ^= h >> 16
	h *= 0x85ebca6b
	h ^= h >> 13
	h *= 0xc2b2ae35
	h ^= h >> 16
	return h
}

func encode(key any) (string, uint32, error) {
	b, err := storage.EncodeKey(key)
	if err != nil {
		return "", 0, err
	}
	return string(b), hashEncoded(b), nil
}

func (idx *Index) slot(h uint32) uint32 {
	return h & (uint32(1)<<uint(idx.globalDepth) - 1)
}

// Insert agrega (key, rid). Es idempotente para el mismo par; con Unique
// devuelve ErrDuplicateKey si la clave ya existe con otro RID.
func (idx *Index) Insert(key any, rid storage.RID) error {
	k, h, err := encode(key)
	if err != nil {
		return err
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()

	di := idx.slot(h)
	b := idx.directory[di]
	if e, ok := b.entries[k]; ok {
		if slices.Contains(e.rids, rid) {
			return nil
		}
		if idx.opts.Unique {
			return fmt.Errorf("%w: %v", storage.ErrDuplicateKey, key)
		}
		e.rids = append(e.rids, rid)
	} else {
		b.entries[k] = &entry{hash: h, rids: []storage.RID{rid}}
		idx.keys++
	}
	b.records++
	idx.records++

	// Splitear mientras la cubeta de esta clave desborde y splitear sirva:
	// con una sola clave, o en MaxDepth, se acepta la cubeta sobrecargada.
	for b.records > idx.opts.BucketSize && len(b.entries) > 1 && b.localDepth < idx.opts.MaxDepth {
		idx.splitLocked(b, di)
		di = idx.slot(h)
		b = idx.directory[di]
	}
	return nil
}

// splitLocked divide b en dos cubetas con localDepth+1 según el bit
// localDepth del hash, duplicando el directorio si hace falta. di es una
// posición del directorio que apunta a b.
func (idx *Index) splitLocked(b *bucket, di uint32) {
	if b.localDepth == idx.globalDepth {
		idx.directory = append(idx.directory, idx.directory...)
		idx.globalDepth++
	}
	d := b.localDepth
	bit := uint32(1) << uint(d)
	b0, b1 := newBucket(d+1), newBucket(d+1)
	for k, e := range b.entries {
		t := b0
		if e.hash&bit != 0 {
			t = b1
		}
		t.entries[k] = e
		t.records += len(e.rids)
	}
	idx.depthCount[d]--
	idx.depthCount[d+1] += 2
	// Las posiciones que apuntan a b son las que comparten sus d bits bajos.
	for i := int(di & (bit - 1)); i < len(idx.directory); i += int(bit) {
		if uint32(i)&bit == 0 {
			idx.directory[i] = b0
		} else {
			idx.directory[i] = b1
		}
	}
}

// Search devuelve (una copia de) todos los RID de key; slice vacío si no hay.
func (idx *Index) Search(key any) ([]storage.RID, error) {
	k, h, err := encode(key)
	if err != nil {
		return nil, err
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if e, ok := idx.directory[idx.slot(h)].entries[k]; ok {
		return slices.Clone(e.rids), nil
	}
	return []storage.RID{}, nil
}

func (idx *Index) RangeSearch(keyMin, keyMax any) ([]storage.RID, error) {
	return nil, common.ErrRangeNotSupported
}

// Delete quita (key, rid) y devuelve si existía. Tras borrar, fusiona
// cubetas hermanas y reduce el directorio cuando es posible.
func (idx *Index) Delete(key any, rid storage.RID) (bool, error) {
	k, h, err := encode(key)
	if err != nil {
		return false, err
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()

	b := idx.directory[idx.slot(h)]
	e, ok := b.entries[k]
	if !ok {
		return false, nil
	}
	j := slices.Index(e.rids, rid)
	if j < 0 {
		return false, nil
	}
	e.rids = slices.Delete(e.rids, j, j+1)
	b.records--
	idx.records--
	if len(e.rids) == 0 {
		delete(b.entries, k)
		idx.keys--
	}
	idx.mergeLocked(h)
	return true, nil
}

// mergeLocked fusiona la cubeta de h con su hermana mientras tengan la misma
// profundidad local y quepan juntas, y después reduce el directorio.
func (idx *Index) mergeLocked(h uint32) {
	merged := false
	for {
		b := idx.directory[idx.slot(h)]
		d := b.localDepth
		if d <= 1 {
			break
		}
		prefix := h & (uint32(1)<<uint(d) - 1)
		buddy := idx.directory[prefix^(uint32(1)<<uint(d-1))]
		if buddy.localDepth != d || b.records+buddy.records > idx.opts.BucketSize {
			break
		}
		nb := newBucket(d - 1)
		for _, src := range []*bucket{b, buddy} {
			for k, e := range src.entries {
				nb.entries[k] = e
			}
		}
		nb.records = b.records + buddy.records
		idx.depthCount[d] -= 2
		idx.depthCount[d-1]++
		step := 1 << uint(d-1)
		for i := int(prefix) & (step - 1); i < len(idx.directory); i += step {
			idx.directory[i] = nb
		}
		merged = true
	}
	if merged {
		idx.shrinkLocked()
	}
}

// shrinkLocked reduce el directorio a la mitad mientras ninguna cubeta use
// toda la profundidad global.
func (idx *Index) shrinkLocked() {
	for idx.globalDepth > 1 && idx.depthCount[idx.globalDepth] == 0 {
		idx.directory = slices.Clone(idx.directory[:len(idx.directory)/2])
		idx.globalDepth--
	}
}

func (idx *Index) SupportsRange() bool { return false }

// GlobalDepth expone la profundidad global actual del directorio, útil
// para inspección/depuración y para la comparación experimental del proyecto.
func (idx *Index) GlobalDepth() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.globalDepth
}

// Stats resume el estado del índice.
type Stats struct {
	GlobalDepth      int
	DirectorySize    int
	Buckets          int
	Keys             int     // claves distintas
	Records          int     // RIDs
	MaxBucketRecords int     // RIDs de la cubeta más llena
	Overflowing      int     // cubetas por encima de BucketSize
	LoadFactor       float64 // Records / (Buckets * BucketSize)
}

func (idx *Index) Stats() Stats {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	s := Stats{GlobalDepth: idx.globalDepth, DirectorySize: len(idx.directory), Keys: idx.keys, Records: idx.records}
	seen := make(map[*bucket]struct{}, len(idx.directory))
	for _, b := range idx.directory {
		if _, dup := seen[b]; dup {
			continue
		}
		seen[b] = struct{}{}
		s.MaxBucketRecords = max(s.MaxBucketRecords, b.records)
		if b.records > idx.opts.BucketSize {
			s.Overflowing++
		}
	}
	s.Buckets = len(seen)
	s.LoadFactor = float64(s.Records) / float64(s.Buckets*idx.opts.BucketSize)
	return s
}

var errNilArg = errors.New("extendible: argumento nil")
