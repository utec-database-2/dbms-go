// Package sequential: Archivo Secuencial Paginado en disco. Registros
// ordenados por clave int64 en páginas principales de tamaño fijo, con
// una cadena de overflow por página, eliminación lazy y reorganización
// periódica al superar un umbral de espacio desperdiciado.
package sequential

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/dbms-go/v2/dbms/lib/storage"
)

var (
	ErrKeyExists = errors.New("sequential: key already exists")
	ErrNotFound  = errors.New("sequential: key not found")
)

const (
	magic = 0x53455131 // "SEQ1"

	metaSize = 64

	pageHeaderSize = 10 // Count uint16 (2) + OverflowHead int64 (8)
	mainSlotHeader = 11 // tombstone(1) + key(8) + longitud payload(2)
	ovfSlotHeader  = 19 // tombstone(1) + key(8) + next(8) + longitud payload(2)
	ovfNone        int64 = -1

	// ReorgThreshold: fracción por defecto de registros eliminados que
	// dispara una reorganización automática.
	ReorgThreshold = 0.30
)

// KV: par clave/payload vivo, retornado por los scans.
type KV struct {
	Key     int64
	Payload []byte
}

// RecordID: identidad canónica de registro. Se fuerza el RID unificado del
// storage para que ambos motores (heap y secuencial) compartan tipo.
type RecordID = storage.RID

// RecordInfo: clave, RID y payload de un registro vivo, tal y como se entrega
// en un scan con localización física.
type RecordInfo struct {
	Key     int64
	RID     storage.RID
	Payload []byte
}

// Stats: estado actual del archivo, usado en la comparación experimental
// contra el Heap File.
type Stats struct {
	NumPages    int
	LiveCount   int64
	DeadCount   int64
	WastedRatio float64
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

	numPages    int32
	liveCount   int64
	deadCount   int64
	ovfCount    int64
	ovfFreeHead int64

	// pageMinKey[i]: clave del slot 0 de la página i (válida aunque ese
	// slot ya esté eliminado), usada para rutear por binary search sin ir a disco.
	pageMinKey []int64

	reorgThreshold float64
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
		mainFile:       mf,
		ovfFile:        of,
		pageCapacity:   pageCapacity,
		payloadSize:    payloadSize,
		mainSlotSize:   mainSlotHeader + payloadSize,
		ovfSlotSize:    ovfSlotHeader + payloadSize,
		pageSize:       0,
		numPages:       0,
		ovfFreeHead:    ovfNone,
		reorgThreshold: ReorgThreshold,
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
// el header de metadata escrito por Create.
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

	s := &SeqFile{mainFile: mf, ovfFile: of, reorgThreshold: ReorgThreshold}
	if err := s.readMeta(); err != nil {
		mf.Close()
		of.Close()
		return nil, err
	}
	s.mainSlotSize = mainSlotHeader + s.payloadSize
	s.ovfSlotSize = ovfSlotHeader + s.payloadSize
	s.pageSize = pageHeaderSize + s.pageCapacity*s.mainSlotSize

	if err := s.rebuildPageMinKeys(); err != nil {
		mf.Close()
		of.Close()
		return nil, err
	}
	return s, nil
}

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

func (s *SeqFile) writeMeta() error {
	buf := make([]byte, metaSize)
	binary.LittleEndian.PutUint32(buf[0:4], magic)
	binary.LittleEndian.PutUint32(buf[4:8], uint32(s.pageCapacity))
	binary.LittleEndian.PutUint32(buf[8:12], uint32(s.payloadSize))
	binary.LittleEndian.PutUint32(buf[12:16], uint32(s.numPages))
	binary.LittleEndian.PutUint64(buf[16:24], uint64(s.liveCount))
	binary.LittleEndian.PutUint64(buf[24:32], uint64(s.deadCount))
	binary.LittleEndian.PutUint64(buf[32:40], uint64(s.ovfCount))
	binary.LittleEndian.PutUint64(buf[40:48], uint64(s.ovfFreeHead))
	_, err := s.mainFile.WriteAt(buf, 0)
	return err
}

