package heap

import (
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"sync"
)

const DefaultPageSize = 4096

var (
	ErrRecordTooLarge = errors.New("heap: record too large for a page")
	ErrEmptyRecord    = errors.New("heap: empty records are not supported")
	ErrNotFound       = errors.New("heap: record not found")
	ErrCorruptPage    = errors.New("heap: corrupt page")
)

// RecordID identifica físicamente un registro del Heap File.
// Page.Compact puede mover los bytes internos, pero conserva SlotID;
// por eso este RID permanece estable mientras el registro no sea reubicado
// explícitamente por Update.
type RecordID struct {
	PageID uint32
	SlotID uint16
}

// RID es el nombre corto usado por la capa de índices.
type RID = RecordID

func (r RecordID) String() string { return fmt.Sprintf("(%d,%d)", r.PageID, r.SlotID) }

// Uint64 permite almacenar un RID como valor compacto en una estructura de índice.
func (r RecordID) Uint64() uint64 {
	return (uint64(r.PageID) << 16) | uint64(r.SlotID)
}

// RecordIDFromUint64 reconstruye un RID serializado con Uint64.
func RecordIDFromUint64(v uint64) (RecordID, error) {
	if v>>48 != 0 {
		return RecordID{}, fmt.Errorf("heap: invalid encoded RID %d", v)
	}
	return RecordID{PageID: uint32(v >> 16), SlotID: uint16(v)}, nil
}

// Stats expone métricas útiles para la comparación experimental del proyecto.
type Stats struct {
	NumPages       uint32
	LiveRecords    int64
	FreeBytes      int64
	AllocatedBytes int64
}

// HeapFile: colección de páginas en disco con registros de largo variable
// en orden de llegada y reutilización de espacio libre.
type HeapFile struct {
	mu       sync.Mutex
	file     *os.File
	pageSize int
	numPages uint32

	// freeSpace: pageID -> bytes libres actuales de la página.
	freeSpace map[uint32]int
	closed    bool
}

func validatePageSize(pageSize int) error {
	if pageSize <= pageHeaderSize+slotEntrySize {
		return fmt.Errorf("heap: page size too small: %d", pageSize)
	}
	// Page usa uint16 para offsets y puntero de espacio libre.
	if pageSize > math.MaxUint16 {
		return fmt.Errorf("heap: page size %d exceeds uint16 layout limit %d", pageSize, math.MaxUint16)
	}
	return nil
}

// Create crea un Heap File nuevo (trunca path si ya existía).
func Create(path string, pageSize int) (*HeapFile, error) {
	if err := validatePageSize(pageSize); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	return &HeapFile{
		file:      f,
		pageSize:  pageSize,
		freeSpace: make(map[uint32]int),
	}, nil
}

// Open reabre un Heap File y reconstruye el índice de espacio libre.
// Además valida la estructura de todas las páginas antes de aceptar el archivo.
func Open(path string, pageSize int) (*HeapFile, error) {
	if err := validatePageSize(pageSize); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if info.Size()%int64(pageSize) != 0 {
		_ = f.Close()
		return nil, fmt.Errorf("heap: file size %d is not a multiple of page size %d", info.Size(), pageSize)
	}

	h := &HeapFile{
		file:      f,
		pageSize:  pageSize,
		numPages:  uint32(info.Size() / int64(pageSize)),
		freeSpace: make(map[uint32]int),
	}
	for pid := uint32(0); pid < h.numPages; pid++ {
		page, err := h.readPageLocked(pid)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		h.freeSpace[pid] = page.FreeBytes()
	}
	return h, nil
}

func (h *HeapFile) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	if err := h.file.Sync(); err != nil {
		return err
	}
	if err := h.file.Close(); err != nil {
		return err
	}
	h.closed = true
	return nil
}

func (h *HeapFile) Sync() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return os.ErrClosed
	}
	return h.file.Sync()
}

func (h *HeapFile) PageSize() int { return h.pageSize }

func (h *HeapFile) NumPages() uint32 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.numPages
}

func (h *HeapFile) validateData(data []byte) error {
	if len(data) == 0 {
		return ErrEmptyRecord
	}
	maxPayload := h.pageSize - pageHeaderSize - slotEntrySize
	if len(data) > maxPayload {
		return ErrRecordTooLarge
	}
	return nil
}

