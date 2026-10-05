package sequential

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/dbms-go/v2/dbms/lib/storage"
)

// tupleSource es un flujo de tuplas ordenadas por PK.
type tupleSource interface {
	next() (storage.Tuple, bool, error)
}

type sliceSource struct {
	ts []storage.Tuple
	i  int
}

func (s *sliceSource) next() (storage.Tuple, bool, error) {
	if s.i >= len(s.ts) {
		return nil, false, nil
	}
	s.i++
	return s.ts[s.i-1], true, nil
}

// mainSource recorre las filas vivas del principal actual.
type mainSource struct {
	s   *SequentialFile
	pos int64
	buf []storage.Tuple
	i   int
}

func (m *mainSource) next() (storage.Tuple, bool, error) {
	s := m.s
	for m.i >= len(m.buf) {
		if m.pos >= s.hdr.Slots {
			return nil, false, nil
		}
		n := min(int64(iterChunk), s.hdr.Slots-m.pos)
		b := make([]byte, int(n)*s.slotSize)
		if _, err := s.main.ReadAt(b, s.slotOff(m.pos)); err != nil {
			return nil, false, fmt.Errorf("%w: %v", storage.ErrCorruptFile, err)
		}
		m.buf, m.i = m.buf[:0], 0
		for j := 0; j < int(n); j++ {
			flag, t, err := s.decodeSlot(b[j*s.slotSize : (j+1)*s.slotSize])
			if err != nil {
				return nil, false, err
			}
			if flag == SeqFlagLive {
				m.buf = append(m.buf, t)
			}
		}
		m.pos += n
	}
	m.i++
	return m.buf[m.i-1], true, nil
}

// Rebuild absorbe el auxiliar y descarta los tombstones, reescribiendo el
// principal. Invalida los iteradores abiertos.
func (s *SequentialFile) Rebuild() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.main == nil {
		return storage.ErrClosed
	}
	return s.rebuildLocked()
}

func (s *SequentialFile) rebuildLocked() error {
	aux := make([]storage.Tuple, len(s.auxMem))
	for i, e := range s.auxMem {
		aux[i] = e.t
	}
	return s.replaceMain(&mainSource{s: s}, &sliceSource{ts: aux})
}

// BulkLoad carga un conjunto de filas (en cualquier orden) en un archivo vacío.
func (s *SequentialFile) BulkLoad(rows []storage.Tuple) error {
	ts := make([]storage.Tuple, len(rows))
	for i, r := range rows {
		if err := s.table.CheckTuple(r); err != nil {
			return fmt.Errorf("fila %d: %w", i, err)
		}
		ts[i] = cloneTuple(r)
	}
	sort.SliceStable(ts, func(a, b int) bool {
		return s.table.CompareKeys(s.key(ts[a]), s.key(ts[b])) < 0
	})
	for i := 1; i < len(ts); i++ {
		if s.table.CompareKeys(s.key(ts[i-1]), s.key(ts[i])) == 0 {
			return fmt.Errorf("%w: %v", storage.ErrDuplicateKey, s.key(ts[i]))
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.main == nil {
		return storage.ErrClosed
	}
	if s.hdr.Slots != 0 || s.auxSlots != 0 {
		return fmt.Errorf("%w: BulkLoad requiere un archivo vacío", storage.ErrBadOptions)
	}
	return s.replaceMain(&sliceSource{ts: ts}, &sliceSource{})
}

// replaceMain mezcla a y b (disjuntos y ordenados) en un archivo temporal, lo
// sincroniza y lo renombra sobre el principal. Antes del rename un fallo deja
// el estado intacto; después, el epoch nuevo hace que un auxiliar viejo se
// descarte al reabrir.
func (s *SequentialFile) replaceMain(a, b tupleSource) error {
	path := s.mainPath()
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	abort := func(err error) error {
		f.Close()
		os.Remove(tmp)
		return err
	}

	nh := s.hdr
	nh.Epoch++
	nh.Slots, nh.Dead = 0, 0

	bw := bufio.NewWriterSize(f, 64<<10)
	if _, err := bw.Write(make([]byte, SeqHeaderSize)); err != nil {
		return abort(err)
	}
	emit := func(t storage.Tuple) error {
		rec, err := s.encodeSlot(t, SeqFlagLive)
		if err != nil {
			return err
		}
		if _, err := bw.Write(rec); err != nil {
			return err
		}
		nh.Slots++
		return nil
	}

	ta, oka, err := a.next()
	if err != nil {
		return abort(err)
	}
	tb, okb, err := b.next()
	if err != nil {
		return abort(err)
	}
	for oka || okb {
		var pick storage.Tuple
		takeA := oka
		if oka && okb {
			switch c := s.table.CompareKeys(s.key(ta), s.key(tb)); {
			case c == 0:
				return abort(fmt.Errorf("%w: clave repetida entre principal y auxiliar", storage.ErrCorruptFile))
			case c > 0:
				takeA = false
			}
		}
		if takeA {
			pick = ta
			ta, oka, err = a.next()
		} else {
			pick = tb
			tb, okb, err = b.next()
		}
		if err != nil {
			return abort(err)
		}
		if err := emit(pick); err != nil {
			return abort(err)
		}
	}

	if err := bw.Flush(); err != nil {
		return abort(err)
	}
	if err := writeHeader(f, nh); err != nil {
		return abort(err)
	}
	if err := f.Sync(); err != nil {
		return abort(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	syncDir(filepath.Dir(path))

	newMain, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		// El principal ya fue reemplazado pero no se puede reabrir: cerrar.
		s.main.Close()
		s.aux.Close()
		s.main, s.aux, s.auxMem = nil, nil, nil
		return fmt.Errorf("reabriendo el principal reconstruido: %w", err)
	}
	s.main.Close()
	s.main, s.hdr = newMain, nh
	s.gen++
	return s.resetAux()
}
