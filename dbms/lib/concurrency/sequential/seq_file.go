// Package sequential: Archivo Secuencial Paginado en disco. Registros
// ordenados por clave int64 en páginas principales de tamaño fijo, con
// una cadena de overflow por página, eliminación lazy y reorganización
// periódica al superar un umbral de espacio desperdiciado.
//
// RecordID es un identificador LÓGICO y ESTABLE: se asigna una vez al
// insertar y nunca cambia, aunque el registro se reubique físicamente por
// culpa de otro insert/delete en la misma página o de una reorganización.
// Esto es lo que permite que un índice B+ agrupado guarde RIDs en memoria
// entre operaciones sin que se invaliden por un cambio en otra fila.
package sequential

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
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
	magic = 0x53455131 // "SEQ1"

	metaSize = 96

	pageHeaderSize       = 10 // Count uint16 (2) + OverflowHead int64 (8)
	mainSlotHeader       = 19 // tombstone(1) + key(8) + rid(8) + longitud payload(2)
	ovfSlotHeader        = 27 // tombstone(1) + key(8) + rid(8) + next(8) + longitud payload(2)
	ovfNone        int64 = -1

	// ReorgThreshold: fracción por defecto de registros eliminados que
	// dispara una reorganización automática.
	ReorgThreshold = 0.30

	// OverflowReorgThreshold: fracción por defecto de registros vivos que
	// pueden vivir en overflow antes de forzar una reorganización, para no
	// degenerar en una cadena enorme dentro de una sola página.
	OverflowReorgThreshold = 0.25
)

// RecordID: identificador lógico estable de un registro. No codifica
// página ni posición física — esos datos pueden cambiar libremente.
type RecordID uint64

func (r RecordID) String() string { return fmt.Sprintf("RID(%d)", uint64(r)) }

// KV: par clave/payload vivo, retornado por los scans simples.
type KV struct {
	Key     int64
	Payload []byte
}

// RecordInfo: clave, RID y payload de un registro vivo, tal y como se
// entrega en un scan con RID incluido.
type RecordInfo struct {
	Key     int64
	RID     RecordID
	Payload []byte
}

// Stats: estado actual del archivo, usado en la comparación experimental
// contra el Heap File.
type Stats struct {
	NumPages      int
	LiveCount     int64
	DeadCount     int64
	OverflowLive  int64
	OverflowSlots int64
	WastedRatio   float64
	OverflowRatio float64
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

// locator ubica físicamente un registro vivo: en un slot principal
// (mainSlot >= 0) o en un nodo de la cadena de overflow (ovfIdx != ovfNone).
type locator struct {
	pageID   int32
	mainSlot int
	ovfIdx   int64
}

// SeqFile: archivo secuencial paginado ordenado por clave int64.
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
	ovfCount  int64 // slots físicos asignados en overflow (vivos o tombstone)
	liveOvf   int64 // slots vivos actualmente en overflow
	nextRID   RecordID

	pageMinKey []int64
	ridIndex   map[RecordID]locator

	reorgThreshold         float64
	overflowReorgThreshold float64
}

