// Package sequential implementa un Archivo Secuencial Paginado persistente.
//
// Propiedades principales:
//   - páginas principales de capacidad fija, ordenadas por clave;
//   - cadenas de overflow ordenadas para absorber inserciones sin reescribir todo;
//   - claves duplicadas permitidas (necesario para índices sobre atributos no únicos);
//   - eliminación lazy mediante tombstones;
//   - reorganización automática por desperdicio y por exceso de overflow;
//   - RID lógico estable: una reorganización puede mover físicamente un registro,
//     pero su RecordID no cambia. Esto permite que un índice B+ conserve sus referencias.
package sequential

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"sync"
)

var (
	ErrKeyExists       = errors.New("sequential: key already exists")
	ErrNotFound        = errors.New("sequential: record not found")
	ErrPayloadTooLarge = errors.New("sequential: payload too large")
	ErrCorruptFile     = errors.New("sequential: corrupt file")
)

const (
	magic = 0x53455132 // "SEQ2". Formato incompatible a propósito con SEQ1.

	metaSize = 96

	pageHeaderSize       = 10 // Count uint16 (2) + OverflowHead int64 (8)
	mainSlotHeader       = 19 // tombstone(1) + key(8) + rid(8) + payloadLen(2)
	ovfSlotHeader        = 27 // tombstone(1) + key(8) + rid(8) + next(8) + payloadLen(2)
	ovfNone        int64 = -1

	// ReorgThreshold: fracción de registros eliminados que dispara la
	// reorganización automática. El proyecto propone ~30% de desperdicio.
	ReorgThreshold = 0.30

	// OverflowReorgThreshold limita cuánto del archivo vivo puede residir en
	// overflow antes de reorganizarlo a páginas principales.
	OverflowReorgThreshold = 0.25
)

// RecordID es un identificador lógico estable de registro.
// No codifica página/slot: esos datos pueden cambiar durante una reorganización.
type RecordID uint64

// RID es el nombre corto que usará la capa de índices B+.
type RID = RecordID

func (r RecordID) String() string { return fmt.Sprintf("RID(%d)", uint64(r)) }

func (r RecordID) Uint64() uint64 { return uint64(r) }

func RecordIDFromUint64(v uint64) (RecordID, error) {
	if v == 0 {
		return 0, fmt.Errorf("sequential: RID 0 is invalid")
	}
	return RecordID(v), nil
}

// Record es la representación completa que usa la capa de índices.
type Record struct {
	RID     RecordID
	Key     int64
	Payload []byte
}

// KV mantiene compatibilidad con scans que solo necesitan clave/payload.
type KV struct {
	Key     int64
	Payload []byte
}

// Stats sirve para las comparaciones experimentales solicitadas por el proyecto.
type Stats struct {
	NumPages       int
	LiveCount      int64
	DeadCount      int64
	OverflowLive   int64
	OverflowSlots  int64
	WastedRatio    float64
	OverflowRatio  float64
	MainBytes      int64
	OverflowBytes  int64
	AllocatedBytes int64
}

type locator struct {
	pageID   int32
	slotID   uint16
	overflow bool
	ovfIdx   int64
}

type slotRecord struct {
	deleted bool
	key     int64
	rid     RecordID
	payload []byte
}

type mainPage struct {
	count   uint16
	ovfHead int64
	slots   [][]byte
}

// SeqFile representa el archivo principal + archivo de overflow.
type SeqFile struct {
	mu sync.Mutex

	mainFile *os.File
	ovfFile  *os.File

	pageCapacity int
	payloadSize  int
	mainSlotSize int
	ovfSlotSize  int
	pageSize     int

	numPages  int32
	liveCount int64
	deadCount int64
	ovfCount  int64 // slots físicos asignados en overflow, vivos o tombstone
	liveOvf   int64 // slots vivos actualmente en overflow
	nextRID   RecordID

	pageMinKey []int64
	pageMaxKey []int64
	ridIndex   map[RecordID]locator

	reorgThreshold         float64
	overflowReorgThreshold float64
	closed                 bool
}

