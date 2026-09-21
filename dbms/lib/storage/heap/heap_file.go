package heap

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/dbms-go/v2/dbms/lib/storage"
)

const DefaultPageSize = 4096

var (
	ErrRecordTooLarge = errors.New("heap: record too large for a page")
	ErrNotFound       = errors.New("heap: record not found")
)

// RecordID: ubicación física de un registro (página, slot).
type RecordID = storage.RID

// HeapFile: colección de páginas en disco con registros de largo variable
// en orden de llegada, reutilizando el espacio liberado por eliminaciones.
type HeapFile struct {
	mu       sync.Mutex
	file     *os.File
	pageSize int
	numPages uint32

	// freeSpace: pageID -> bytes libres, índice en memoria para elegir
	// página al insertar (first-fit) en vez de siempre agregar una nueva.
	freeSpace map[uint32]int
}

// Create crea un heap file nuevo en path (trunca si ya existe).
func Create(path string, pageSize int) (*HeapFile, error) {
	if pageSize <= pageHeaderSize+slotEntrySize {
		return nil, fmt.Errorf("heap: page size too small: %d", pageSize)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	return &HeapFile{
		file:      f,
		pageSize:  pageSize,
		numPages:  0,
		freeSpace: make(map[uint32]int),
	}, nil
}

// Open reabre un heap file existente, reconstruyendo el índice de espacio
// libre a partir de cada página.
func Open(path string, pageSize int) (*HeapFile, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if info.Size()%int64(pageSize) != 0 {
		f.Close()
		return nil, fmt.Errorf("heap: file size %d is not a multiple of page size %d", info.Size(), pageSize)
	}

	h := &HeapFile{
		file:      f,
		pageSize:  pageSize,
		numPages:  uint32(info.Size() / int64(pageSize)),
		freeSpace: make(map[uint32]int),
	}
	for pid := uint32(0); pid < h.numPages; pid++ {
		page, err := h.readPage(pid)
		if err != nil {
			f.Close()
			return nil, err
		}
		h.freeSpace[pid] = page.FreeBytes()
	}
	return h, nil
}

func (h *HeapFile) Close() error {
	return h.file.Close()
}

func (h *HeapFile) PageSize() int { return h.pageSize }

// Stats resume el estado del HeapFile: número de páginas, registros vivos
// (slots con datos), tombstones (slots con length 0) y bytes libres.
type Stats struct {
	NumPages  uint32
	LiveCount int
	DeadCount int
	FreeBytes int
}

func (h *HeapFile) Stats() (Stats, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	st := Stats{NumPages: h.numPages}
	for pid := uint32(0); pid < h.numPages; pid++ {
		page, err := h.readPage(pid)
		if err != nil {
			return st, err
		}
		live := 0
		page.Scan(func(uint16, []byte) bool {
			live++
			return true
		})
		st.LiveCount += live
		st.DeadCount += int(page.slotCount()) - live
		st.FreeBytes += page.FreeBytes()
	}
	return st, nil
}

func (h *HeapFile) NumPages() uint32 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.numPages
}

func (h *HeapFile) readPage(pid uint32) (*Page, error) {
	buf := make([]byte, h.pageSize)
	off := int64(pid) * int64(h.pageSize)
	if _, err := h.file.ReadAt(buf, off); err != nil {
		return nil, err
	}
	return LoadPage(buf), nil
}

func (h *HeapFile) writePage(pid uint32, p *Page) error {
	off := int64(pid) * int64(h.pageSize)
	_, err := h.file.WriteAt(p.Bytes(), off)
	return err
}

// Insert guarda data reutilizando espacio libre de una página existente
// (first-fit) antes de crear una nueva. Retorna el RecordID asignado.
func (h *HeapFile) Insert(data []byte) (RecordID, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	maxPayload := h.pageSize - pageHeaderSize - slotEntrySize
	if len(data) == 0 || len(data) > maxPayload {
		return RecordID{}, ErrRecordTooLarge
	}
	// cota inferior optimista (reutilizar slot cuesta solo len(data));
	// Page.Insert resuelve si de verdad entra
	for pid, free := range h.freeSpace {
		if free < len(data) {
			continue
		}
		page, err := h.readPage(pid)
		if err != nil {
			return RecordID{}, err
		}
		slot, ok := page.Insert(data)
		if !ok {
			// la estimación fue optimista; se intenta compactar antes de descartar la página
			page.Compact()
			slot, ok = page.Insert(data)
		}
		if ok {
			if err := h.writePage(pid, page); err != nil {
				return RecordID{}, err
			}
			h.freeSpace[pid] = page.FreeBytes()
			return RecordID{PageID: pid, SlotID: slot}, nil
		}
		h.freeSpace[pid] = page.FreeBytes()
	}

	page := NewPage(h.pageSize)
	slot, ok := page.Insert(data)
	if !ok {
		return RecordID{}, ErrRecordTooLarge
	}
	pid := h.numPages
	if err := h.writePage(pid, page); err != nil {
		return RecordID{}, err
	}
	h.numPages++
	h.freeSpace[pid] = page.FreeBytes()
	return RecordID{PageID: pid, SlotID: slot}, nil
}