func (s *SeqFile) readMeta() error {
	buf := make([]byte, metaSize)
	if _, err := s.mainFile.ReadAt(buf, 0); err != nil {
		return err
	}
	if binary.LittleEndian.Uint32(buf[0:4]) != magic {
		return fmt.Errorf("sequential: bad magic in main file header")
	}
	s.pageCapacity = int(binary.LittleEndian.Uint32(buf[4:8]))
	s.payloadSize = int(binary.LittleEndian.Uint32(buf[8:12]))
	s.numPages = int32(binary.LittleEndian.Uint32(buf[12:16]))
	s.liveCount = int64(binary.LittleEndian.Uint64(buf[16:24]))
	s.deadCount = int64(binary.LittleEndian.Uint64(buf[24:32]))
	s.ovfCount = int64(binary.LittleEndian.Uint64(buf[32:40]))
	s.ovfFreeHead = int64(binary.LittleEndian.Uint64(buf[40:48]))
	return nil
}

// ---- I/O de páginas principales ----

type mainPage struct {
	count   uint16
	ovfHead int64
	slots   [][]byte // mainSlotSize bytes c/u, len == count
}

func (s *SeqFile) pageOffset(pageID int32) int64 {
	return metaSize + int64(pageID)*int64(s.pageSize)
}