// Create inicializa un archivo secuencial nuevo.
func Create(mainPath, ovfPath string, pageCapacity, payloadSize int) (*SeqFile, error) {
	if pageCapacity <= 0 || pageCapacity > math.MaxUint16 {
		return nil, fmt.Errorf("sequential: pageCapacity must be in [1,%d]", math.MaxUint16)
	}
	if payloadSize <= 0 || payloadSize > math.MaxUint16 {
		return nil, fmt.Errorf("sequential: payloadSize must be in [1,%d]", math.MaxUint16)
	}
	if mainPath == ovfPath {
		return nil, fmt.Errorf("sequential: main and overflow paths must be different")
	}

	mf, err := os.OpenFile(mainPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	of, err := os.OpenFile(ovfPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		_ = mf.Close()
		return nil, err
	}

	s := &SeqFile{
		mainFile:               mf,
		ovfFile:                of,
		pageCapacity:           pageCapacity,
		payloadSize:            payloadSize,
		mainSlotSize:           mainSlotHeader + payloadSize,
		ovfSlotSize:            ovfSlotHeader + payloadSize,
		nextRID:                1,
		ridIndex:               make(map[RecordID]locator),
		reorgThreshold:         ReorgThreshold,
		overflowReorgThreshold: OverflowReorgThreshold,
	}
	s.pageSize = pageHeaderSize + pageCapacity*s.mainSlotSize

	if err := s.writeMetaLocked(); err != nil {
		_ = mf.Close()
		_ = of.Close()
		return nil, err
	}
	return s, nil
}

// Open reabre un archivo existente y reconstruye sus índices auxiliares.
func Open(mainPath, ovfPath string) (*SeqFile, error) {
	mf, err := os.OpenFile(mainPath, os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	of, err := os.OpenFile(ovfPath, os.O_RDWR, 0o644)
	if err != nil {
		_ = mf.Close()
		return nil, err
	}

	s := &SeqFile{
		mainFile:               mf,
		ovfFile:                of,
		ridIndex:               make(map[RecordID]locator),
		reorgThreshold:         ReorgThreshold,
		overflowReorgThreshold: OverflowReorgThreshold,
	}
	if err := s.readMetaLocked(); err != nil {
		_ = mf.Close()
		_ = of.Close()
		return nil, err
	}
	s.mainSlotSize = mainSlotHeader + s.payloadSize
	s.ovfSlotSize = ovfSlotHeader + s.payloadSize
	s.pageSize = pageHeaderSize + s.pageCapacity*s.mainSlotSize

	if err := s.validateFileSizesLocked(); err != nil {
		_ = mf.Close()
		_ = of.Close()
		return nil, err
	}
	if err := s.rebuildIndexesLocked(true); err != nil {
		_ = mf.Close()
		_ = of.Close()
		return nil, err
	}
	return s, nil
}

func (s *SeqFile) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	if err := s.writeMetaLocked(); err != nil {
		return err
	}
	if err := s.mainFile.Sync(); err != nil {
		return err
	}
	if err := s.ovfFile.Sync(); err != nil {
		return err
	}
	if err := s.mainFile.Close(); err != nil {
		return err
	}
	if err := s.ovfFile.Close(); err != nil {
		return err
	}
	s.closed = true
	return nil
}

// Sync fuerza a disco páginas, overflow y metadata.
func (s *SeqFile) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return os.ErrClosed
	}
	if err := s.writeMetaLocked(); err != nil {
		return err
	}
	if err := s.mainFile.Sync(); err != nil {
		return err
	}
	return s.ovfFile.Sync()
}

// SetReorgThreshold modifica el umbral de tombstones. Valores >1 desactivan
// la reorganización por borrados, útil en pruebas.
func (s *SeqFile) SetReorgThreshold(ratio float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ratio < 0 {
		ratio = 0
	}
	s.reorgThreshold = ratio
}

// SetOverflowReorgThreshold modifica el umbral de overflow. Valores >1 lo
// desactivan (salvo la protección por longitud de cadena).
func (s *SeqFile) SetOverflowReorgThreshold(ratio float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ratio < 0 {
		ratio = 0
	}
	s.overflowReorgThreshold = ratio
}

func (s *SeqFile) writeMetaLocked() error {
	buf := make([]byte, metaSize)
	binary.LittleEndian.PutUint32(buf[0:4], magic)
	binary.LittleEndian.PutUint32(buf[4:8], uint32(s.pageCapacity))
	binary.LittleEndian.PutUint32(buf[8:12], uint32(s.payloadSize))
	binary.LittleEndian.PutUint32(buf[12:16], uint32(s.numPages))
	binary.LittleEndian.PutUint64(buf[16:24], uint64(s.liveCount))
	binary.LittleEndian.PutUint64(buf[24:32], uint64(s.deadCount))
	binary.LittleEndian.PutUint64(buf[32:40], uint64(s.ovfCount))
	binary.LittleEndian.PutUint64(buf[40:48], uint64(s.liveOvf))
	binary.LittleEndian.PutUint64(buf[48:56], uint64(s.nextRID))
	_, err := s.mainFile.WriteAt(buf, 0)
	return err
}

func (s *SeqFile) readMetaLocked() error {
	buf := make([]byte, metaSize)
	if _, err := s.mainFile.ReadAt(buf, 0); err != nil {
		return fmt.Errorf("%w: cannot read metadata: %v", ErrCorruptFile, err)
	}
	if binary.LittleEndian.Uint32(buf[0:4]) != magic {
		return fmt.Errorf("%w: invalid magic/version", ErrCorruptFile)
	}
	s.pageCapacity = int(binary.LittleEndian.Uint32(buf[4:8]))
	s.payloadSize = int(binary.LittleEndian.Uint32(buf[8:12]))
	if s.pageCapacity <= 0 || s.pageCapacity > math.MaxUint16 || s.payloadSize <= 0 || s.payloadSize > math.MaxUint16 {
		return fmt.Errorf("%w: invalid pageCapacity/payloadSize", ErrCorruptFile)
	}
	s.numPages = int32(binary.LittleEndian.Uint32(buf[12:16]))
	s.liveCount = int64(binary.LittleEndian.Uint64(buf[16:24]))
	s.deadCount = int64(binary.LittleEndian.Uint64(buf[24:32]))
	s.ovfCount = int64(binary.LittleEndian.Uint64(buf[32:40]))
	s.liveOvf = int64(binary.LittleEndian.Uint64(buf[40:48]))
	s.nextRID = RecordID(binary.LittleEndian.Uint64(buf[48:56]))
	if s.liveCount < 0 || s.deadCount < 0 || s.ovfCount < 0 || s.liveOvf < 0 || s.liveOvf > s.ovfCount || s.nextRID == 0 {
		return fmt.Errorf("%w: invalid metadata counters", ErrCorruptFile)
	}
	return nil
}

