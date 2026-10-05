package heap

import (
	"encoding/binary"
	"fmt"

	"github.com/dbms-go/v2/dbms/lib/storage"
)

// encode serializa la tupla como [len][payload] y valida que quepa en un slot.
func (h *HeapFile) encode(t storage.Tuple) ([]byte, error) {
	n, err := h.codec.Sizeof(t)
	if err != nil {
		return nil, err
	}
	if storage.SlotLenSize+n > int(h.fh.SlotSize) {
		return nil, fmt.Errorf("%w: necesita %d bytes y el slot admite %d",
			storage.ErrTupleTooLarge, storage.SlotLenSize+n, int(h.fh.SlotSize)-storage.SlotLenSize)
	}
	rec := make([]byte, storage.SlotLenSize+n)
	binary.BigEndian.PutUint32(rec, uint32(n))
	if _, err := h.codec.Marshal(t, rec[storage.SlotLenSize:]); err != nil {
		return nil, err
	}
	return rec, nil
}

func (h *HeapFile) putSlot(pg []byte, slot int32, rec []byte) {
	off := h.slotOffset(slot)
	clear(pg[off : off+int(h.fh.SlotSize)])
	copy(pg[off:], rec)
}

func (h *HeapFile) isFree(pg []byte, slot int32) bool {
	return binary.BigEndian.Uint32(pg[h.slotOffset(slot):]) == storage.FreeMarker
}

func (h *HeapFile) decodeSlot(pg []byte, slot int32) (storage.Tuple, error) {
	off := h.slotOffset(slot)
	n := int(binary.BigEndian.Uint32(pg[off:]))
	if n > int(h.fh.SlotSize)-storage.SlotLenSize {
		return nil, fmt.Errorf("%w: longitud %d inválida en el slot %d", storage.ErrCorruptFile, n, slot)
	}
	return h.codec.Unmarshal(pg[off+storage.SlotLenSize : off+storage.SlotLenSize+n])
}

// locate valida el RID, lee su página y comprueba que el slot esté ocupado.
func (h *HeapFile) locate(rid storage.RID) ([]byte, storage.PageHeader, error) {
	var ph storage.PageHeader
	if h.data == nil {
		return nil, ph, storage.ErrClosed
	}
	if rid.File != h.opts.FileID || rid.Page < 0 || rid.Page >= h.fh.Pages ||
		rid.Slot < 0 || rid.Slot >= h.fh.MaxRows {
		return nil, ph, fmt.Errorf("%w: %+v", storage.ErrInvalidRID, rid)
	}
	pg, err := h.readPage(rid.Page)
	if err != nil {
		return nil, ph, err
	}
	if ph, err = pageHeader(pg); err != nil {
		return nil, ph, err
	}
	if rid.Slot >= ph.HighWater || h.isFree(pg, rid.Slot) {
		return nil, ph, fmt.Errorf("%w: %+v", storage.ErrRecordNotFound, rid)
	}
	return pg, ph, nil
}

// findPage devuelve una página con espacio, o una nueva (aún sin escribir).
func (h *HeapFile) findPage() (int32, []byte, storage.PageHeader, error) {
	for p := h.fh.FreePage; p < h.fh.Pages; p++ {
		pg, err := h.readPage(p)
		if err != nil {
			return 0, nil, storage.PageHeader{}, err
		}
		ph, err := pageHeader(pg)
		if err != nil {
			return 0, nil, storage.PageHeader{}, err
		}
		if ph.Rows < h.fh.MaxRows {
			return p, pg, ph, nil
		}
		if p == h.fh.FreePage {
			h.fh.FreePage++ // pista: todo lo anterior está lleno
		}
	}
	p := h.fh.Pages
	pg := h.newPage()
	ph, _ := pageHeader(pg)
	h.fh.Pages++
	h.fh.TotalSize += int64(h.fh.PageSize)
	return p, pg, ph, nil
}

// Insert guarda la tupla y devuelve su RID.
func (h *HeapFile) Insert(t storage.Tuple) (rid storage.RID, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.data == nil {
		return rid, storage.ErrClosed
	}
	rec, err := h.encode(t)
	if err != nil {
		return rid, err
	}

	snap := h.fh
	defer func() {
		if err != nil {
			h.fh = snap // no dejar el header en memoria adelantado al disco
		}
	}()

	page, pg, ph, err := h.findPage()
	if err != nil {
		return rid, err
	}

	var slot int32
	switch {
	case h.opts.Strategy == FreeList && ph.FreeHead != storage.NoSlot:
		slot = ph.FreeHead
		ph.FreeHead = int32(binary.BigEndian.Uint32(pg[h.slotOffset(slot)+4:]))
	default:
		slot = ph.HighWater // MoveTheLast: HighWater == Rows
		ph.HighWater++
	}
	h.putSlot(pg, slot, rec)
	ph.Rows++
	putPageHeader(pg, ph)
	h.fh.Rows++

	// Primero la página y luego el header: si algo falla entre ambos no se
	// pierden datos, solo queda el contador del header desactualizado.
	if err = h.writePage(page, pg); err != nil {
		return rid, err
	}
	if err = h.writeFileHeader(); err != nil {
		return rid, err
	}
	return storage.RID{File: h.opts.FileID, Page: page, Slot: slot}, nil
}