func (h *HeapFile) Read(rid RecordID) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if rid.PageID >= h.numPages {
		return nil, ErrNotFound
	}
	page, err := h.readPage(rid.PageID)
	if err != nil {
		return nil, err
	}
	data, ok := page.Get(rid.SlotID)
	if !ok {
		return nil, ErrNotFound
	}
	return data, nil
}

// Delete marca el registro como eliminado y libera su espacio para reuso.
func (h *HeapFile) Delete(rid RecordID) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if rid.PageID >= h.numPages {
		return ErrNotFound
	}
	page, err := h.readPage(rid.PageID)
	if err != nil {
		return err
	}
	if !page.Delete(rid.SlotID) {
		return ErrNotFound
	}
	page.Compact() // recupera el espacio de inmediato para poder reutilizarlo
	if err := h.writePage(rid.PageID, page); err != nil {
		return err
	}
	h.freeSpace[rid.PageID] = page.FreeBytes()
	return nil
}

// Update reemplaza el registro en rid. Si ya no entra en su slot original,
// se reubica (insert del nuevo + delete del viejo) y se retorna el nuevo
// RecordID. La reubicación es write-ahead: primero se persiste la copia nueva
// y solo después se descarta la vieja, para no perder el dato si el insert
// falla (p. ej. ErrRecordTooLarge).
func (h *HeapFile) Update(rid RecordID, data []byte) (RecordID, error) {
	h.mu.Lock()
	// Valida el payload ANTES de tocar el registro original: si es inválido
	// y solo lo descubriéramos al reubicar (delete + insert), ya habríamos
	// borrado el dato viejo sin poder escribir el nuevo.
	maxPayload := h.pageSize - pageHeaderSize - slotEntrySize
	if len(data) == 0 || len(data) > maxPayload {
		h.mu.Unlock()
		return RecordID{}, ErrRecordTooLarge
	}
	if rid.PageID >= h.numPages {
		h.mu.Unlock()
		return RecordID{}, ErrNotFound
	}
	page, err := h.readPage(rid.PageID)
	if err != nil {
		h.mu.Unlock()
		return RecordID{}, err
	}
	if page.Update(rid.SlotID, data) {
		if err := h.writePage(rid.PageID, page); err != nil {
			h.mu.Unlock()
			return RecordID{}, err
		}
		h.freeSpace[rid.PageID] = page.FreeBytes()
		h.mu.Unlock()
		return rid, nil
	}
	h.mu.Unlock()

	newRid, err := h.Insert(data)
	if err != nil {
		return RecordID{}, err
	}
	if err := h.Delete(rid); err != nil {
		if rollbackErr := h.Delete(newRid); rollbackErr != nil {
			return RecordID{}, fmt.Errorf("heap: update reubicado pero falló borrar el original (%v) y el rollback (%v)", err, rollbackErr)
		}
		return RecordID{}, err
	}
	return newRid, nil
}

// Scan recorre los registros vivos en orden físico (página, slot).
func (h *HeapFile) Scan(fn func(RecordID, []byte) bool) error {
	h.mu.Lock()
	numPages := h.numPages
	h.mu.Unlock()

	for pid := uint32(0); pid < numPages; pid++ {
		h.mu.Lock()
		page, err := h.readPage(pid)
		h.mu.Unlock()
		if err != nil {
			return err
		}
		stop := false
		page.Scan(func(slotID uint16, data []byte) bool {
			if !fn(RecordID{PageID: pid, SlotID: slotID}, data) {
				stop = true
				return false
			}
			return true
		})
		if stop {
			return nil
		}
	}
	return nil
}
