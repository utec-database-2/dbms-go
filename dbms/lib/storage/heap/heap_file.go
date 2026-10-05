// Package heap implementa un heap file de slots de tamaño fijo.
//
// Layout del archivo <filename>_data.dat:
//
//	[FileHeader: 64 bytes][página 0][página 1]...      (páginas de PageSize bytes)
//
// Layout de página:
//
//	[PageHeader: 16 bytes][slot 0][slot 1]...[slot MaxRows-1]
//
// Un slot ocupado guarda [len uint32][tupla serializada]; un slot libre
// guarda [FreeMarker][siguiente slot libre]. El offset de cualquier slot es
// función solo de (page, slot), por lo que los RIDs son estables.
package heap

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/dbms-go/v2/dbms/lib/storage"
)

type Strategy int

const (
	// FreeList: un slot borrado se encadena a la free list de su página y se
	// reutiliza en el siguiente Insert. Los RIDs nunca cambian.
	FreeList Strategy = iota
	// MoveTheLast: al borrar se copia la última fila de la página al hueco, de
	// modo que la página siempre queda compacta. El RID de la fila movida
	// cambia; se notifica mediante Options.OnMove.
	MoveTheLast
)

type Options struct {
	PageSize int      // por defecto storage.DefaultPageSize
	SlotSize int      // por defecto storage.DefaultSlotSize
	Strategy Strategy // por defecto FreeList
	FileID   int32    // valor de RID.File para las filas de este heap

	// OnMove se invoca (con el lock del heap tomado, no llames al heap desde
	// aquí) cuando MoveTheLast cambia el RID de una fila.
	OnMove func(oldRID, newRID storage.RID)
}

type HeapFile struct {
	table    storage.Table
	codec    storage.TupleCodec
	opts     Options
	filename string

	data *os.File
	fh   storage.FileHeader // copia en memoria; se persiste tras cada cambio

	mu sync.Mutex
}

var _ storage.FileStorage[storage.Tuple] = (*HeapFile)(nil)

// New prepara un heap sin tocar el disco; Open crea o abre el archivo.
func New(filename string, table storage.Table, opts Options) (*HeapFile, error) {
	if opts.PageSize == 0 {
		opts.PageSize = storage.DefaultPageSize
	}
	if opts.SlotSize == 0 {
		opts.SlotSize = storage.DefaultSlotSize
	}
	if opts.SlotSize < storage.MinSlotSize ||
		opts.PageSize < storage.PageHeaderSize+opts.SlotSize {
		return nil, fmt.Errorf("%w: PageSize=%d SlotSize=%d", storage.ErrBadOptions, opts.PageSize, opts.SlotSize)
	}
	if opts.Strategy != FreeList && opts.Strategy != MoveTheLast {
		return nil, fmt.Errorf("%w: estrategia %d", storage.ErrBadOptions, opts.Strategy)
	}
	if len(table.Types) == 0 {
		return nil, fmt.Errorf("%w: el esquema no tiene columnas", storage.ErrBadOptions)
	}
	return &HeapFile{
		table:    table,
		codec:    storage.NewTupleCodec(table),
		opts:     opts,
		filename: filename,
	}, nil
}

func (h *HeapFile) path() string { return h.filename + storage.SUFFIX_DATA }