func (s *SeqFile) validateFileSizesLocked() error {
	mi, err := s.mainFile.Stat()
	if err != nil {
		return err
	}
	expectedMain := int64(metaSize) + int64(s.numPages)*int64(s.pageSize)
	if mi.Size() != expectedMain {
		return fmt.Errorf("%w: main size=%d expected=%d", ErrCorruptFile, mi.Size(), expectedMain)
	}
	oi, err := s.ovfFile.Stat()
	if err != nil {
		return err
	}
	expectedOvf := s.ovfCount * int64(s.ovfSlotSize)
	if oi.Size() != expectedOvf {
		return fmt.Errorf("%w: overflow size=%d expected=%d", ErrCorruptFile, oi.Size(), expectedOvf)
	}
	return nil
}

func (s *SeqFile) pageOffset(pageID int32) int64 {
	return int64(metaSize) + int64(pageID)*int64(s.pageSize)
}

func (s *SeqFile) readPageLocked(pageID int32) (*mainPage, error) {
	if pageID < 0 || pageID >= s.numPages {
		return nil, fmt.Errorf("%w: invalid page id %d", ErrCorruptFile, pageID)
	}
	buf := make([]byte, s.pageSize)
	n, err := s.mainFile.ReadAt(buf, s.pageOffset(pageID))
	if err != nil && err != io.EOF {
		return nil, err
	}
	if n != len(buf) {
		return nil, fmt.Errorf("%w: short page %d", ErrCorruptFile, pageID)
	}
	count := binary.LittleEndian.Uint16(buf[0:2])
	if int(count) > s.pageCapacity {
		return nil, fmt.Errorf("%w: page %d count=%d capacity=%d", ErrCorruptFile, pageID, count, s.pageCapacity)
	}
	ovfHead := int64(binary.LittleEndian.Uint64(buf[2:10]))
	if ovfHead != ovfNone && (ovfHead < 0 || ovfHead >= s.ovfCount) {
		return nil, fmt.Errorf("%w: page %d invalid overflow head %d", ErrCorruptFile, pageID, ovfHead)
	}
	p := &mainPage{count: count, ovfHead: ovfHead, slots: make([][]byte, 0, count)}
	for i := uint16(0); i < count; i++ {
		off := pageHeaderSize + int(i)*s.mainSlotSize
		slot := make([]byte, s.mainSlotSize)
		copy(slot, buf[off:off+s.mainSlotSize])
		if _, err := s.decodeMainSlot(slot); err != nil {
			return nil, fmt.Errorf("%w: page %d slot %d: %v", ErrCorruptFile, pageID, i, err)
		}
		p.slots = append(p.slots, slot)
	}
	return p, nil
}

func (s *SeqFile) writePageLocked(pageID int32, p *mainPage) error {
	if p == nil || int(p.count) != len(p.slots) || int(p.count) > s.pageCapacity {
		return fmt.Errorf("sequential: invalid page representation")
	}
	if p.ovfHead != ovfNone && (p.ovfHead < 0 || p.ovfHead >= s.ovfCount) {
		return fmt.Errorf("sequential: invalid overflow head %d", p.ovfHead)
	}
	buf := make([]byte, s.pageSize)
	binary.LittleEndian.PutUint16(buf[0:2], p.count)
	binary.LittleEndian.PutUint64(buf[2:10], uint64(p.ovfHead))
	for i, slot := range p.slots {
		if len(slot) != s.mainSlotSize {
			return fmt.Errorf("sequential: invalid slot size")
		}
		off := pageHeaderSize + i*s.mainSlotSize
		copy(buf[off:off+s.mainSlotSize], slot)
	}
	_, err := s.mainFile.WriteAt(buf, s.pageOffset(pageID))
	return err
}

func (s *SeqFile) encodeMainSlot(deleted bool, key int64, rid RecordID, payload []byte) ([]byte, error) {
	if err := s.validatePayload(payload); err != nil {
		return nil, err
	}
	buf := make([]byte, s.mainSlotSize)
	if deleted {
		buf[0] = 1
	}
	binary.LittleEndian.PutUint64(buf[1:9], uint64(key))
	binary.LittleEndian.PutUint64(buf[9:17], uint64(rid))
	binary.LittleEndian.PutUint16(buf[17:19], uint16(len(payload)))
	copy(buf[19:], payload)
	return buf, nil
}

func (s *SeqFile) decodeMainSlot(buf []byte) (slotRecord, error) {
	if len(buf) != s.mainSlotSize {
		return slotRecord{}, fmt.Errorf("invalid main slot size")
	}
	length := int(binary.LittleEndian.Uint16(buf[17:19]))
	if length > s.payloadSize {
		return slotRecord{}, fmt.Errorf("invalid payload length %d", length)
	}
	rid := RecordID(binary.LittleEndian.Uint64(buf[9:17]))
	if rid == 0 {
		return slotRecord{}, fmt.Errorf("invalid RID 0")
	}
	payload := make([]byte, length)
	copy(payload, buf[19:19+length])
	return slotRecord{
		deleted: buf[0] == 1,
		key:     int64(binary.LittleEndian.Uint64(buf[1:9])),
		rid:     rid,
		payload: payload,
	}, nil
}