func (h *HeapFile) validatePageLocked(pid uint32, p *Page) error {
	if p == nil || p.size != h.pageSize || len(p.data) != h.pageSize {
		return fmt.Errorf("%w: page %d has invalid size", ErrCorruptPage, pid)
	}
	count := p.slotCount()
	dirEnd := pageHeaderSize + int(count)*slotEntrySize
	if dirEnd > h.pageSize {
		return fmt.Errorf("%w: page %d slot directory exceeds page size", ErrCorruptPage, pid)
	}
	freePtr := int(p.freeSpacePtr())
	if freePtr < dirEnd || freePtr > h.pageSize {
		return fmt.Errorf("%w: page %d invalid free-space pointer %d", ErrCorruptPage, pid, freePtr)
	}

	type interval struct{ start, end int }
	intervals := make([]interval, 0, count)
	for sid := uint16(0); sid < count; sid++ {
		offset, length := p.readSlot(sid)
		if length == 0 { // tombstone
			continue
		}
		start := int(offset)
		end := start + int(length)
		if start < freePtr || start < dirEnd || end > h.pageSize || start >= end {
			return fmt.Errorf("%w: page %d slot %d invalid interval [%d,%d)", ErrCorruptPage, pid, sid, start, end)
		}
		intervals = append(intervals, interval{start: start, end: end})
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].start < intervals[j].start })
	for i := 1; i < len(intervals); i++ {
		if intervals[i].start < intervals[i-1].end {
			return fmt.Errorf("%w: page %d has overlapping records", ErrCorruptPage, pid)
		}
	}
	return nil
}

func (h *HeapFile) readPageLocked(pid uint32) (*Page, error) {
	if pid >= h.numPages {
		return nil, ErrNotFound
	}
	buf := make([]byte, h.pageSize)
	off := int64(pid) * int64(h.pageSize)
	n, err := h.file.ReadAt(buf, off)
	if err != nil {
		return nil, err
	}
	if n != len(buf) {
		return nil, fmt.Errorf("%w: short read on page %d", ErrCorruptPage, pid)
	}
	p := LoadPage(buf)
	if err := h.validatePageLocked(pid, p); err != nil {
		return nil, err
	}
	return p, nil
}

func (h *HeapFile) writePageLocked(pid uint32, p *Page) error {
	if p == nil || p.size != h.pageSize || len(p.data) != h.pageSize {
		return fmt.Errorf("heap: invalid page passed to writePage")
	}
	if err := h.validatePageLocked(pid, p); err != nil {
		return err
	}
	off := int64(pid) * int64(h.pageSize)
	_, err := h.file.WriteAt(p.Bytes(), off)
	return err
}

// Insert guarda data usando first-fit determinístico: revisa páginas en orden
// físico y crea una página nueva solo cuando ninguna existente sirve.
func (h *HeapFile) Insert(data []byte) (RecordID, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return RecordID{}, os.ErrClosed
	}
	if err := h.validateData(data); err != nil {
		return RecordID{}, err
	}
	return h.insertLocked(data, nil)
}

func (h *HeapFile) insertLocked(data []byte, skipPage *uint32) (RecordID, error) {
	for pid := uint32(0); pid < h.numPages; pid++ {
		if skipPage != nil && pid == *skipPage {
			continue
		}
		free := h.freeSpace[pid]
		if free < len(data) {
			continue
		}
		page, err := h.readPageLocked(pid)
		if err != nil {
			return RecordID{}, err
		}
		slot, ok := page.Insert(data)
		if !ok {
			// Defensivo ante fragmentación: compacta y reintenta.
			page.Compact()
			slot, ok = page.Insert(data)
		}
		if !ok {
			h.freeSpace[pid] = page.FreeBytes()
			continue
		}
		if err := h.writePageLocked(pid, page); err != nil {
			return RecordID{}, err
		}
		h.freeSpace[pid] = page.FreeBytes()
		return RecordID{PageID: pid, SlotID: slot}, nil
	}

	page := NewPage(h.pageSize)
	slot, ok := page.Insert(data)
	if !ok {
		return RecordID{}, ErrRecordTooLarge
	}
	pid := h.numPages
	// validatePageLocked acepta pid aunque aún sea la página nueva.
	if err := h.writePageLocked(pid, page); err != nil {
		return RecordID{}, err
	}
	h.numPages++
	h.freeSpace[pid] = page.FreeBytes()
	return RecordID{PageID: pid, SlotID: slot}, nil
}

func (h *HeapFile) Read(rid RecordID) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, os.ErrClosed
	}
	if rid.PageID >= h.numPages {
		return nil, ErrNotFound
	}
	page, err := h.readPageLocked(rid.PageID)
	if err != nil {
		return nil, err
	}
	data, ok := page.Get(rid.SlotID)
	if !ok {
		return nil, ErrNotFound
	}
	return data, nil
}

// Delete elimina un registro y compacta su página inmediatamente, dejando el
// SlotID como tombstone reutilizable. Los demás RIDs de la página no cambian.
func (h *HeapFile) Delete(rid RecordID) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return os.ErrClosed
	}
	return h.deleteLocked(rid)
}

