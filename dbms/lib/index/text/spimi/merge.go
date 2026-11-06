package spimi

import (
	"container/heap"
	"fmt"
	"os"

	"github.com/dbms-go/v2/dbms/lib/index/text/textindex"
)

// mergeItem es la entrada actual de un cursor, esperando su turno en el heap.
type mergeItem struct {
	term     string
	postings []textindex.Posting
	src      int // índice del bloque de origen: menor = más antiguo
}

// mergeHeap ordena por término y, en empate, por bloque más antiguo. Ese
// desempate es el que deja las listas concatenadas en orden de docID.
type mergeHeap []mergeItem

func (h mergeHeap) Len() int { return len(h) }
func (h mergeHeap) Less(i, j int) bool {
	if h[i].term != h[j].term {
		return h[i].term < h[j].term
	}
	return h[i].src < h[j].src
}
func (h mergeHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *mergeHeap) Push(x any)   { *h = append(*h, x.(mergeItem)) }
func (h *mergeHeap) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	*h = old[:n-1]
	return it
}

// mergeStream mezcla los cursores (cada uno ya ordenado por término) y llama
// a emit una vez por término, en orden, con la lista completa de posteos.
// Los cursores deben haberse generado con docID crecientes entre bloques
// (bloque 0 < bloque 1 < ...); si no, devuelve ErrDocOrder.
func mergeStream(cursors []*blockCursor, emit func(term string, ps []textindex.Posting) error) error {
	h := make(mergeHeap, 0, len(cursors))

	advance := func(src int) error {
		term, ps, ok, err := cursors[src].next()
		if err != nil {
			return err
		}
		if ok {
			heap.Push(&h, mergeItem{term: term, postings: ps, src: src})
		}
		return nil
	}
	for i := range cursors {
		if err := advance(i); err != nil {
			return err
		}
	}

	for h.Len() > 0 {
		first := heap.Pop(&h).(mergeItem)
		if err := advance(first.src); err != nil {
			return err
		}
		merged := first.postings

		// Mismo término en otros bloques: salen consecutivos y en orden de bloque.
		for h.Len() > 0 && h[0].term == first.term {
			it := heap.Pop(&h).(mergeItem)
			if it.postings[0].DocID <= merged[len(merged)-1].DocID {
				return fmt.Errorf("%w: término %q", ErrDocOrder, first.term)
			}
			merged = append(merged, it.postings...)
			if err := advance(it.src); err != nil {
				return err
			}
		}
		if err := emit(first.term, merged); err != nil {
			return err
		}
	}
	return nil
}

// mergeToBlock mezcla los bloques paths en un único bloque out.
func mergeToBlock(paths []string, out string) (int64, error) {
	cursors, err := openCursors(paths)
	if err != nil {
		return 0, err
	}
	defer closeCursors(cursors)

	bw, err := createBlock(out)
	if err != nil {
		return 0, err
	}
	if err := mergeStream(cursors, bw.add); err != nil {
		bw.abort()
		return 0, err
	}
	return bw.close()
}

// reduceBlocks mezcla en rondas de a maxFanIn bloques consecutivos hasta
// que queden a lo sumo maxFanIn, para no abrir demasiados archivos a la vez.
// Agrupar bloques consecutivos preserva el orden de docID. Devuelve los
// bloques restantes, las rondas hechas y los bytes escritos.
func reduceBlocks(paths []string, maxFanIn int, nextPath func() string) ([]string, int, int64, error) {
	var rounds int
	var written int64
	for len(paths) > maxFanIn {
		next := make([]string, 0, (len(paths)+maxFanIn-1)/maxFanIn)
		for i := 0; i < len(paths); i += maxFanIn {
			group := paths[i:min(i+maxFanIn, len(paths))]
			if len(group) == 1 {
				next = append(next, group[0])
				continue
			}
			dst := nextPath()
			n, err := mergeToBlock(group, dst)
			if err != nil {
				return nil, rounds, written, err
			}
			written += n
			for _, p := range group {
				os.Remove(p)
			}
			next = append(next, dst)
		}
		paths = next
		rounds++
	}
	return paths, rounds, written, nil
}