func (s *SeqFile) ovfOffset(idx int64) int64 { return idx * int64(s.ovfSlotSize) }

func (s *SeqFile) readOvfLocked(idx int64) (slotRecord, int64, error) {
	if idx < 0 || idx >= s.ovfCount {
		return slotRecord{}, ovfNone, fmt.Errorf("%w: invalid overflow index %d", ErrCorruptFile, idx)
	}
	buf := make([]byte, s.ovfSlotSize)
	n, err := s.ovfFile.ReadAt(buf, s.ovfOffset(idx))
	if err != nil && err != io.EOF {
		return slotRecord{}, ovfNone, err
	}
	if n != len(buf) {
		return slotRecord{}, ovfNone, fmt.Errorf("%w: short overflow slot %d", ErrCorruptFile, idx)
	}
	length := int(binary.LittleEndian.Uint16(buf[25:27]))
	if length > s.payloadSize {
		return slotRecord{}, ovfNone, fmt.Errorf("%w: invalid overflow payload length %d", ErrCorruptFile, length)
	}
	rid := RecordID(binary.LittleEndian.Uint64(buf[9:17]))
	if rid == 0 {
		return slotRecord{}, ovfNone, fmt.Errorf("%w: invalid overflow RID 0", ErrCorruptFile)
	}
	next := int64(binary.LittleEndian.Uint64(buf[17:25]))
	if next != ovfNone && (next < 0 || next >= s.ovfCount) {
		return slotRecord{}, ovfNone, fmt.Errorf("%w: invalid overflow next %d", ErrCorruptFile, next)
	}
	payload := make([]byte, length)
	copy(payload, buf[27:27+length])
	return slotRecord{
		deleted: buf[0] == 1,
		key:     int64(binary.LittleEndian.Uint64(buf[1:9])),
		rid:     rid,
		payload: payload,
	}, next, nil
}

func (s *SeqFile) writeOvfLocked(idx int64, rec slotRecord, next int64) error {
	if err := s.validatePayload(rec.payload); err != nil {
		return err
	}
	if rec.rid == 0 {
		return fmt.Errorf("sequential: RID 0 is invalid")
	}
	if next != ovfNone && (next < 0 || next >= s.ovfCount) {
		return fmt.Errorf("sequential: invalid overflow next %d", next)
	}
	buf := make([]byte, s.ovfSlotSize)
	if rec.deleted {
		buf[0] = 1
	}
	binary.LittleEndian.PutUint64(buf[1:9], uint64(rec.key))
	binary.LittleEndian.PutUint64(buf[9:17], uint64(rec.rid))
	binary.LittleEndian.PutUint64(buf[17:25], uint64(next))
	binary.LittleEndian.PutUint16(buf[25:27], uint16(len(rec.payload)))
	copy(buf[27:], rec.payload)
	_, err := s.ovfFile.WriteAt(buf, s.ovfOffset(idx))
	return err
}

func (s *SeqFile) validatePayload(payload []byte) error {
	if len(payload) > s.payloadSize {
		return fmt.Errorf("%w: got %d bytes, max %d", ErrPayloadTooLarge, len(payload), s.payloadSize)
	}
	return nil
}

func lessPair(aKey int64, aRID RecordID, bKey int64, bRID RecordID) bool {
	return aKey < bKey || (aKey == bKey && aRID < bRID)
}

