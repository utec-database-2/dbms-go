package sequential

import (
	"fmt"
	"sort"

	"github.com/dbms-go/v2/dbms/lib/storage"
)

type where int

const (
	absent   where = iota
	mainLive       // en el principal, vivo
	mainDead       // en el principal con tombstone
	inAux          // en el auxiliar (vivo)
)

type loc struct {
	w   where
	idx int64 // slot del principal (mainLive, mainDead)
	ai  int   // posición en auxMem (inAux)
}

func (s *SequentialFile) key(t storage.Tuple) storage.Tuple { return s.table.KeyOf(t) }

// mainLower devuelve el primer slot del principal cuya clave es >= key
// (los tombstones conservan su clave, así que el orden se mantiene).
func (s *SequentialFile) mainLower(key storage.Tuple) (int64, error) {
	lo, hi := int64(0), s.hdr.Slots
	for lo < hi {
		mid := lo + (hi-lo)/2
		_, t, err := s.readMainSlot(mid)
		if err != nil {
			return 0, err
		}
		if s.table.CompareKeys(s.key(t), key) < 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo, nil
}

func (s *SequentialFile) auxLower(key storage.Tuple) int {
	return sort.Search(len(s.auxMem), func(i int) bool {
		return s.table.CompareKeys(s.key(s.auxMem[i].t), key) >= 0
	})
}

// find localiza una clave y devuelve su tupla si está viva.
func (s *SequentialFile) find(key storage.Tuple) (loc, storage.Tuple, error) {
	idx, err := s.mainLower(key)
	if err != nil {
		return loc{}, nil, err
	}
	if idx < s.hdr.Slots {
		flag, t, err := s.readMainSlot(idx)
		if err != nil {
			return loc{}, nil, err
		}
		if s.table.CompareKeys(s.key(t), key) == 0 {
			if flag == SeqFlagLive {
				return loc{w: mainLive, idx: idx}, t, nil
			}
			return loc{w: mainDead, idx: idx}, nil, nil
		}
	}
	if ai := s.auxLower(key); ai < len(s.auxMem) && s.table.CompareKeys(s.key(s.auxMem[ai].t), key) == 0 {
		return loc{w: inAux, ai: ai}, s.auxMem[ai].t, nil
	}
	return loc{w: absent}, nil, nil
}

func (s *SequentialFile) threshold() int64 {
	return max(int64(s.opts.RebuildMin), s.hdr.Slots/10)
}

func (s *SequentialFile) needsRebuild() bool {
	return s.auxSlots >= s.threshold() || s.hdr.Dead >= s.threshold()
}

// Insert agrega una fila. Devuelve ErrDuplicateKey si la PK ya existe. Si la
// inserción dispara una reconstrucción y esta falla, la fila queda insertada
// y el error se devuelve igualmente.
func (s *SequentialFile) Insert(t storage.Tuple) error {
	if err := s.table.CheckTuple(t); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.main == nil {
		return storage.ErrClosed
	}
	return s.insertLocked(cloneTuple(t))
}

func (s *SequentialFile) insertLocked(t storage.Tuple) error {
	key := s.key(t)
	l, _, err := s.find(key)
	if err != nil {
		return err
	}
	switch l.w {
	case mainLive, inAux:
		return fmt.Errorf("%w: %v", storage.ErrDuplicateKey, key)

	case mainDead: // revivir el slot: mantiene el orden sin reconstruir
		rec, err := s.encodeSlot(t, SeqFlagLive)
		if err != nil {
			return err
		}
		if _, err := s.main.WriteAt(rec, s.slotOff(l.idx)); err != nil {
			return err
		}
		s.hdr.Dead--
		return writeHeader(s.main, s.hdr)

	default: // absent: va al auxiliar
		rec, err := s.encodeSlot(t, SeqFlagLive)
		if err != nil {
			return err
		}
		pos := s.auxSlots
		if _, err := s.aux.WriteAt(rec, s.slotOff(pos)); err != nil {
			return err
		}
		s.auxSlots++
		ai := s.auxLower(key)
		s.auxMem = append(s.auxMem, auxEntry{})
		copy(s.auxMem[ai+1:], s.auxMem[ai:])
		s.auxMem[ai] = auxEntry{t: t, pos: pos}
		if s.needsRebuild() {
			return s.rebuildLocked()
		}
		return nil
	}
}

// Search devuelve la fila con esa PK o ErrRecordNotFound.
func (s *SequentialFile) Search(key storage.Tuple) (storage.Tuple, error) {
	if err := s.table.ValidateKey(key); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.main == nil {
		return nil, storage.ErrClosed
	}
	l, t, err := s.find(key)
	if err != nil {
		return nil, err
	}
	if l.w != mainLive && l.w != inAux {
		return nil, fmt.Errorf("%w: %v", storage.ErrRecordNotFound, key)
	}
	return cloneTuple(t), nil
}

// Delete elimina la fila con esa PK (tombstone en el principal, baja en el
// auxiliar). El espacio se recupera en la siguiente reconstrucción.
func (s *SequentialFile) Delete(key storage.Tuple) error {
	if err := s.table.ValidateKey(key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.main == nil {
		return storage.ErrClosed
	}
	return s.deleteLocked(key)
}

func (s *SequentialFile) deleteLocked(key storage.Tuple) error {
	l, _, err := s.find(key)
	if err != nil {
		return err
	}
	dead := []byte{SeqFlagDead}
	switch l.w {
	case mainLive:
		if _, err := s.main.WriteAt(dead, s.slotOff(l.idx)); err != nil {
			return err
		}
		s.hdr.Dead++
		if err := writeHeader(s.main, s.hdr); err != nil {
			return err
		}
	case inAux:
		if _, err := s.aux.WriteAt(dead, s.slotOff(s.auxMem[l.ai].pos)); err != nil {
			return err
		}
		s.auxMem = append(s.auxMem[:l.ai], s.auxMem[l.ai+1:]...)
	default:
		return fmt.Errorf("%w: %v", storage.ErrRecordNotFound, key)
	}
	if s.needsRebuild() {
		return s.rebuildLocked()
	}
	return nil
}

// Update reemplaza la fila con PK key por t. Si t cambia la PK, equivale a
// borrar e insertar atómicamente respecto de otras llamadas (mismo lock).
func (s *SequentialFile) Update(key storage.Tuple, t storage.Tuple) error {
	if err := s.table.ValidateKey(key); err != nil {
		return err
	}
	if err := s.table.CheckTuple(t); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.main == nil {
		return storage.ErrClosed
	}

	t = cloneTuple(t)
	newKey := s.key(t)
	old, _, err := s.find(key)
	if err != nil {
		return err
	}
	if old.w != mainLive && old.w != inAux {
		return fmt.Errorf("%w: %v", storage.ErrRecordNotFound, key)
	}

	if s.table.CompareKeys(key, newKey) == 0 { // misma PK: en el sitio
		rec, err := s.encodeSlot(t, SeqFlagLive)
		if err != nil {
			return err
		}
		if old.w == mainLive {
			_, err = s.main.WriteAt(rec, s.slotOff(old.idx))
			return err
		}
		if _, err = s.aux.WriteAt(rec, s.slotOff(s.auxMem[old.ai].pos)); err != nil {
			return err
		}
		s.auxMem[old.ai].t = t
		return nil
	}

	// PK distinta: insertar la nueva (comprueba duplicado) y borrar la vieja.
	if err := s.insertLocked(t); err != nil {
		return err
	}
	if err := s.deleteLocked(key); err != nil {
		_ = s.deleteLocked(newKey) // deshacer; si falla, el error original prevalece
		return err
	}
	return nil
}