// Create inicializa un archivo secuencial nuevo (main + overflow), con
// registros de largo fijo (hasta payloadSize bytes) y pageCapacity
// registros por página principal.
func Create(mainPath, ovfPath string, pageCapacity, payloadSize int) (*SeqFile, error) {
	if pageCapacity <= 0 {
		return nil, fmt.Errorf("sequential: pageCapacity must be > 0")
	}
	if payloadSize <= 0 {
		return nil, fmt.Errorf("sequential: payloadSize must be > 0")
	}
	mf, err := os.OpenFile(mainPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	of, err := os.OpenFile(ovfPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		mf.Close()
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

	if err := s.writeMeta(); err != nil {
		mf.Close()
		of.Close()
		return nil, err
	}
	return s, nil
}

// Open reabre un archivo secuencial existente, leyendo su layout desde
// el header de metadata escrito por Create y reconstruyendo los índices
// en memoria (pageMinKey, ridIndex) a partir del contenido en disco.
func Open(mainPath, ovfPath string) (*SeqFile, error) {
	mf, err := os.OpenFile(mainPath, os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	of, err := os.OpenFile(ovfPath, os.O_RDWR, 0o644)
	if err != nil {
		mf.Close()
		return nil, err
	}

	s := &SeqFile{
		mainFile:               mf,
		ovfFile:                of,
		ridIndex:               make(map[RecordID]locator),
		reorgThreshold:         ReorgThreshold,
		overflowReorgThreshold: OverflowReorgThreshold,
	}
	if err := s.readMeta(); err != nil {
		mf.Close()
		of.Close()
		return nil, err
	}
	s.mainSlotSize = mainSlotHeader + s.payloadSize
	s.ovfSlotSize = ovfSlotHeader + s.payloadSize
	s.pageSize = pageHeaderSize + s.pageCapacity*s.mainSlotSize

	if err := s.rebuildIndexesLocked(); err != nil {
		mf.Close()
		of.Close()
		return nil, err
	}
	return s, nil
}

// Close flushes metadata y cierra ambos archivos.
func (s *SeqFile) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writeMeta(); err != nil {
		return err
	}
	if err := s.mainFile.Close(); err != nil {
		return err
	}
	return s.ovfFile.Close()
}

// SetReorgThreshold cambia el umbral (0..1) de espacio desperdiciado que
// dispara la reorganización automática tras un delete. Sobre todo para tests.
func (s *SeqFile) SetReorgThreshold(ratio float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reorgThreshold = ratio
}

// SetOverflowReorgThreshold cambia el umbral (0..1) de registros vivos en
// overflow que dispara la reorganización automática tras un insert.
func (s *SeqFile) SetOverflowReorgThreshold(ratio float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.overflowReorgThreshold = ratio
}

func (s *SeqFile) writeMeta() error {
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

func (s *SeqFile) readMeta() error {
	buf := make([]byte, metaSize)
	if _, err := s.mainFile.ReadAt(buf, 0); err != nil {
		return err
	}
	if binary.LittleEndian.Uint32(buf[0:4]) != magic {
		return fmt.Errorf("%w: magic inválido en el header", ErrCorruptFile)
	}
	s.pageCapacity = int(binary.LittleEndian.Uint32(buf[4:8]))
	s.payloadSize = int(binary.LittleEndian.Uint32(buf[8:12]))
	s.numPages = int32(binary.LittleEndian.Uint32(buf[12:16]))
	s.liveCount = int64(binary.LittleEndian.Uint64(buf[16:24]))
	s.deadCount = int64(binary.LittleEndian.Uint64(buf[24:32]))
	s.ovfCount = int64(binary.LittleEndian.Uint64(buf[32:40]))
	s.liveOvf = int64(binary.LittleEndian.Uint64(buf[40:48]))
	s.nextRID = RecordID(binary.LittleEndian.Uint64(buf[48:56]))
	return nil
}

// ---- I/O de páginas principales ----

func (s *SeqFile) pageOffset(pageID int32) int64 {
	return int64(metaSize) + int64(pageID)*int64(s.pageSize)
}

func (s *SeqFile) readPage(pageID int32) (*mainPage, error) {
	buf := make([]byte, s.pageSize)
	if _, err := s.mainFile.ReadAt(buf, s.pageOffset(pageID)); err != nil && err != io.EOF {
		return nil, err
	}
	count := binary.LittleEndian.Uint16(buf[0:2])
	ovfHead := int64(binary.LittleEndian.Uint64(buf[2:10]))
	p := &mainPage{count: count, ovfHead: ovfHead}
	for i := uint16(0); i < count; i++ {
		off := pageHeaderSize + int(i)*s.mainSlotSize
		slot := make([]byte, s.mainSlotSize)
		copy(slot, buf[off:off+s.mainSlotSize])
		p.slots = append(p.slots, slot)
	}
	return p, nil
}

func (s *SeqFile) writePage(pageID int32, p *mainPage) error {
	buf := make([]byte, s.pageSize)
	binary.LittleEndian.PutUint16(buf[0:2], p.count)
	binary.LittleEndian.PutUint64(buf[2:10], uint64(p.ovfHead))
	for i, slot := range p.slots {
		off := pageHeaderSize + i*s.mainSlotSize
		copy(buf[off:off+s.mainSlotSize], slot)
	}
	_, err := s.mainFile.WriteAt(buf, s.pageOffset(pageID))
	return err
}

func mainSlotEncode(buf []byte, deleted bool, key int64, rid RecordID, payload []byte) {
	if deleted {
		buf[0] = 1
	} else {
		buf[0] = 0
	}
	binary.LittleEndian.PutUint64(buf[1:9], uint64(key))
	binary.LittleEndian.PutUint64(buf[9:17], uint64(rid))
	binary.LittleEndian.PutUint16(buf[17:19], uint16(len(payload)))
	copy(buf[19:], payload)
}

func mainSlotDecode(buf []byte) slotRecord {
	length := binary.LittleEndian.Uint16(buf[17:19])
	payload := make([]byte, length)
	copy(payload, buf[19:19+int(length)])
	return slotRecord{
		deleted: buf[0] == 1,
		key:     int64(binary.LittleEndian.Uint64(buf[1:9])),
		rid:     RecordID(binary.LittleEndian.Uint64(buf[9:17])),
		payload: payload,
	}
}

// ---- I/O de la cadena de overflow ----

func (s *SeqFile) ovfOffset(idx int64) int64 { return idx * int64(s.ovfSlotSize) }

func (s *SeqFile) readOvf(idx int64) (slotRecord, int64, error) {
	buf := make([]byte, s.ovfSlotSize)
	if _, err := s.ovfFile.ReadAt(buf, s.ovfOffset(idx)); err != nil && err != io.EOF {
		return slotRecord{}, ovfNone, err
	}
	length := binary.LittleEndian.Uint16(buf[25:27])
	payload := make([]byte, length)
	copy(payload, buf[27:27+int(length)])
	next := int64(binary.LittleEndian.Uint64(buf[17:25]))
	rec := slotRecord{
		deleted: buf[0] == 1,
		key:     int64(binary.LittleEndian.Uint64(buf[1:9])),
		rid:     RecordID(binary.LittleEndian.Uint64(buf[9:17])),
		payload: payload,
	}
	return rec, next, nil
}

func (s *SeqFile) writeOvf(idx int64, rec slotRecord, next int64) error {
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

func (s *SeqFile) allocOvf() int64 {
	idx := s.ovfCount
	s.ovfCount++
	return idx
}

func validatePayloadSize(payload []byte, size int) error {
	if len(payload) > size {
		return fmt.Errorf("%w: %d bytes, máximo %d", ErrPayloadTooLarge, len(payload), size)
	}
	return nil
}

func lessPair(aKey int64, aRID RecordID, bKey int64, bRID RecordID) bool {
	return aKey < bKey || (aKey == bKey && aRID < bRID)
}

// rebuildIndexesLocked reconstruye pageMinKey y ridIndex desde disco,
// recuperando también nextRID a partir del máximo RID visto.
func (s *SeqFile) rebuildIndexesLocked() error {
	s.pageMinKey = make([]int64, s.numPages)
	s.ridIndex = make(map[RecordID]locator)
	var maxRID RecordID

	for pid := int32(0); pid < s.numPages; pid++ {
		page, err := s.readPage(pid)
		if err != nil {
			return err
		}
		minKey := int64(0)
		if page.count > 0 {
			minKey = mainSlotDecode(page.slots[0]).key
		}
		s.pageMinKey[pid] = minKey

		for i, raw := range page.slots {
			rec := mainSlotDecode(raw)
			if rec.rid > maxRID {
				maxRID = rec.rid
			}
			if !rec.deleted {
				s.ridIndex[rec.rid] = locator{pageID: pid, mainSlot: i, ovfIdx: ovfNone}
			}
		}
		cur := page.ovfHead
		for cur != ovfNone {
			rec, next, err := s.readOvf(cur)
			if err != nil {
				return err
			}
			if rec.rid > maxRID {
				maxRID = rec.rid
			}
			if !rec.deleted {
				s.ridIndex[rec.rid] = locator{pageID: pid, mainSlot: -1, ovfIdx: cur}
			}
			cur = next
		}
	}
	if s.nextRID <= maxRID {
		s.nextRID = maxRID + 1
	}
	if s.nextRID == 0 {
		s.nextRID = 1
	}
	return nil
}

func (s *SeqFile) routePage(key int64) int32 {
	if s.numPages == 0 {
		return -1
	}
	lo, hi := 0, len(s.pageMinKey)-1
	best := 0
	for lo <= hi {
		mid := (lo + hi) / 2
		if s.pageMinKey[mid] <= key {
			best = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return int32(best)
}

// Insert agrega un par clave/payload único, manteniendo el archivo
// ordenado por clave. Rechaza claves duplicadas (usar InsertRecord para
// permitirlas, por ejemplo en índices sobre columnas no únicas).
func (s *SeqFile) Insert(key int64, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validatePayloadSize(payload, s.payloadSize); err != nil {
		return err
	}
	if s.numPages > 0 {
		pid := s.routePage(key)
		if _, found, err := s.searchInPageLocked(pid, key); err != nil {
			return err
		} else if found {
			return ErrKeyExists
		}
	}
	_, err := s.insertRecordLocked(key, payload)
	return err
}

// InsertRecord agrega un par clave/payload (permite claves duplicadas) y
// retorna el RID lógico y estable asignado a este registro.
func (s *SeqFile) InsertRecord(key int64, payload []byte) (RecordID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validatePayloadSize(payload, s.payloadSize); err != nil {
		return 0, err
	}
	return s.insertRecordLocked(key, payload)
}

func (s *SeqFile) insertRecordLocked(key int64, payload []byte) (RecordID, error) {
	rid := s.nextRID

	if s.numPages == 0 {
		slot := make([]byte, s.mainSlotSize)
		mainSlotEncode(slot, false, key, rid, payload)
		page := &mainPage{count: 1, ovfHead: ovfNone, slots: [][]byte{slot}}
		if err := s.writePage(0, page); err != nil {
			return 0, err
		}
		s.numPages = 1
		s.pageMinKey = []int64{key}
		s.ridIndex[rid] = locator{pageID: 0, mainSlot: 0, ovfIdx: ovfNone}
		s.liveCount++
		s.nextRID++
		return rid, s.writeMeta()
	}

	pid := s.routePage(key)
	page, err := s.readPage(pid)
	if err != nil {
		return 0, err
	}

	if int(page.count) < s.pageCapacity {
		slot := make([]byte, s.mainSlotSize)
		mainSlotEncode(slot, false, key, rid, payload)

		insertAt := len(page.slots)
		for i, sl := range page.slots {
			existing := mainSlotDecode(sl)
			if lessPair(key, rid, existing.key, existing.rid) {
				insertAt = i
				break
			}
		}
		newSlots := make([][]byte, 0, len(page.slots)+1)
		newSlots = append(newSlots, page.slots[:insertAt]...)
		newSlots = append(newSlots, slot)
		newSlots = append(newSlots, page.slots[insertAt:]...)
		page.slots = newSlots
		page.count++

		if err := s.writePage(pid, page); err != nil {
			return 0, err
		}
		if pid == 0 && insertAt == 0 {
			s.pageMinKey[0] = key
		}
		s.liveCount++
		s.nextRID++
		if err := s.reindexPageLocked(pid, page); err != nil {
			return 0, err
		}
		return rid, s.writeMeta()
	}

	// Página llena: insertar en la cadena de overflow, manteniendo el
	// orden (key, rid) para que los scans puedan fusionarla con main.
	idx := s.allocOvf()
	var prevIdx int64 = ovfNone
	cur := page.ovfHead
	chainLen := 0
	for cur != ovfNone {
		existing, next, err := s.readOvf(cur)
		if err != nil {
			return 0, err
		}
		chainLen++
		if lessPair(key, rid, existing.key, existing.rid) {
			break
		}
		prevIdx = cur
		cur = next
	}

	rec := slotRecord{key: key, rid: rid, payload: payload}
	if err := s.writeOvf(idx, rec, cur); err != nil {
		return 0, err
	}
	if prevIdx == ovfNone {
		page.ovfHead = idx
		if err := s.writePage(pid, page); err != nil {
			return 0, err
		}
	} else {
		prevRec, _, err := s.readOvf(prevIdx)
		if err != nil {
			return 0, err
		}
		// Preserva el estado (deleted) del nodo previo: sobreescribirlo con
		// deleted=false lo resucitaría si ya estaba eliminado.
		if err := s.writeOvf(prevIdx, prevRec, idx); err != nil {
			return 0, err
		}
	}

	s.liveCount++
	s.liveOvf++
	s.nextRID++
	s.ridIndex[rid] = locator{pageID: pid, mainSlot: -1, ovfIdx: idx}

	if s.shouldReorganizeOverflowLocked(chainLen + 1) {
		if err := s.reorganizeLocked(); err != nil {
			return 0, err
		}
		return rid, nil
	}
	return rid, s.writeMeta()
}

func (s *SeqFile) shouldReorganizeOverflowLocked(chainLen int) bool {
	if chainLen > s.pageCapacity*4 {
		return true
	}
	if s.liveCount == 0 {
		return false
	}
	return float64(s.liveOvf)/float64(s.liveCount) > s.overflowReorgThreshold
}

// reindexPageLocked actualiza ridIndex y pageMinKey tras una mutación que
// insertó/reordenó slots principales de una página (los índices de los
// demás slots de esa página pueden haber cambiado de posición).
func (s *SeqFile) reindexPageLocked(pid int32, page *mainPage) error {
	if page.count == 0 {
		return nil
	}
	minKey := mainSlotDecode(page.slots[0]).key
	for i, raw := range page.slots {
		rec := mainSlotDecode(raw)
		if !rec.deleted {
			s.ridIndex[rec.rid] = locator{pageID: pid, mainSlot: i, ovfIdx: ovfNone}
		}
	}
	s.pageMinKey[pid] = minKey
	return nil
}

// searchInPageLocked busca key dentro de la página pid (main + overflow).
func (s *SeqFile) searchInPageLocked(pid int32, key int64) ([]byte, bool, error) {
	page, err := s.readPage(pid)
	if err != nil {
		return nil, false, err
	}
	for _, slot := range page.slots {
		rec := mainSlotDecode(slot)
		if !rec.deleted && rec.key == key {
			return rec.payload, true, nil
		}
	}
	cur := page.ovfHead
	for cur != ovfNone {
		rec, next, err := s.readOvf(cur)
		if err != nil {
			return nil, false, err
		}
		if !rec.deleted && rec.key == key {
			return rec.payload, true, nil
		}
		cur = next
	}
	return nil, false, nil
}

// Search busca la primera clave viva que coincida (ver SearchAll para
// obtener todas las coincidencias cuando se permiten duplicados).
func (s *SeqFile) Search(key int64) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.numPages == 0 {
		return nil, false, nil
	}
	return s.searchInPageLocked(s.routePage(key), key)
}

// Read resuelve un RID lógico a su payload, incluso si el registro se
// reubicó físicamente por otro insert/delete o por una reorganización.
func (s *SeqFile) Read(rid RecordID) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	loc, ok := s.ridIndex[rid]
	if !ok {
		return nil, ErrNotFound
	}
	if loc.ovfIdx != ovfNone {
		rec, _, err := s.readOvf(loc.ovfIdx)
		if err != nil {
			return nil, err
		}
		if rec.deleted || rec.rid != rid {
			return nil, ErrNotFound
		}
		return rec.payload, nil
	}
	page, err := s.readPage(loc.pageID)
	if err != nil {
		return nil, err
	}
	if loc.mainSlot >= len(page.slots) {
		return nil, ErrNotFound
	}
	rec := mainSlotDecode(page.slots[loc.mainSlot])
	if rec.deleted || rec.rid != rid {
		return nil, ErrNotFound
	}
	return rec.payload, nil
}

// Delete elimina de forma lazy el primer registro vivo con key.
func (s *SeqFile) Delete(key int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.numPages == 0 {
		return false, nil
	}
	pid := s.routePage(key)
	page, err := s.readPage(pid)
	if err != nil {
		return false, err
	}
	for i, slot := range page.slots {
		rec := mainSlotDecode(slot)
		if !rec.deleted && rec.key == key {
			mainSlotEncode(page.slots[i], true, rec.key, rec.rid, rec.payload)
			if err := s.writePage(pid, page); err != nil {
				return false, err
			}
			delete(s.ridIndex, rec.rid)
			s.liveCount--
			s.deadCount++
			return true, s.maybeReorganizeLocked()
		}
	}
	cur := page.ovfHead
	for cur != ovfNone {
		rec, next, err := s.readOvf(cur)
		if err != nil {
			return false, err
		}
		if !rec.deleted && rec.key == key {
			rec.deleted = true
			if err := s.writeOvf(cur, rec, next); err != nil {
				return false, err
			}
			delete(s.ridIndex, rec.rid)
			s.liveCount--
			s.deadCount++
			s.liveOvf--
			return true, s.maybeReorganizeLocked()
		}
		cur = next
	}
	return false, nil
}

// DeleteRID elimina de forma lazy el registro identificado por rid.
func (s *SeqFile) DeleteRID(rid RecordID) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	loc, ok := s.ridIndex[rid]
	if !ok {
		return ErrNotFound
	}
	if loc.ovfIdx != ovfNone {
		rec, next, err := s.readOvf(loc.ovfIdx)
		if err != nil {
			return err
		}
		if rec.deleted || rec.rid != rid {
			return ErrNotFound
		}
		rec.deleted = true
		if err := s.writeOvf(loc.ovfIdx, rec, next); err != nil {
			return err
		}
		s.liveOvf--
	} else {
		page, err := s.readPage(loc.pageID)
		if err != nil {
			return err
		}
		if loc.mainSlot >= len(page.slots) {
			return ErrNotFound
		}
		rec := mainSlotDecode(page.slots[loc.mainSlot])
		if rec.deleted || rec.rid != rid {
			return ErrNotFound
		}
		mainSlotEncode(page.slots[loc.mainSlot], true, rec.key, rec.rid, rec.payload)
		if err := s.writePage(loc.pageID, page); err != nil {
			return err
		}
	}
	delete(s.ridIndex, rid)
	s.liveCount--
	s.deadCount++
	return s.maybeReorganizeLocked()
}

func (s *SeqFile) maybeReorganizeLocked() error {
	total := s.liveCount + s.deadCount
	if total == 0 {
		return s.writeMeta()
	}
	if float64(s.deadCount)/float64(total) > s.reorgThreshold {
		return s.reorganizeLocked()
	}
	return s.writeMeta()
}

// Reorganize reconstruye el archivo: junta los registros vivos en orden y
// los reescribe en páginas principales nuevas, vaciando el overflow. Los
// RID lógicos se preservan (van embebidos en cada slot reescrito).
func (s *SeqFile) Reorganize() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reorganizeLocked()
}

func (s *SeqFile) reorganizeLocked() error {
	records, err := s.collectAllLiveLocked()
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
		page := &mainPage{count: uint16(end - start), ovfHead: ovfNone}
		for _, r := range records[start:end] {
			slot := make([]byte, s.mainSlotSize)
			mainSlotEncode(slot, false, r.Key, r.RID, r.Payload)
			page.slots = append(page.slots, slot)
		}
		if err := s.writePage(pid, page); err != nil {
			return err
		}
	}

	if err := s.mainFile.Truncate(s.pageOffset(newNumPages)); err != nil {
		return err
	}
	if err := s.ovfFile.Truncate(0); err != nil {
		return err
	}

	s.numPages = newNumPages
	s.liveCount = int64(len(records))
	s.deadCount = 0
	s.ovfCount = 0
	s.liveOvf = 0

	if err := s.rebuildIndexesLocked(); err != nil {
		return err
	}
	return s.writeMeta()
}

// collectAllLiveLocked recorre cada página (slots + cadena de overflow
// mezclados) en orden ascendente y retorna todos los registros vivos.
func (s *SeqFile) collectAllLiveLocked() ([]RecordInfo, error) {
	var out []RecordInfo
	for pid := int32(0); pid < s.numPages; pid++ {
		recs, err := s.pageLiveLocked(pid)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	return out, nil
}

// pageLiveLocked fusiona los slots principales de una página (ya
// ordenados) con su cadena de overflow (también ordenada), devolviendo
// solo los registros vivos en orden (key, rid).
func (s *SeqFile) pageLiveLocked(pid int32) ([]RecordInfo, error) {
	page, err := s.readPage(pid)
	if err != nil {
		return nil, err
	}
	main := make([]RecordInfo, 0, page.count)
	for _, raw := range page.slots {
		rec := mainSlotDecode(raw)
		if !rec.deleted {
			main = append(main, RecordInfo{Key: rec.key, RID: rec.rid, Payload: rec.payload})
		}
	}
	var ovf []RecordInfo
	cur := page.ovfHead
	for cur != ovfNone {
		rec, next, err := s.readOvf(cur)
		if err != nil {
			return nil, err
		}
		if !rec.deleted {
			ovf = append(ovf, RecordInfo{Key: rec.key, RID: rec.rid, Payload: rec.payload})
		}
		cur = next
	}

	out := make([]RecordInfo, 0, len(main)+len(ovf))
	i, j := 0, 0
	for i < len(main) || j < len(ovf) {
		if j >= len(ovf) || (i < len(main) && lessPair(main[i].Key, main[i].RID, ovf[j].Key, ovf[j].RID)) {
			out = append(out, main[i])
			i++
			continue
		}
		out = append(out, ovf[j])
		j++
	}
	return out, nil
}

// ScanRecords retorna todos los registros vivos, con su RID, en orden de clave.
func (s *SeqFile) ScanRecords() ([]RecordInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.collectAllLiveLocked()
}

// Scan retorna todos los registros vivos (clave/payload), sin RID.
func (s *SeqFile) Scan() ([]KV, error) {
	records, err := s.ScanRecords()
	if err != nil {
		return nil, err
	}
	out := make([]KV, 0, len(records))
	for _, r := range records {
		out = append(out, KV{Key: r.Key, Payload: r.Payload})
	}
	return out, nil
}

// RangeScan retorna los registros vivos con low <= key <= high, ordenados.
func (s *SeqFile) RangeScan(low, high int64) ([]KV, error) {
	all, err := s.Scan()
	if err != nil {
		return nil, err
	}
	var out []KV
	for _, kv := range all {
		if kv.Key >= low && kv.Key <= high {
			out = append(out, kv)
		}
	}
	return out, nil
}

// Stats reporta el tamaño actual del archivo y la proporción de espacio desperdiciado.
func (s *SeqFile) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := s.liveCount + s.deadCount
	wasted := 0.0
	if total > 0 {
		wasted = float64(s.deadCount) / float64(total)
	}
	overflowRatio := 0.0
	if s.liveCount > 0 {
		overflowRatio = float64(s.liveOvf) / float64(s.liveCount)
	}
	return Stats{
		NumPages:      int(s.numPages),
		LiveCount:     s.liveCount,
		DeadCount:     s.deadCount,
		OverflowLive:  s.liveOvf,
		OverflowSlots: s.ovfCount,
		WastedRatio:   wasted,
		OverflowRatio: overflowRatio,
	}
}