func (s *SeqFile) rebuildIndexesLocked(validateCounts bool) error {
	s.pageMinKey = make([]int64, s.numPages)
	s.pageMaxKey = make([]int64, s.numPages)
	s.ridIndex = make(map[RecordID]locator)

	var live, dead, liveOvf int64
	visitedOvf := make(map[int64]bool, s.ovfCount)
	var maxRID RecordID
	var prevPageMax int64
	var havePrevPage bool

	for pid := int32(0); pid < s.numPages; pid++ {
		p, err := s.readPageLocked(pid)
		if err != nil {
			return err
		}
		if p.count == 0 {
			return fmt.Errorf("%w: empty main page %d", ErrCorruptFile, pid)
		}

		first := true
		var minKey, maxKey int64
		var prev slotRecord
		for i, raw := range p.slots {
			rec, err := s.decodeMainSlot(raw)
			if err != nil {
				return err
			}
			if i > 0 && lessPair(rec.key, rec.rid, prev.key, prev.rid) {
				return fmt.Errorf("%w: main page %d is not sorted", ErrCorruptFile, pid)
			}
			prev = rec
			if first || rec.key < minKey {
				minKey = rec.key
			}
			if first || rec.key > maxKey {
				maxKey = rec.key
			}
			first = false
			if rec.rid > maxRID {
				maxRID = rec.rid
			}
			if rec.deleted {
				dead++
			} else {
				if _, exists := s.ridIndex[rec.rid]; exists {
					return fmt.Errorf("%w: duplicate live RID %d", ErrCorruptFile, rec.rid)
				}
				s.ridIndex[rec.rid] = locator{pageID: pid, slotID: uint16(i)}
				live++
			}
		}

		cur := p.ovfHead
		var prevOvf slotRecord
		havePrevOvf := false
		for cur != ovfNone {
			if visitedOvf[cur] {
				return fmt.Errorf("%w: overflow cycle/shared node at %d", ErrCorruptFile, cur)
			}
			visitedOvf[cur] = true
			rec, next, err := s.readOvfLocked(cur)
			if err != nil {
				return err
			}
			if havePrevOvf && lessPair(rec.key, rec.rid, prevOvf.key, prevOvf.rid) {
				return fmt.Errorf("%w: overflow chain of page %d is not sorted", ErrCorruptFile, pid)
			}
			prevOvf, havePrevOvf = rec, true
			if rec.key < minKey {
				minKey = rec.key
			}
			if rec.key > maxKey {
				maxKey = rec.key
			}
			if rec.rid > maxRID {
				maxRID = rec.rid
			}
			if rec.deleted {
				dead++
			} else {
				if _, exists := s.ridIndex[rec.rid]; exists {
					return fmt.Errorf("%w: duplicate live RID %d", ErrCorruptFile, rec.rid)
				}
				s.ridIndex[rec.rid] = locator{pageID: pid, overflow: true, ovfIdx: cur}
				live++
				liveOvf++
			}
			cur = next
		}

		s.pageMinKey[pid] = minKey
		s.pageMaxKey[pid] = maxKey
		if havePrevPage && minKey < prevPageMax {
			return fmt.Errorf("%w: page ranges overlap out of order around page %d", ErrCorruptFile, pid)
		}
		prevPageMax = maxKey
		havePrevPage = true
	}

	if int64(len(visitedOvf)) != s.ovfCount {
		return fmt.Errorf("%w: %d unreachable overflow slots", ErrCorruptFile, s.ovfCount-int64(len(visitedOvf)))
	}
	if validateCounts && (live != s.liveCount || dead != s.deadCount || liveOvf != s.liveOvf) {
		return fmt.Errorf("%w: metadata counters do not match disk (live %d/%d dead %d/%d ovf-live %d/%d)",
			ErrCorruptFile, s.liveCount, live, s.deadCount, dead, s.liveOvf, liveOvf)
	}
	if maxRID >= s.nextRID {
		return fmt.Errorf("%w: nextRID=%d must be greater than max RID=%d", ErrCorruptFile, s.nextRID, maxRID)
	}
	return nil
}

func (s *SeqFile) refreshPageIndexLocked(pid int32, p *mainPage) error {
	if p.count == 0 {
		return fmt.Errorf("sequential: cannot index empty page")
	}
	first := true
	var minKey, maxKey int64
	for i, raw := range p.slots {
		rec, err := s.decodeMainSlot(raw)
		if err != nil {
			return err
		}
		if first || rec.key < minKey {
			minKey = rec.key
		}
		if first || rec.key > maxKey {
			maxKey = rec.key
		}
		first = false
		if !rec.deleted {
			s.ridIndex[rec.rid] = locator{pageID: pid, slotID: uint16(i)}
		}
	}
	cur := p.ovfHead
	for cur != ovfNone {
		rec, next, err := s.readOvfLocked(cur)
		if err != nil {
			return err
		}
		if rec.key < minKey {
			minKey = rec.key
		}
		if rec.key > maxKey {
			maxKey = rec.key
		}
		if !rec.deleted {
			s.ridIndex[rec.rid] = locator{pageID: pid, overflow: true, ovfIdx: cur}
		}
		cur = next
	}
	s.pageMinKey[pid] = minKey
	s.pageMaxKey[pid] = maxKey
	return nil
}

func (s *SeqFile) routePageForInsertLocked(key int64) int32 {
	if s.numPages == 0 {
		return -1
	}
	lo, hi := 0, len(s.pageMinKey)-1
	best := 0
	for lo <= hi {
		mid := lo + (hi-lo)/2
		if s.pageMinKey[mid] <= key {
			best = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return int32(best)
}

func (s *SeqFile) firstCandidatePageLocked(key int64) int32 {
	if s.numPages == 0 {
		return -1
	}
	lo, hi := 0, len(s.pageMaxKey)-1
	ans := len(s.pageMaxKey)
	for lo <= hi {
		mid := lo + (hi-lo)/2
		if s.pageMaxKey[mid] >= key {
			ans = mid
			hi = mid - 1
		} else {
			lo = mid + 1
		}
	}
	if ans == len(s.pageMaxKey) {
		return -1
	}
	return int32(ans)
}

// Insert mantiene la semántica original: la clave debe ser única.
// Para índices B+ sobre atributos no únicos use InsertRecord, que sí permite
// duplicados y retorna el RID estable de cada registro.
func (s *SeqFile) Insert(key int64, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return os.ErrClosed
	}
	if err := s.validatePayload(payload); err != nil {
		return err
	}
	recs, err := s.searchAllLocked(key)
	if err != nil {
		return err
	}
	if len(recs) > 0 {
		return ErrKeyExists
	}
	_, err = s.insertRecordLocked(key, payload)
	return err
}

// InsertRecord inserta un registro permitiendo claves duplicadas y retorna
// un RID lógico estable apto para ser guardado por un índice B+.
func (s *SeqFile) InsertRecord(key int64, payload []byte) (RecordID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, os.ErrClosed
	}
	if err := s.validatePayload(payload); err != nil {
		return 0, err
	}
	return s.insertRecordLocked(key, payload)
}