func (s *SeqFile) readPage(pageID int32) (*mainPage, error) {
	buf := make([]byte, s.pageSize)
	if _, err := s.mainFile.ReadAt(buf, s.pageOffset(pageID)); err != nil {
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

func mainSlotEncode(buf []byte, deleted bool, key int64, payload []byte) {
	if deleted {
		buf[0] = 1
	} else {
		buf[0] = 0
	}
	binary.LittleEndian.PutUint64(buf[1:9], uint64(key))
	binary.LittleEndian.PutUint16(buf[9:11], uint16(len(payload)))
	copy(buf[11:], payload)
}

func mainSlotDecode(buf []byte) (deleted bool, key int64, payload []byte) {
	deleted = buf[0] == 1
	key = int64(binary.LittleEndian.Uint64(buf[1:9]))
	length := binary.LittleEndian.Uint16(buf[9:11])
	payload = make([]byte, length)
	copy(payload, buf[11:11+int(length)])
	return
}

// ---- I/O de la cadena de overflow ----

func (s *SeqFile) ovfOffset(idx int64) int64 {
	return idx * int64(s.ovfSlotSize)
}

func (s *SeqFile) readOvf(idx int64) (deleted bool, key int64, next int64, payload []byte, err error) {
	buf := make([]byte, s.ovfSlotSize)
	if _, err = s.ovfFile.ReadAt(buf, s.ovfOffset(idx)); err != nil {
		return
	}
	deleted = buf[0] == 1
	key = int64(binary.LittleEndian.Uint64(buf[1:9]))
	next = int64(binary.LittleEndian.Uint64(buf[9:17]))
	length := binary.LittleEndian.Uint16(buf[17:19])
	payload = make([]byte, length)
	copy(payload, buf[19:19+int(length)])
	return
}

func (s *SeqFile) writeOvf(idx int64, deleted bool, key int64, next int64, payload []byte) error {
	buf := make([]byte, s.ovfSlotSize)
	if deleted {
		buf[0] = 1
	}
	binary.LittleEndian.PutUint64(buf[1:9], uint64(key))
	binary.LittleEndian.PutUint64(buf[9:17], uint64(next))
	binary.LittleEndian.PutUint16(buf[17:19], uint16(len(payload)))
	copy(buf[19:], payload)
	_, err := s.ovfFile.WriteAt(buf, s.ovfOffset(idx))
	return err
}

// allocOvf retorna un índice para un nuevo nodo de overflow, reutilizando
// la free list si hay slots eliminados disponibles.
func (s *SeqFile) allocOvf() (int64, error) {
	if s.ovfFreeHead != ovfNone {
		idx := s.ovfFreeHead
		_, _, next, _, err := s.readOvf(idx)
		if err != nil {
			return 0, err
		}
		s.ovfFreeHead = next
		return idx, nil
	}
	idx := s.ovfCount
	s.ovfCount++
	return idx, nil
}

func (s *SeqFile) freeOvf(idx int64) error {
	return s.writeOvf(idx, true, 0, s.ovfFreeHead, make([]byte, s.payloadSize))
}

func validatePayloadSize(payload []byte, size int) error {
	if len(payload) > size {
		return fmt.Errorf("sequential: payload of %d bytes exceeds configured size %d", len(payload), size)
	}
	return nil
}

// rebuildPageMinKeys reconstruye el cache de ruteo desde disco.
func (s *SeqFile) rebuildPageMinKeys() error {
	s.pageMinKey = make([]int64, s.numPages)
	for i := int32(0); i < s.numPages; i++ {
		page, err := s.readPage(i)
		if err != nil {
			return err
		}
		if page.count == 0 {
			s.pageMinKey[i] = 0
			continue
		}
		_, key, _ := mainSlotDecode(page.slots[0])
		s.pageMinKey[i] = key
	}
	return nil
}

// routePage retorna el índice de página al que debe rutearse key.
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

// Insert agrega un par clave/payload, manteniendo el archivo ordenado por clave.
func (s *SeqFile) Insert(key int64, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _, err := s.insertLocked(key, payload)
	return err
}

// InsertRecord agrega un par clave/payload y devuelve el RID lógico asignado.
//
// El RID es (página, ordinal): la página principal y la posición del registro
// dentro del orden de claves vivo de esa página (main + overflow fusionados).
// Es una vista válida del estado en el momento de retornarlo: cualquier mutación
// posterior dentro de la misma página (insert o delete que desplace el orden)
// invalida RIDs previos. Un índice agrupado debe re-reconstruirse tras esas
// mutaciones (Rebuild/ScanRecords) o usar el RID inmediatamente.
func (s *SeqFile) InsertRecord(key int64, payload []byte) (storage.RID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pid, ordinal, err := s.insertLocked(key, payload)
	if err != nil {
		return storage.RID{}, err
	}
	return storage.RID{PageID: uint32(pid), SlotID: uint16(ordinal)}, nil
}

// insertLocked asume s.mu tomado; devuelve (página, ordinal) al insertar.
func (s *SeqFile) insertLocked(key int64, payload []byte) (int32, int, error) {
	if err := validatePayloadSize(payload, s.payloadSize); err != nil {
		return 0, 0, err
	}

	if s.numPages == 0 {
		page := &mainPage{count: 1, ovfHead: ovfNone}
		slot := make([]byte, s.mainSlotSize)
		mainSlotEncode(slot, false, key, payload)
		page.slots = append(page.slots, slot)
		if err := s.writePage(0, page); err != nil {
			return 0, 0, err
		}
		s.numPages = 1
		s.pageMinKey = []int64{key}
		s.liveCount++
		if err := s.writeMeta(); err != nil {
			return 0, 0, err
		}
		return 0, 0, nil
	}

	pid := s.routePage(key)
	page, err := s.readPage(pid)
	if err != nil {
		return 0, 0, err
	}

	// rechaza duplicados: revisa slots principales y luego la cadena de overflow
	for _, slot := range page.slots {
		deleted, k, _ := mainSlotDecode(slot)
		if !deleted && k == key {
			return 0, 0, ErrKeyExists
		}
	}
	cur := page.ovfHead
	for cur != ovfNone {
		deleted, k, next, _, err := s.readOvf(cur)
		if err != nil {
			return 0, 0, err
		}
		if !deleted && k == key {
			return 0, 0, ErrKeyExists
		}
		cur = next
	}

	// si hay espacio, inserta directo en la página principal, en la
	// posición que mantiene los slots ordenados por clave
	if int(page.count) < s.pageCapacity {
		slot := make([]byte, s.mainSlotSize)
		mainSlotEncode(slot, false, key, payload)

		insertAt := len(page.slots)
		for i, sl := range page.slots {
			_, k, _ := mainSlotDecode(sl)
			if key < k {
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
			return 0, 0, err
		}
		// solo la página 0 puede recibir una clave menor a su mínimo actual
		// (el ruteo cae en la página 0 para claves fuera de rango)
		if pid == 0 && insertAt == 0 {
			s.pageMinKey[0] = key
		}
		s.liveCount++
		if err := s.writeMeta(); err != nil {
			return 0, 0, err
		}
	} else {
		// si no hay espacio, inserta en la cadena de overflow ordenada de la página
		idx, err := s.allocOvf()
		if err != nil {
			return 0, 0, err
		}

		var prevIdx int64 = ovfNone
		cur = page.ovfHead
		for cur != ovfNone {
			_, k, next, _, err := s.readOvf(cur)
			if err != nil {
				return 0, 0, err
			}
			if k > key {
				break
			}
			prevIdx = cur
			cur = next
		}

		if err := s.writeOvf(idx, false, key, cur, payload); err != nil {
			return 0, 0, err
		}
		if prevIdx == ovfNone {
			page.ovfHead = idx
			if err := s.writePage(pid, page); err != nil {
				return 0, 0, err
			}
		} else {
			// Preserva el flag "deleted" del nodo previo: si estaba eliminado,
			// re-escribirlo con deleted=false lo resucitaría con su payload viejo.
			prevDeleted, pk, _, pp, err := s.readOvf(prevIdx)
			if err != nil {
				return 0, 0, err
			}
			if err := s.writeOvf(prevIdx, prevDeleted, pk, idx, pp); err != nil {
				return 0, 0, err
			}
		}

		s.liveCount++
		if err := s.writeMeta(); err != nil {
			return 0, 0, err
		}
	}

	ordinal, err := s.ordinalOfLocked(pid, key)
	if err != nil {
		return 0, 0, err
	}
	return pid, ordinal, nil
}

func (s *SeqFile) Search(key int64) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.searchLocked(key)
}

func (s *SeqFile) searchLocked(key int64) ([]byte, bool, error) {
	if s.numPages == 0 {
		return nil, false, nil
	}
	pid := s.routePage(key)
	page, err := s.readPage(pid)
	if err != nil {
		return nil, false, err
	}
	for _, slot := range page.slots {
		deleted, k, payload := mainSlotDecode(slot)
		if !deleted && k == key {
			return payload, true, nil
		}
	}
	cur := page.ovfHead
	for cur != ovfNone {
		deleted, k, next, payload, err := s.readOvf(cur)
		if err != nil {
			return nil, false, err
		}
		// un delete+insert de la misma clave puede dejar un nodo eliminado
		// seguido de uno vivo con la misma clave más adelante en la cadena;
		// no cortar la búsqueda en el primer match, seguir hasta uno vivo
		if !deleted && k == key {
			return payload, true, nil
		}
		cur = next
	}
	return nil, false, nil
}

// locEntry: registro vivo resuelto dentro de una página, con su ubicación
// física (slot principal o nodo de overflow) para poder leerlo o borrarlo.
type locEntry struct {
	key      int64
	payload  []byte
	mainSlot int   // índice en page.slots; -1 si vive en overflow
	ovfIdx   int64 // índice del nodo de overflow; ovfNone si vive en main
	next     int64 // enlace next del nodo de overflow (no usado si vive en main)
}

// pageLiveEntriesLocked devuelve, en orden de clave, los registros vivos de la
// página pid (main + overflow fusionados), cada uno con su localización física.
func (s *SeqFile) pageLiveEntriesLocked(pid int32) ([]locEntry, error) {
	page, err := s.readPage(pid)
	if err != nil {
		return nil, err
	}

	type chainNode struct {
		idx     int64
		key     int64
		next    int64
		payload []byte
	}
	var chain []chainNode
	cur := page.ovfHead
	for cur != ovfNone {
		deleted, k, next, payload, err := s.readOvf(cur)
		if err != nil {
			return nil, err
		}
		if !deleted {
			chain = append(chain, chainNode{idx: cur, key: k, next: next, payload: payload})
		}
		cur = next
	}

	var out []locEntry
	i, j := 0, 0
	for i < len(page.slots) || j < len(chain) {
		if i < len(page.slots) {
			deleted, k, payload := mainSlotDecode(page.slots[i])
			if deleted {
				i++
				continue
			}
if j < len(chain) && chain[j].key < k {
			out = append(out, locEntry{key: chain[j].key, payload: chain[j].payload, mainSlot: -1, ovfIdx: chain[j].idx, next: chain[j].next})
			j++
			continue
		}
			out = append(out, locEntry{key: k, payload: payload, mainSlot: i, ovfIdx: ovfNone})
			i++
			continue
		}
		out = append(out, locEntry{key: chain[j].key, payload: chain[j].payload, mainSlot: -1, ovfIdx: chain[j].idx, next: chain[j].next})
		j++
	}
	return out, nil
}

// ordinalOfLocked devuelve la posición de key en el orden de claves vivas de la
// página pid (índice usado como SlotID del RID). Asume que key acaba de insertarse.
func (s *SeqFile) ordinalOfLocked(pid int32, key int64) (int, error) {
	entries, err := s.pageLiveEntriesLocked(pid)
	if err != nil {
		return 0, err
	}
	for i, e := range entries {
		if e.key == key {
			return i, nil
		}
	}
	return 0, ErrNotFound
}

// ScanRecords recorre el archivo en orden de clave y entrega cada registro vivo
// con su RID lógico (página, ordinal).
func (s *SeqFile) ScanRecords() ([]RecordInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []RecordInfo
	for pid := int32(0); pid < s.numPages; pid++ {
		entries, err := s.pageLiveEntriesLocked(pid)
		if err != nil {
			return nil, err
		}
		for ordinal, e := range entries {
			out = append(out, RecordInfo{
				Key:     e.key,
				RID:     storage.RID{PageID: uint32(pid), SlotID: uint16(ordinal)},
				Payload: e.payload,
			})
		}
	}
	return out, nil
}

// Read devuelve el payload del registro identificado por rid.
func (s *SeqFile) Read(rid storage.RID) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rid.PageID >= uint32(s.numPages) {
		return nil, ErrNotFound
	}
	entries, err := s.pageLiveEntriesLocked(int32(rid.PageID))
	if err != nil {
		return nil, err
	}
	if int(rid.SlotID) >= len(entries) {
		return nil, ErrNotFound
	}
	return entries[rid.SlotID].payload, nil
}

// DeleteRID elimina el registro identificado por rid (tombstone lazy).
//
// No dispara reorganización automática: reescribir páginas invalidaría los RIDs
// ya publicados al árbol. Si el archivo acumula muchos tombstones, conviene
// Reorganize() + Rebuild() del índice agrupado.
//
// El rid debe resolver a un registro aún vivo en el momento de la llamada;
// si el archivo mutó desde que se obtuvo el RID, puede apuntar a otra clave.
func (s *SeqFile) DeleteRID(rid storage.RID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rid.PageID >= uint32(s.numPages) {
		return ErrNotFound
	}
	entries, err := s.pageLiveEntriesLocked(int32(rid.PageID))
	if err != nil {
		return err
	}
	if int(rid.SlotID) >= len(entries) {
		return ErrNotFound
	}
	rec := entries[rid.SlotID]
	if rec.ovfIdx != ovfNone {
		if err := s.writeOvf(rec.ovfIdx, true, rec.key, rec.next, rec.payload); err != nil {
			return err
		}
	} else {
		page, err := s.readPage(int32(rid.PageID))
		if err != nil {
			return err
		}
		mainSlotEncode(page.slots[rec.mainSlot], true, rec.key, rec.payload)
		if err := s.writePage(int32(rid.PageID), page); err != nil {
			return err
		}
	}
	s.liveCount--
	s.deadCount++
	return s.writeMeta()
}

// Delete elimina la clave de forma lazy (tombstone). Si el espacio
// desperdiciado supera el umbral, dispara una reorganización automática.
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
		deleted, k, payload := mainSlotDecode(slot)
		if !deleted && k == key {
			mainSlotEncode(page.slots[i], true, key, payload)
			if err := s.writePage(pid, page); err != nil {
				return false, err
			}
			s.liveCount--
			s.deadCount++
			return true, s.maybeReorganizeLocked()
		}
	}
	cur := page.ovfHead
	for cur != ovfNone {
		deleted, k, next, payload, err := s.readOvf(cur)
		if err != nil {
			return false, err
		}
		// mismo motivo que en searchLocked: no detenerse en un nodo ya
		// eliminado con la misma clave, puede haber uno vivo más adelante
		if !deleted && k == key {
			if err := s.writeOvf(cur, true, key, next, payload); err != nil {
				return false, err
			}
			s.liveCount--
			s.deadCount++
			return true, s.maybeReorganizeLocked()
		}
		cur = next
	}
	return false, nil
}

