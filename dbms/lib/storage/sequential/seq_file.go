package sequential

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/dbms-go/v2/dbms/lib/storage"
)

type Options struct {
	// RebuildMin es el mínimo de slots auxiliares (o tombstones) que dispara
	// una reconstrucción. El umbral real es max(RebuildMin, slotsPrincipal/10).
	RebuildMin int // por defecto 128
}

type auxEntry struct {
	t   storage.Tuple
	pos int64 // slot dentro del archivo auxiliar
}

type SequentialFile struct {
	table    storage.Table
	codec    storage.TupleCodec
	opts     Options
	filename string
	slotSize int

	main *os.File
	aux  *os.File
	hdr  SeqHeader // cabecera del principal (copia en memoria)

	auxSlots int64      // slots físicos del auxiliar (vivos + borrados)
	auxMem   []auxEntry // slots vivos del auxiliar, ordenados por clave
	gen      uint64     // se incrementa en cada reconstrucción

	mu sync.RWMutex
}

var _ storage.KeyedStorage = (*SequentialFile)(nil)

// New prepara el archivo sin tocar el disco; Open lo crea o abre.
func New(filename string, table storage.Table, opts Options) (*SequentialFile, error) {
	if err := table.Validate(); err != nil {
		return nil, err
	}
	if opts.RebuildMin == 0 {
		opts.RebuildMin = 128
	}
	if opts.RebuildMin < 1 {
		return nil, fmt.Errorf("%w: RebuildMin=%d", storage.ErrBadOptions, opts.RebuildMin)
	}
	return &SequentialFile{
		table:    table,
		codec:    storage.NewTupleCodec(table),
		opts:     opts,
		filename: filename,
		slotSize: SeqSlotHead + table.MaxPayload(),
	}, nil
}

func (s *SequentialFile) mainPath() string { return s.filename + SUFFIX_SEQ }
func (s *SequentialFile) auxPath() string  { return s.filename + SUFFIX_AUX }

// Open abre el archivo; si no existe lo inicializa.
func (s *SequentialFile) Open() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.main != nil {
		return storage.ErrAlreadyOpen
	}
	_ = os.Remove(s.mainPath() + ".tmp") // resto de una reconstrucción interrumpida

	f, err := os.OpenFile(s.mainPath(), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	hash := s.table.SchemaHash()

	if st.Size() == 0 {
		s.hdr = SeqHeader{
			Magic: SeqMagic, Version: SeqVersion,
			SlotSize: int32(s.slotSize), SchemaHash: hash, Epoch: 1,
		}
		if err := writeHeader(f, s.hdr); err != nil {
			f.Close()
			return err
		}
	} else {
		buf := make([]byte, SeqHeaderSize)
		if _, err := f.ReadAt(buf, 0); err != nil {
			f.Close()
			return fmt.Errorf("%w: header ilegible: %v", storage.ErrCorruptFile, err)
		}
		var h SeqHeader
		if err := GetSeqHeaderCodec().Unmarshal(&h, buf, SeqMagic); err != nil {
			f.Close()
			return err
		}
		if h.SchemaHash != hash || int(h.SlotSize) != s.slotSize {
			f.Close()
			return fmt.Errorf("%w: el archivo fue creado con otro esquema", storage.ErrSchemaMismatch)
		}
		if want := int64(SeqHeaderSize) + h.Slots*int64(h.SlotSize); st.Size() < want {
			f.Close()
			return fmt.Errorf("%w: tamaño %d < %d esperado", storage.ErrCorruptFile, st.Size(), want)
		}
		s.hdr = h
	}
	s.main = f

	if err := s.loadAux(hash); err != nil {
		s.main.Close()
		s.main = nil
		return err
	}
	return nil
}