func (s *SeqFile) insertRecordLocked(key int64, payload []byte) (RecordID, error) {
	rid := s.nextRID
	rec := slotRecord{key: key, rid: rid, payload: append([]byte(nil), payload...)}

	if s.numPages == 0 {
		raw, err := s.encodeMainSlot(false, key, rid, payload)
		if err != nil {
			return 0, err
		}
		p := &mainPage{count: 1, ovfHead: ovfNone, slots: [][]byte{raw}}
		if err := s.writePageLocked(0, p); err != nil {
			return 0, err
		}
		s.numPages = 1
		s.pageMinKey = []int64{key}
		s.pageMaxKey = []int64{key}
		s.ridIndex[rid] = locator{pageID: 0, slotID: 0}
		s.liveCount++
		s.nextRID++
		if err := s.writeMetaLocked(); err != nil {
			return 0, err
		}
		return rid, nil
	}

	pid := s.routePageForInsertLocked(key)
	p, err := s.readPageLocked(pid)
	if err != nil {
		return 0, err
	}

	if int(p.count) < s.pageCapacity {
		raw, err := s.encodeMainSlot(false, key, rid, payload)
		if err != nil {
			return 0, err
		}
		insertAt := len(p.slots)
		for i, sl := range p.slots {
			existing, err := s.decodeMainSlot(sl)
			if err != nil {
				return 0, err
			}
			if lessPair(key, rid, existing.key, existing.rid) {
				insertAt = i
				break
			}
		}
		p.slots = append(p.slots, nil)
		copy(p.slots[insertAt+1:], p.slots[insertAt:])
		p.slots[insertAt] = raw
		p.count++
		if err := s.writePageLocked(pid, p); err != nil {
			return 0, err
		}
		s.liveCount++
		s.nextRID++
		if err := s.refreshPageIndexLocked(pid, p); err != nil {
			return 0, err
		}
		if err := s.writeMetaLocked(); err != nil {
			return 0, err
		}
		return rid, nil
	}

	// Página llena: insertar en overflow manteniendo orden (key,RID).
	idx := s.ovfCount
	s.ovfCount++ // necesario para que writeOvfLocked acepte idx y next válidos

	var prevIdx int64 = ovfNone
	cur := p.ovfHead
	chainLen := 0
	for cur != ovfNone {
		existing, next, err := s.readOvfLocked(cur)
		if err != nil {
			s.ovfCount--
			return 0, err
		}
		chainLen++
		if lessPair(key, rid, existing.key, existing.rid) {
			break
		}
		prevIdx = cur
		cur = next
	}

	if err := s.writeOvfLocked(idx, rec, cur); err != nil {
		s.ovfCount--
		_ = s.ovfFile.Truncate(s.ovfCount * int64(s.ovfSlotSize))
		return 0, err
	}
	if prevIdx == ovfNone {
		p.ovfHead = idx
		if err := s.writePageLocked(pid, p); err != nil {
			return 0, err
		}
	} else {
		prevRec, _, err := s.readOvfLocked(prevIdx)
		if err != nil {
			return 0, err
		}
		// Preserva el tombstone del nodo previo: corrige el bug de resurrección.
		if err := s.writeOvfLocked(prevIdx, prevRec, idx); err != nil {
			return 0, err
		}
	}

	s.liveCount++
	s.liveOvf++
	s.nextRID++
	s.ridIndex[rid] = locator{pageID: pid, overflow: true, ovfIdx: idx}
	if err := s.refreshPageIndexLocked(pid, p); err != nil {
		return 0, err
	}

	// Evita degenerar en una única cadena enorme de overflow.
	if s.shouldReorganizeOverflowLocked(chainLen + 1) {
		if err := s.reorganizeLocked(); err != nil {
			return 0, err
		}
		return rid, nil
	}
	if err := s.writeMetaLocked(); err != nil {
		return 0, err
	}
	return rid, nil
}

func (s *SeqFile) shouldReorganizeOverflowLocked(chainLen int) bool {
	if chainLen > s.pageCapacity {
		return true
	}
	if s.liveCount == 0 {
		return false
	}
	return float64(s.liveOvf)/float64(s.liveCount) > s.overflowReorgThreshold
}

// Search devuelve el primer registro vivo con key (ordenado por RID entre duplicados).
func (s *SeqFile) Search(key int64) ([]byte, bool, error) {
	recs, err := s.SearchAll(key)
	if err != nil {
		return nil, false, err
	}
	if len(recs) == 0 {
		return nil, false, nil
	}
	return append([]byte(nil), recs[0].Payload...), true, nil
}

// SearchAll retorna todos los registros vivos con una clave dada.
func (s *SeqFile) SearchAll(key int64) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, os.ErrClosed
	}
	return s.searchAllLocked(key)
}