// Open abre el archivo; si no existe (o está vacío) lo inicializa.
func (h *HeapFile) Open() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.data != nil {
		return storage.ErrAlreadyOpen
	}

	f, err := os.OpenFile(h.path(), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}

	if st.Size() == 0 {
		h.fh = storage.FileHeader{
			Magic:    storage.FileMagic,
			Version:  storage.FormatVersion,
			PageSize: int32(h.opts.PageSize),
			SlotSize: int32(h.opts.SlotSize),
			MaxRows:  int32((h.opts.PageSize - storage.PageHeaderSize) / h.opts.SlotSize),
			Strategy: int32(h.opts.Strategy),
		}
		h.data = f
		if err := h.writeFileHeader(); err != nil {
			h.data = nil
			f.Close()
			return err
		}
		return nil
	}

	buf := make([]byte, storage.FileHeaderSize)
	if _, err := f.ReadAt(buf, 0); err != nil {
		f.Close()
		return fmt.Errorf("%w: header ilegible: %v", storage.ErrCorruptFile, err)
	}
	var fh storage.FileHeader
	if err := storage.GetFileHeaderCodec().Unmarshal(&fh, buf); err != nil {
		f.Close()
		return err
	}
	if Strategy(fh.Strategy) != h.opts.Strategy {
		f.Close()
		return fmt.Errorf("%w: el archivo usa la estrategia %d", storage.ErrBadOptions, fh.Strategy)
	}
	want := int64(storage.FileHeaderSize) + int64(fh.Pages)*int64(fh.PageSize)
	if st.Size() < want {
		f.Close()
		return fmt.Errorf("%w: tamaño %d < %d esperado", storage.ErrCorruptFile, st.Size(), want)
	}
	h.fh = fh
	h.opts.PageSize = int(fh.PageSize) // el archivo manda sobre las opciones
	h.opts.SlotSize = int(fh.SlotSize)
	h.data = f
	return nil
}

// Sync fuerza el contenido a disco (fsync).
func (h *HeapFile) Sync() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.data == nil {
		return storage.ErrClosed
	}
	return h.data.Sync()
}

func (h *HeapFile) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.data == nil {
		return storage.ErrClosed
	}
	err := errors.Join(h.data.Sync(), h.data.Close())
	h.data = nil
	return err
}

// Rows devuelve la cantidad de filas vivas.
func (h *HeapFile) Rows() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fh.Rows
}

// Header devuelve una copia del FileHeader (solo lectura).
func (h *HeapFile) Header() storage.FileHeader {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fh
}

// ---------------------------------------------------------------------------
// E/S de bajo nivel (todas se llaman con h.mu tomado)
// ---------------------------------------------------------------------------

func (h *HeapFile) pageOffset(page int32) int64 {
	return int64(storage.FileHeaderSize) + int64(page)*int64(h.fh.PageSize)
}

func (h *HeapFile) slotOffset(slot int32) int {
	return storage.PageHeaderSize + int(slot)*int(h.fh.SlotSize)
}

func (h *HeapFile) writeFileHeader() error {
	buf := make([]byte, storage.FileHeaderSize)
	if err := storage.GetFileHeaderCodec().Marshal(h.fh, buf); err != nil {
		return err
	}
	_, err := h.data.WriteAt(buf, 0)
	return err
}

func (h *HeapFile) readPage(page int32) ([]byte, error) {
	buf := make([]byte, h.fh.PageSize)
	n, err := h.data.ReadAt(buf, h.pageOffset(page))
	if n == len(buf) {
		return buf, nil // ReadAt puede devolver io.EOF junto con n completo
	}
	if err == nil || errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	return nil, fmt.Errorf("%w: página %d: %v", storage.ErrCorruptFile, page, err)
}

func (h *HeapFile) writePage(page int32, buf []byte) error {
	_, err := h.data.WriteAt(buf, h.pageOffset(page))
	return err
}

func (h *HeapFile) newPage() []byte {
	buf := make([]byte, h.fh.PageSize)
	_ = storage.GetPageHeaderCodec().Marshal(storage.PageHeader{FreeHead: storage.NoSlot}, buf)
	return buf
}

func pageHeader(pg []byte) (storage.PageHeader, error) {
	var ph storage.PageHeader
	err := storage.GetPageHeaderCodec().Unmarshal(&ph, pg)
	return ph, err
}

func putPageHeader(pg []byte, ph storage.PageHeader) {
	_ = storage.GetPageHeaderCodec().Marshal(ph, pg)
}