// loadAux abre el auxiliar y carga sus filas vivas ordenadas. Un auxiliar de
// otro epoch ya fue absorbido por el principal (crash entre el rename y el
// truncado) y se descarta.
func (s *SequentialFile) loadAux(hash uint32) error {
	f, err := os.OpenFile(s.auxPath(), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	s.aux = f
	s.auxSlots, s.auxMem = 0, nil

	fail := func(err error) error {
		f.Close()
		s.aux = nil
		return err
	}
	st, err := f.Stat()
	if err != nil {
		return fail(err)
	}

	fresh := false
	if st.Size() < SeqHeaderSize {
		fresh = true
	} else {
		buf := make([]byte, SeqHeaderSize)
		if _, err := f.ReadAt(buf, 0); err != nil {
			return fail(err)
		}
		var h SeqHeader
		if err := GetSeqHeaderCodec().Unmarshal(&h, buf, SeqAuxMagic); err != nil {
			return fail(err)
		}
		switch {
		case h.SchemaHash != hash || int(h.SlotSize) != s.slotSize:
			return fail(fmt.Errorf("%w: el auxiliar fue creado con otro esquema", storage.ErrSchemaMismatch))
		case h.Epoch != s.hdr.Epoch:
			fresh = true
		}
	}
	if fresh {
		if err := s.resetAux(); err != nil {
			return fail(err)
		}
		return nil
	}

	n := (st.Size() - SeqHeaderSize) / int64(s.slotSize)
	if want := SeqHeaderSize + n*int64(s.slotSize); st.Size() != want {
		if err := f.Truncate(want); err != nil { // append interrumpido a medias
			return fail(err)
		}
	}
	const chunk = 256
	buf := make([]byte, chunk*s.slotSize)
	for i := int64(0); i < n; {
		c := min(chunk, n-i)
		b := buf[:int(c)*s.slotSize]
		if _, err := f.ReadAt(b, s.slotOff(i)); err != nil {
			return fail(fmt.Errorf("%w: auxiliar: %v", storage.ErrCorruptFile, err))
		}
		for j := int64(0); j < c; j++ {
			flag, t, err := s.decodeSlot(b[int(j)*s.slotSize : int(j+1)*s.slotSize])
			if err != nil {
				return fail(err)
			}
			if flag == SeqFlagLive {
				s.auxMem = append(s.auxMem, auxEntry{t: t, pos: i + j})
			}
		}
		i += c
	}
	sort.Slice(s.auxMem, func(a, b int) bool {
		return s.table.CompareKeys(s.table.KeyOf(s.auxMem[a].t), s.table.KeyOf(s.auxMem[b].t)) < 0
	})
	for i := 1; i < len(s.auxMem); i++ {
		if s.table.CompareKeys(s.table.KeyOf(s.auxMem[i-1].t), s.table.KeyOf(s.auxMem[i].t)) == 0 {
			return fail(fmt.Errorf("%w: clave repetida en el auxiliar", storage.ErrCorruptFile))
		}
	}
	s.auxSlots = n
	return nil
}

// resetAux vacía el auxiliar y lo marca con el epoch actual del principal.
func (s *SequentialFile) resetAux() error {
	if err := s.aux.Truncate(0); err != nil {
		return err
	}
	h := SeqHeader{
		Magic: SeqAuxMagic, Version: SeqVersion,
		SlotSize: int32(s.slotSize), SchemaHash: s.table.SchemaHash(), Epoch: s.hdr.Epoch,
	}
	if err := writeHeader(s.aux, h); err != nil {
		return err
	}
	s.auxSlots, s.auxMem = 0, nil
	return nil
}

// Sync fuerza ambos archivos a disco (fsync).
func (s *SequentialFile) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.main == nil {
		return storage.ErrClosed
	}
	return errors.Join(s.main.Sync(), s.aux.Sync())
}

func (s *SequentialFile) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.main == nil {
		return storage.ErrClosed
	}
	err := errors.Join(s.main.Sync(), s.aux.Sync(), s.main.Close(), s.aux.Close())
	s.main, s.aux, s.auxMem = nil, nil, nil
	return err
}

// Rows devuelve la cantidad de filas vivas.
func (s *SequentialFile) Rows() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hdr.Slots - s.hdr.Dead + int64(len(s.auxMem))
}

type Stats struct {
	MainSlots, MainDead, AuxSlots, AuxLive int64
	Epoch                                  uint64
}

func (s *SequentialFile) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Stats{s.hdr.Slots, s.hdr.Dead, s.auxSlots, int64(len(s.auxMem)), s.hdr.Epoch}
}

// ---------------------------------------------------------------------------
// Slots y E/S de bajo nivel (se llaman con s.mu tomado)
// ---------------------------------------------------------------------------

func (s *SequentialFile) slotOff(i int64) int64 {
	return int64(SeqHeaderSize) + i*int64(s.slotSize)
}

func writeHeader(f *os.File, h SeqHeader) error {
	buf := make([]byte, SeqHeaderSize)
	if err := GetSeqHeaderCodec().Marshal(h, buf); err != nil {
		return err
	}
	_, err := f.WriteAt(buf, 0)
	return err
}

func (s *SequentialFile) encodeSlot(t storage.Tuple, flag byte) ([]byte, error) {
	n, err := s.codec.Sizeof(t)
	if err != nil {
		return nil, err
	}
	if SeqSlotHead+n > s.slotSize {
		return nil, fmt.Errorf("%w: %d bytes en un slot de %d", storage.ErrTupleTooLarge, SeqSlotHead+n, s.slotSize)
	}
	rec := make([]byte, s.slotSize)
	rec[0] = flag
	binary.BigEndian.PutUint32(rec[1:], uint32(n))
	if _, err := s.codec.Marshal(t, rec[SeqSlotHead:SeqSlotHead+n]); err != nil {
		return nil, err
	}
	return rec, nil
}

func (s *SequentialFile) decodeSlot(b []byte) (byte, storage.Tuple, error) {
	n := int(binary.BigEndian.Uint32(b[1:]))
	if n > s.slotSize-SeqSlotHead || (b[0] != SeqFlagLive && b[0] != SeqFlagDead) {
		return 0, nil, fmt.Errorf("%w: slot ilegible", storage.ErrCorruptFile)
	}
	t, err := s.codec.Unmarshal(b[SeqSlotHead : SeqSlotHead+n])
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %v", storage.ErrCorruptFile, err)
	}
	return b[0], t, nil
}

func (s *SequentialFile) readMainSlot(i int64) (byte, storage.Tuple, error) {
	b := make([]byte, s.slotSize)
	if _, err := s.main.ReadAt(b, s.slotOff(i)); err != nil {
		return 0, nil, fmt.Errorf("%w: slot %d: %v", storage.ErrCorruptFile, i, err)
	}
	return s.decodeSlot(b)
}

func cloneTuple(t storage.Tuple) storage.Tuple {
	c := make(storage.Tuple, len(t))
	for i, v := range t {
		if b, ok := v.([]byte); ok {
			c[i] = append([]byte{}, b...)
		} else {
			c[i] = v
		}
	}
	return c
}

func syncDir(path string) {
	if d, err := os.Open(path); err == nil {
		_ = d.Sync()
		d.Close()
	}
}
