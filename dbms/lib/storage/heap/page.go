// Package heap: Heap File en disco (registros en orden de llegada, con
// reutilización de espacio libre de registros eliminados).
package heap

import "encoding/binary"

const (
	pageHeaderSize = 4 // SlotCount (uint16) + FreeSpacePtr (uint16)
	slotEntrySize  = 4 // Offset (uint16) + Length (uint16) por slot
)

// Page: página con slots. Layout: [header][directorio de slots][espacio libre][datos]
type Page struct {
	size int
	data []byte
}

// NewPage crea una página vacía de tamaño size.
func NewPage(size int) *Page {
	p := &Page{size: size, data: make([]byte, size)}
	p.setSlotCount(0)
	p.setFreeSpacePtr(uint16(size))
	return p
}

// LoadPage envuelve bytes leídos de disco como Page.
func LoadPage(data []byte) *Page {
	buf := make([]byte, len(data))
	copy(buf, data)
	return &Page{size: len(data), data: buf}
}

func (p *Page) Bytes() []byte {
	return p.data
}

func (p *Page) slotCount() uint16 {
	return binary.LittleEndian.Uint16(p.data[0:2])
}

func (p *Page) setSlotCount(n uint16) {
	binary.LittleEndian.PutUint16(p.data[0:2], n)
}

func (p *Page) freeSpacePtr() uint16 {
	return binary.LittleEndian.Uint16(p.data[2:4])
}

func (p *Page) setFreeSpacePtr(v uint16) {
	binary.LittleEndian.PutUint16(p.data[2:4], v)
}

func (p *Page) slotOffset(slotID uint16) int {
	return pageHeaderSize + int(slotID)*slotEntrySize
}

func (p *Page) readSlot(slotID uint16) (offset, length uint16) {
	o := p.slotOffset(slotID)
	offset = binary.LittleEndian.Uint16(p.data[o : o+2])
	length = binary.LittleEndian.Uint16(p.data[o+2 : o+4])
	return
}

func (p *Page) writeSlot(slotID uint16, offset, length uint16) {
	o := p.slotOffset(slotID)
	binary.LittleEndian.PutUint16(p.data[o:o+2], offset)
	binary.LittleEndian.PutUint16(p.data[o+2:o+4], length)
}

// directoryEnd: primer byte después del directorio de slots.
func (p *Page) directoryEnd() int {
	return pageHeaderSize + int(p.slotCount())*slotEntrySize
}

// FreeBytes: espacio disponible para un insert nuevo (no cuenta lo que
// Compact podría recuperar de slots eliminados).
func (p *Page) FreeBytes() int {
	free := int(p.freeSpacePtr()) - p.directoryEnd()
	if free < 0 {
		return 0
	}
	return free
}

// findFreeSlot busca un slot eliminado (tombstone) reutilizable.
func (p *Page) findFreeSlot() (uint16, bool) {
	count := p.slotCount()
	for i := uint16(0); i < count; i++ {
		_, length := p.readSlot(i)
		if length == 0 {
			return i, true
		}
	}
	return 0, false
}

// Insert guarda data en la página. ok es false si no hay espacio suficiente.
func (p *Page) Insert(data []byte) (slotID uint16, ok bool) {
	if len(data) == 0 {
		// longitud 0 sería indistinguible de un tombstone
		return 0, false
	}

	reuse, hasReuse := p.findFreeSlot()
	needed := len(data)
	if !hasReuse {
		needed += slotEntrySize
	}
	if needed > p.FreeBytes() {
		return 0, false
	}

	newFree := int(p.freeSpacePtr()) - len(data)
	p.setFreeSpacePtr(uint16(newFree))
	copy(p.data[newFree:newFree+len(data)], data)

	if hasReuse {
		p.writeSlot(reuse, uint16(newFree), uint16(len(data)))
		return reuse, true
	}

	id := p.slotCount()
	p.writeSlot(id, uint16(newFree), uint16(len(data)))
	p.setSlotCount(id + 1)
	return id, true
}

// Get retorna el registro en slotID. ok es false si no existe o fue eliminado.
func (p *Page) Get(slotID uint16) (data []byte, ok bool) {
	if slotID >= p.slotCount() {
		return nil, false
	}
	offset, length := p.readSlot(slotID)
	if length == 0 {
		return nil, false
	}
	out := make([]byte, length)
	copy(out, p.data[offset:int(offset)+int(length)])
	return out, true
}

// Delete marca el slot como tombstone. Retorna false si no existía o ya estaba eliminado.
func (p *Page) Delete(slotID uint16) bool {
	if slotID >= p.slotCount() {
		return false
	}
	offset, length := p.readSlot(slotID)
	if length == 0 {
		return false
	}
	p.writeSlot(slotID, offset, 0)
	return true
}

// Update reemplaza el contenido de slotID in place si entra en el espacio
// ya reservado. Si retorna false, el caller debe reubicar el registro
// (Delete + Insert en otro lado).
func (p *Page) Update(slotID uint16, data []byte) bool {
	if slotID >= p.slotCount() {
		return false
	}
	offset, length := p.readSlot(slotID)
	if length == 0 || len(data) == 0 {
		return false
	}
	if len(data) > int(length) {
		return false
	}
	copy(p.data[offset:int(offset)+len(data)], data)
	p.writeSlot(slotID, offset, uint16(len(data)))
	return true
}

// Compact recupera espacio fragmentado reescribiendo los registros vivos
// de forma contigua.
func (p *Page) Compact() {
	count := p.slotCount()
	type rec struct {
		id     uint16
		data   []byte
		offset uint16
	}
	var recs []rec
	for i := uint16(0); i < count; i++ {
		offset, length := p.readSlot(i)
		if length == 0 {
			continue
		}
		buf := make([]byte, length)
		copy(buf, p.data[offset:int(offset)+int(length)])
		recs = append(recs, rec{id: i, data: buf, offset: offset})
	}

	free := uint16(p.size)
	for _, r := range recs {
		free -= uint16(len(r.data))
		copy(p.data[free:int(free)+len(r.data)], r.data)
		p.writeSlot(r.id, free, uint16(len(r.data)))
	}
	p.setFreeSpacePtr(free)
}

// Scan recorre los registros vivos en orden de slot. Se detiene si fn retorna false.
func (p *Page) Scan(fn func(slotID uint16, data []byte) bool) {
	count := p.slotCount()
	for i := uint16(0); i < count; i++ {
		data, ok := p.Get(i)
		if !ok {
			continue
		}
		if !fn(i, data) {
			return
		}
	}
}
