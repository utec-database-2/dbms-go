package sequential

import (
	"fmt"

	"github.com/dbms-go/v2/dbms/lib/storage"
)

const iterChunk = 64

// Iterator recorre las filas en orden de PK mezclando el principal y el
// auxiliar. Es débilmente consistente: lee el principal por bloques y toma una
// instantánea del auxiliar al crearse, de modo que modificaciones concurrentes
// pueden verse o no. Si ocurre una reconstrucción, Next devuelve false y Err
// devuelve ErrIteratorInvalidated.
type Iterator struct {
	s   *SequentialFile
	gen uint64
	hi  storage.Tuple // nil: sin cota superior (inclusiva)

	mainPos  int64
	mainBuf  []storage.Tuple
	mainI    int
	mainDone bool

	aux  []storage.Tuple
	auxI int

	cur  storage.Tuple
	done bool
	err  error
}

// Range itera las filas con lo <= PK <= hi, ambas inclusivas. Una cota nil
// significa sin límite. Las cotas son tuplas con un valor por columna de la PK.
func (s *SequentialFile) Range(lo, hi storage.Tuple) *Iterator {
	it := &Iterator{s: s, hi: hi}
	for _, k := range []storage.Tuple{lo, hi} {
		if k != nil {
			if err := s.table.ValidateKey(k); err != nil {
				it.err = err
				return it
			}
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.main == nil {
		it.err = storage.ErrClosed
		return it
	}
	it.gen = s.gen
	ai := 0
	if lo != nil {
		pos, err := s.mainLower(lo)
		if err != nil {
			it.err = err
			return it
		}
		it.mainPos = pos
		ai = s.auxLower(lo)
	}
	it.mainDone = it.mainPos >= s.hdr.Slots
	for _, e := range s.auxMem[ai:] {
		it.aux = append(it.aux, cloneTuple(e.t))
	}
	return it
}

// Scan itera toda la tabla en orden de PK.
func (s *SequentialFile) Scan() *Iterator { return s.Range(nil, nil) }

// fill carga el siguiente bloque del principal con filas vivas.
func (it *Iterator) fill() {
	s := it.s
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.main == nil {
		it.err = storage.ErrClosed
		return
	}
	if s.gen != it.gen {
		it.err = storage.ErrIteratorInvalidated
		return
	}
	it.mainBuf, it.mainI = it.mainBuf[:0], 0
	for len(it.mainBuf) == 0 && !it.mainDone {
		n := min(int64(iterChunk), s.hdr.Slots-it.mainPos)
		b := make([]byte, int(n)*s.slotSize)
		if _, err := s.main.ReadAt(b, s.slotOff(it.mainPos)); err != nil {
			it.err = fmt.Errorf("%w: %v", storage.ErrCorruptFile, err)
			return
		}
		for j := 0; j < int(n); j++ {
			flag, t, err := s.decodeSlot(b[j*s.slotSize : (j+1)*s.slotSize])
			if err != nil {
				it.err = err
				return
			}
			if flag == SeqFlagLive {
				it.mainBuf = append(it.mainBuf, t)
			}
		}
		it.mainPos += n
		it.mainDone = it.mainPos >= s.hdr.Slots
	}
}

func (it *Iterator) Next() bool {
	if it.err != nil || it.done {
		return false
	}
	tb := it.s.table
	if it.mainI >= len(it.mainBuf) && !it.mainDone {
		if it.fill(); it.err != nil {
			return false
		}
	}
	var m, a storage.Tuple
	if it.mainI < len(it.mainBuf) {
		m = it.mainBuf[it.mainI]
	}
	if it.auxI < len(it.aux) {
		a = it.aux[it.auxI]
	}
	var pick storage.Tuple
	switch {
	case m == nil && a == nil:
		it.done = true
		return false
	case a == nil || (m != nil && tb.CompareKeys(tb.KeyOf(m), tb.KeyOf(a)) < 0):
		pick = m
		it.mainI++
	default:
		pick = a
		it.auxI++
	}
	if it.hi != nil && tb.CompareKeys(tb.KeyOf(pick), it.hi) > 0 {
		it.done = true
		return false
	}
	it.cur = pick
	return true
}

func (it *Iterator) Tuple() storage.Tuple { return it.cur }
func (it *Iterator) Err() error           { return it.err }