// Get lee la tupla en rid.
func (h *HeapFile) Get(rid storage.RID) (storage.Tuple, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	pg, _, err := h.locate(rid)
	if err != nil {
		return nil, err
	}
	return h.decodeSlot(pg, rid.Slot)
}

// Update reemplaza la tupla en el mismo slot; el RID no cambia.
func (h *HeapFile) Update(rid storage.RID, t storage.Tuple) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	pg, _, err := h.locate(rid)
	if err != nil {
		return err
	}
	rec, err := h.encode(t)
	if err != nil {
		return err
	}
	h.putSlot(pg, rid.Slot, rec)
	return h.writePage(rid.Page, pg)
}

// Delete elimina la tupla en rid.
func (h *HeapFile) Delete(rid storage.RID) (err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	pg, ph, err := h.locate(rid)
	if err != nil {
		return err
	}

	snap := h.fh
	defer func() {
		if err != nil {
			h.fh = snap
		}
	}()

	var moved *[2]storage.RID
	switch h.opts.Strategy {
	case FreeList:
		off := h.slotOffset(rid.Slot)
		clear(pg[off : off+int(h.fh.SlotSize)])
		binary.BigEndian.PutUint32(pg[off:], storage.FreeMarker)
		binary.BigEndian.PutUint32(pg[off+4:], uint32(ph.FreeHead))
		ph.FreeHead = rid.Slot
	case MoveTheLast:
		last := ph.Rows - 1
		if rid.Slot != last {
			src, dst, size := h.slotOffset(last), h.slotOffset(rid.Slot), int(h.fh.SlotSize)
			copy(pg[dst:dst+size], pg[src:src+size])
			moved = &[2]storage.RID{
				{File: h.opts.FileID, Page: rid.Page, Slot: last},
				{File: h.opts.FileID, Page: rid.Page, Slot: rid.Slot},
			}
		}
		off := h.slotOffset(last)
		clear(pg[off : off+int(h.fh.SlotSize)])
		ph.HighWater = last
	}
	ph.Rows--
	putPageHeader(pg, ph)
	h.fh.Rows--
	if rid.Page < h.fh.FreePage {
		h.fh.FreePage = rid.Page // esta página volvió a tener espacio
	}

	if err = h.writePage(rid.Page, pg); err != nil {
		return err
	}
	if err = h.writeFileHeader(); err != nil {
		return err
	}
	if moved != nil && h.opts.OnMove != nil {
		h.opts.OnMove(moved[0], moved[1])
	}
	return nil
}

// ---------------------------------------------------------------------------
// Scan
// ---------------------------------------------------------------------------

// Iterator recorre todas las filas vivas. Lee una página por vez (con el lock
// del heap) y la itera sin lock, así que ve una instantánea por página.
type Iterator struct {
	h    *HeapFile
	page int32
	slot int32
	pg   []byte
	ph   storage.PageHeader
	rid  storage.RID
	tup  storage.Tuple
	err  error
}

func (h *HeapFile) Scan() *Iterator { return &Iterator{h: h} }

func (it *Iterator) Next() bool {
	for it.err == nil {
		if it.pg == nil {
			if !it.loadPage() {
				return false
			}
		}
		for it.slot < it.ph.HighWater {
			s := it.slot
			it.slot++
			if it.h.isFree(it.pg, s) {
				continue
			}
			t, err := it.h.decodeSlot(it.pg, s)
			if err != nil {
				it.err = err
				return false
			}
			it.rid = storage.RID{File: it.h.opts.FileID, Page: it.page, Slot: s}
			it.tup = t
			return true
		}
		it.pg = nil
		it.page++
	}
	return false
}

func (it *Iterator) loadPage() bool {
	h := it.h
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.data == nil {
		it.err = storage.ErrClosed
		return false
	}
	if it.page >= h.fh.Pages {
		return false
	}
	pg, err := h.readPage(it.page)
	if err != nil {
		it.err = err
		return false
	}
	ph, err := pageHeader(pg)
	if err != nil {
		it.err = err
		return false
	}
	it.pg, it.ph, it.slot = pg, ph, 0
	return true
}

func (it *Iterator) RID() storage.RID     { return it.rid }
func (it *Iterator) Tuple() storage.Tuple { return it.tup }
func (it *Iterator) Err() error           { return it.err }