func (s *SeqFile) maybeReorganizeLocked() error {
	total := s.liveCount + s.deadCount
	if total == 0 {
		return s.writeMeta()
	}
	ratio := float64(s.deadCount) / float64(total)
	if ratio > s.reorgThreshold {
		return s.reorganizeLocked()
	}
	return s.writeMeta()
}

// Reorganize reconstruye el archivo: junta los registros vivos en orden
// y los reescribe en páginas principales nuevas, vaciando el overflow.
func (s *SeqFile) Reorganize() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reorganizeLocked()
}

func (s *SeqFile) reorganizeLocked() error {
	records, err := s.collectLiveLocked()
	if err != nil {
		return err
	}

	newNumPages := int32(0)
	if len(records) > 0 {
		newNumPages = int32((len(records) + s.pageCapacity - 1) / s.pageCapacity)
	}

	for i := int32(0); i < newNumPages; i++ {
		start := int(i) * s.pageCapacity
		end := start + s.pageCapacity
		if end > len(records) {
			end = len(records)
		}
		page := &mainPage{count: uint16(end - start), ovfHead: ovfNone}
		for _, r := range records[start:end] {
			slot := make([]byte, s.mainSlotSize)
			mainSlotEncode(slot, false, r.Key, r.Payload)
			page.slots = append(page.slots, slot)
		}
		if err := s.writePage(i, page); err != nil {
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
	s.ovfFreeHead = ovfNone

	if err := s.rebuildPageMinKeys(); err != nil {
		return err
	}
	return s.writeMeta()
}

// collectLiveLocked recorre cada página (slots + cadena de overflow
// mezclados) en orden ascendente y retorna todos los registros vivos.
func (s *SeqFile) collectLiveLocked() ([]KV, error) {
	var out []KV
	for pid := int32(0); pid < s.numPages; pid++ {
		page, err := s.readPage(pid)
		if err != nil {
			return nil, err
		}

		type chainNode struct {
			key     int64
			payload []byte
		}
		var chain []chainNode
		cur := page.ovfHead
		for cur != ovfNone {
			deleted, k, next, payload, err := s.readOvf(cur)
			if err != nil {
				return nil, err
			}
			if !deleted {
				chain = append(chain, chainNode{key: k, payload: payload})
			}
			cur = next
		}

		i, j := 0, 0
		for i < len(page.slots) || j < len(chain) {
			if i < len(page.slots) {
				deleted, k, payload := mainSlotDecode(page.slots[i])
				if deleted {
					i++
					continue
				}
				if j < len(chain) && chain[j].key < k {
					out = append(out, KV{Key: chain[j].key, Payload: chain[j].payload})
					j++
					continue
				}
				out = append(out, KV{Key: k, Payload: payload})
				i++
				continue
			}
			out = append(out, KV{Key: chain[j].key, Payload: chain[j].payload})
			j++
		}
	}
	return out, nil
}

// Scan retorna todos los registros vivos, ordenados por clave.
func (s *SeqFile) Scan() ([]KV, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.collectLiveLocked()
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

// Stats retorna el tamaño actual del archivo y la proporción de espacio desperdiciado.
func (s *SeqFile) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := s.liveCount + s.deadCount
	ratio := 0.0
	if total > 0 {
		ratio = float64(s.deadCount) / float64(total)
	}
	return Stats{
		NumPages:    int(s.numPages),
		LiveCount:   s.liveCount,
		DeadCount:   s.deadCount,
		WastedRatio: ratio,
	}
}