func (h *HeapFile) deleteLocked(rid RecordID) error {
	if rid.PageID >= h.numPages {
		return ErrNotFound
	}
	page, err := h.readPageLocked(rid.PageID)
	if err != nil {
		return err
	}
	if !page.Delete(rid.SlotID) {
		return ErrNotFound
	}
	page.Compact()
	if err := h.writePageLocked(rid.PageID, page); err != nil {
		return err
	}
	h.freeSpace[rid.PageID] = page.FreeBytes()
	return nil
}

// Update reemplaza un registro. Si cabe en el espacio actual conserva el RID.
// Si necesita reubicarse, primero garantiza una nueva copia y solo después
// elimina la anterior, evitando perder el registro si la inserción falla.
func (h *HeapFile) Update(rid RecordID, data []byte) (RecordID, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return RecordID{}, os.ErrClosed
	}
	// Corrige el bug original: un payload inválido se rechaza ANTES de borrar.
	if err := h.validateData(data); err != nil {
		return RecordID{}, err
	}
	if rid.PageID >= h.numPages {
		return RecordID{}, ErrNotFound
	}

	page, err := h.readPageLocked(rid.PageID)
	if err != nil {
		return RecordID{}, err
	}
	if _, ok := page.Get(rid.SlotID); !ok {
		return RecordID{}, ErrNotFound
	}

	if page.Update(rid.SlotID, data) {
		if err := h.writePageLocked(rid.PageID, page); err != nil {
			return RecordID{}, err
		}
		h.freeSpace[rid.PageID] = page.FreeBytes()
		return rid, nil
	}

	// Intentar crecer dentro de la misma página liberando primero el registro
	// sobre una copia en memoria. Solo se escribe si toda la operación cabe.
	candidate := LoadPage(page.Bytes())
	if candidate.Delete(rid.SlotID) {
		candidate.Compact()
		newSlot, ok := candidate.Insert(data)
		if ok {
			if err := h.writePageLocked(rid.PageID, candidate); err != nil {
				return RecordID{}, err
			}
			h.freeSpace[rid.PageID] = candidate.FreeBytes()
			return RecordID{PageID: rid.PageID, SlotID: newSlot}, nil
		}
	}

	// No cabe ni liberando el registro original: insertar en otra página antes
	// de tocar la copia vieja. Se excluye la página original porque ya sabemos
	// que ni liberando ese registro puede contener el nuevo payload.
	newRID, err := h.insertLocked(data, &rid.PageID)
	if err != nil {
		return RecordID{}, err
	}
	if err := h.deleteLocked(rid); err != nil {
		// Rollback de mejor esfuerzo: preferimos conservar la copia vieja si
		// falló su eliminación y retirar la nueva para no crear duplicados.
		rollbackErr := h.deleteLocked(newRID)
		if rollbackErr != nil {
			return RecordID{}, errors.Join(err, fmt.Errorf("heap: rollback failed: %w", rollbackErr))
		}
		return RecordID{}, err
	}
	return newRID, nil
}

// Scan entrega un snapshot consistente de los registros vivos en orden físico.
// El callback se ejecuta sin el mutex tomado, por lo que puede invocar otras
// operaciones del HeapFile sin provocar deadlock.
func (h *HeapFile) Scan(fn func(RecordID, []byte) bool) error {
	if fn == nil {
		return nil
	}
	type item struct {
		rid  RecordID
		data []byte
	}

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return os.ErrClosed
	}
	items := make([]item, 0)
	for pid := uint32(0); pid < h.numPages; pid++ {
		page, err := h.readPageLocked(pid)
		if err != nil {
			h.mu.Unlock()
			return err
		}
		page.Scan(func(slotID uint16, data []byte) bool {
			items = append(items, item{
				rid:  RecordID{PageID: pid, SlotID: slotID},
				data: append([]byte(nil), data...),
			})
			return true
		})
	}
	h.mu.Unlock()

	for _, it := range items {
		if !fn(it.rid, it.data) {
			break
		}
	}
	return nil
}

// Stats calcula métricas del archivo a partir de un snapshot de páginas.
func (h *HeapFile) Stats() (Stats, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return Stats{}, os.ErrClosed
	}
	var live int64
	var free int64
	for pid := uint32(0); pid < h.numPages; pid++ {
		p, err := h.readPageLocked(pid)
		if err != nil {
			return Stats{}, err
		}
		free += int64(p.FreeBytes())
		p.Scan(func(_ uint16, _ []byte) bool {
			live++
			return true
		})
	}
	return Stats{
		NumPages:       h.numPages,
		LiveRecords:    live,
		FreeBytes:      free,
		AllocatedBytes: int64(h.numPages) * int64(h.pageSize),
	}, nil
}

// Validate vuelve a validar todas las páginas del archivo.
func (h *HeapFile) Validate() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return os.ErrClosed
	}
	for pid := uint32(0); pid < h.numPages; pid++ {
		if _, err := h.readPageLocked(pid); err != nil {
			return err
		}
	}
	return nil
}