func (s *SeqFile) searchAllLocked(key int64) ([]Record, error) {
	start := s.firstCandidatePageLocked(key)
	if start < 0 {
		return nil, nil
	}
	var out []Record
	for pid := start; pid < s.numPages; pid++ {
		if s.pageMinKey[pid] > key {
			break
		}
		recs, err := s.collectPageLiveLocked(pid)
		if err != nil {
			return nil, err
		}
		for _, r := range recs {
			if r.Key == key {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

// Read resuelve un RID lógico incluso si una reorganización movió el registro.
func (s *SeqFile) Read(rid RecordID) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, os.ErrClosed
	}
	loc, ok := s.ridIndex[rid]
	if !ok {
		return nil, ErrNotFound
	}
	if loc.overflow {
		rec, _, err := s.readOvfLocked(loc.ovfIdx)
		if err != nil {
			return nil, err
		}
		if rec.deleted || rec.rid != rid {
			return nil, ErrNotFound
		}
		return append([]byte(nil), rec.payload...), nil
	}
	p, err := s.readPageLocked(loc.pageID)
	if err != nil {
		return nil, err
	}
	if loc.slotID >= p.count {
		return nil, ErrNotFound
	}
	rec, err := s.decodeMainSlot(p.slots[loc.slotID])
	if err != nil {
		return nil, err
	}
	if rec.deleted || rec.rid != rid {
		return nil, ErrNotFound
	}
	return append([]byte(nil), rec.payload...), nil
}

// Delete elimina de forma lazy el primer registro vivo con key.
func (s *SeqFile) Delete(key int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, os.ErrClosed
	}
	recs, err := s.searchAllLocked(key)
	if err != nil {
		return false, err
	}
	if len(recs) == 0 {
		return false, nil
	}
	if err := s.deleteRIDLocked(recs[0].RID); err != nil {
		return false, err
	}
	return true, nil
}

// DeleteRID elimina de forma lazy un registro concreto; es la operación
// recomendada cuando un B+ ya resolvió la entrada a un RID.
func (s *SeqFile) DeleteRID(rid RecordID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return os.ErrClosed
	}
	return s.deleteRIDLocked(rid)
}

func (s *SeqFile) deleteRIDLocked(rid RecordID) error {
	loc, ok := s.ridIndex[rid]
	if !ok {
		return ErrNotFound
	}
	if loc.overflow {
		rec, next, err := s.readOvfLocked(loc.ovfIdx)
		if err != nil {
			return err
		}
		if rec.deleted || rec.rid != rid {
			return ErrNotFound
		}
		rec.deleted = true
		if err := s.writeOvfLocked(loc.ovfIdx, rec, next); err != nil {
			return err
		}
		s.liveOvf--
	} else {
		p, err := s.readPageLocked(loc.pageID)
		if err != nil {
			return err
		}
		if loc.slotID >= p.count {
			return ErrNotFound
		}
		rec, err := s.decodeMainSlot(p.slots[loc.slotID])
		if err != nil {
			return err
		}
		if rec.deleted || rec.rid != rid {
			return ErrNotFound
		}
		raw, err := s.encodeMainSlot(true, rec.key, rec.rid, rec.payload)
		if err != nil {
			return err
		}
		p.slots[loc.slotID] = raw
		if err := s.writePageLocked(loc.pageID, p); err != nil {
			return err
		}
	}
	delete(s.ridIndex, rid)
	s.liveCount--
	s.deadCount++

	if s.shouldReorganizeWasteLocked() {
		return s.reorganizeLocked()
	}
	return s.writeMetaLocked()
}

func (s *SeqFile) shouldReorganizeWasteLocked() bool {
	total := s.liveCount + s.deadCount
	return total > 0 && float64(s.deadCount)/float64(total) > s.reorgThreshold
}

// DeleteAll elimina todas las filas vivas que comparten key.
func (s *SeqFile) DeleteAll(key int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, os.ErrClosed
	}
	recs, err := s.searchAllLocked(key)
	if err != nil {
		return 0, err
	}
	deleted := 0
	// Desactiva reorganización intermedia; se evalúa una vez al final.
	for _, r := range recs {
		loc, ok := s.ridIndex[r.RID]
		if !ok {
			continue
		}
		if loc.overflow {
			rec, next, err := s.readOvfLocked(loc.ovfIdx)
			if err != nil {
				return deleted, err
			}
			rec.deleted = true
			if err := s.writeOvfLocked(loc.ovfIdx, rec, next); err != nil {
				return deleted, err
			}
			s.liveOvf--
		} else {
			p, err := s.readPageLocked(loc.pageID)
			if err != nil {
				return deleted, err
			}
			rec, err := s.decodeMainSlot(p.slots[loc.slotID])
			if err != nil {
				return deleted, err
			}
			raw, err := s.encodeMainSlot(true, rec.key, rec.rid, rec.payload)
			if err != nil {
				return deleted, err
			}
			p.slots[loc.slotID] = raw
			if err := s.writePageLocked(loc.pageID, p); err != nil {
				return deleted, err
			}
		}
		delete(s.ridIndex, r.RID)
		s.liveCount--
		s.deadCount++
		deleted++
	}
	if s.shouldReorganizeWasteLocked() {
		return deleted, s.reorganizeLocked()
	}
	return deleted, s.writeMetaLocked()
}

// Reorganize compacta tombstones y overflow en nuevas páginas principales.
// Los RID lógicos se preservan.
func (s *SeqFile) Reorganize() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return os.ErrClosed
	}
	return s.reorganizeLocked()
}

func (s *SeqFile) reorganizeLocked() error {
	records, err := s.collectLiveRecordsLocked()
	if err != nil {
		return err
	}
	sort.Slice(records, func(i, j int) bool {
		return lessPair(records[i].Key, records[i].RID, records[j].Key, records[j].RID)
	})

	newNumPages := int32(0)
	if len(records) > 0 {
		newNumPages = int32((len(records) + s.pageCapacity - 1) / s.pageCapacity)
	}

	for pid := int32(0); pid < newNumPages; pid++ {
		start := int(pid) * s.pageCapacity
		end := start + s.pageCapacity
		if end > len(records) {
			end = len(records)
		}
		p := &mainPage{count: uint16(end - start), ovfHead: ovfNone, slots: make([][]byte, 0, end-start)}
		for _, r := range records[start:end] {
			raw, err := s.encodeMainSlot(false, r.Key, r.RID, r.Payload)
			if err != nil {
				return err
			}
			p.slots = append(p.slots, raw)
		}
		if err := s.writePageLocked(pid, p); err != nil {
			return err
		}
	}

	s.numPages = newNumPages
	if err := s.mainFile.Truncate(int64(metaSize) + int64(newNumPages)*int64(s.pageSize)); err != nil {
		return err
	}
	if err := s.ovfFile.Truncate(0); err != nil {
		return err
	}
	s.liveCount = int64(len(records))
	s.deadCount = 0
	s.ovfCount = 0
	s.liveOvf = 0

	if err := s.rebuildIndexesLocked(false); err != nil {
		return err
	}
	return s.writeMetaLocked()
}

func (s *SeqFile) collectPageLiveLocked(pid int32) ([]Record, error) {
	p, err := s.readPageLocked(pid)
	if err != nil {
		return nil, err
	}
	main := make([]Record, 0, p.count)
	for _, raw := range p.slots {
		rec, err := s.decodeMainSlot(raw)
		if err != nil {
			return nil, err
		}
		if !rec.deleted {
			main = append(main, Record{RID: rec.rid, Key: rec.key, Payload: append([]byte(nil), rec.payload...)})
		}
	}
	var ovf []Record
	cur := p.ovfHead
	for cur != ovfNone {
		rec, next, err := s.readOvfLocked(cur)
		if err != nil {
			return nil, err
		}
		if !rec.deleted {
			ovf = append(ovf, Record{RID: rec.rid, Key: rec.key, Payload: append([]byte(nil), rec.payload...)})
		}
		cur = next
	}
	out := make([]Record, 0, len(main)+len(ovf))
	i, j := 0, 0
	for i < len(main) || j < len(ovf) {
		if j >= len(ovf) || (i < len(main) && lessPair(main[i].Key, main[i].RID, ovf[j].Key, ovf[j].RID)) {
			out = append(out, main[i])
			i++
		} else {
			out = append(out, ovf[j])
			j++
		}
	}
	return out, nil
}

func (s *SeqFile) collectLiveRecordsLocked() ([]Record, error) {
	out := make([]Record, 0, s.liveCount)
	for pid := int32(0); pid < s.numPages; pid++ {
		recs, err := s.collectPageLiveLocked(pid)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	return out, nil
}

// ScanRecords retorna todos los registros vivos en orden (key,RID).
func (s *SeqFile) ScanRecords() ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, os.ErrClosed
	}
	return s.collectLiveRecordsLocked()
}

// Scan mantiene la API simple del archivo original.
func (s *SeqFile) Scan() ([]KV, error) {
	recs, err := s.ScanRecords()
	if err != nil {
		return nil, err
	}
	out := make([]KV, 0, len(recs))
	for _, r := range recs {
		out = append(out, KV{Key: r.Key, Payload: r.Payload})
	}
	return out, nil
}

// RangeScanRecords aprovecha el orden del secuencial: empieza en la primera
// página cuyo máximo puede alcanzar low y se detiene cuando min > high.
func (s *SeqFile) RangeScanRecords(low, high int64) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, os.ErrClosed
	}
	if low > high || s.numPages == 0 {
		return nil, nil
	}
	start := s.firstCandidatePageLocked(low)
	if start < 0 {
		return nil, nil
	}
	var out []Record
	for pid := start; pid < s.numPages; pid++ {
		if s.pageMinKey[pid] > high {
			break
		}
		recs, err := s.collectPageLiveLocked(pid)
		if err != nil {
			return nil, err
		}
		for _, r := range recs {
			if r.Key < low {
				continue
			}
			if r.Key > high {
				break
			}
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *SeqFile) RangeScan(low, high int64) ([]KV, error) {
	recs, err := s.RangeScanRecords(low, high)
	if err != nil {
		return nil, err
	}
	out := make([]KV, 0, len(recs))
	for _, r := range recs {
		out = append(out, KV{Key: r.Key, Payload: r.Payload})
	}
	return out, nil
}

func (s *SeqFile) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := s.liveCount + s.deadCount
	wasted := 0.0
	if total > 0 {
		wasted = float64(s.deadCount) / float64(total)
	}
	overflow := 0.0
	if s.liveCount > 0 {
		overflow = float64(s.liveOvf) / float64(s.liveCount)
	}
	mainBytes := int64(metaSize) + int64(s.numPages)*int64(s.pageSize)
	overflowBytes := s.ovfCount * int64(s.ovfSlotSize)
	return Stats{
		NumPages:       int(s.numPages),
		LiveCount:      s.liveCount,
		DeadCount:      s.deadCount,
		OverflowLive:   s.liveOvf,
		OverflowSlots:  s.ovfCount,
		WastedRatio:    wasted,
		OverflowRatio:  overflow,
		MainBytes:      mainBytes,
		OverflowBytes:  overflowBytes,
		AllocatedBytes: mainBytes + overflowBytes,
	}
}
