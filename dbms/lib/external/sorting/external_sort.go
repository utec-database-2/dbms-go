// Package sorting implementa el external sorting con k-way merge que usa el
// motor para ORDER BY y para GROUP BY cuando el conjunto no cabe en memoria.
//
// El algoritmo es el clásico de Runs: se acumulan filas en un buffer de
// SortBufferSlots; al llenarse se ordena el run y se vuelca a un run temporal;
// al final, si hubo más de un run, se hace un k-way merge sobre runs parciales
// (con una cola de prioridad para no comparar todos los pares en cada paso).
package sorting

import (
	"container/heap"
	"fmt"
	"sort"
)

// keyed asocia una fila con su clave de orden y su posición original.
type keyed[T any] struct {
	key any
	row T
	ord int
}

// Run es un fragmento ordenado de filas que queda pendiente de mezclar. Cada
// fila viaja con su clave porque el merge compara claves, no filas completas.
type Run[T any] struct {
	Rows  []keyed[T]
	pos   int
	order int // posición del run: desempata claves iguales en el merge
}

// sorter es la cola de prioridad del k-way merge: siempre saca el run cuya
// siguiente clave es la menor (o la mayor, si el ORDER BY es descendente).
type sorter[T any] struct {
	runs []*Run[T]
	desc bool
}

func (s sorter[T]) Len() int { return len(s.runs) }
func (s sorter[T]) Less(i, j int) bool {
	c := compare(s.runs[i].Rows[s.runs[i].pos].key, s.runs[j].Rows[s.runs[j].pos].key)
	if c != 0 {
		if s.desc {
			return c > 0
		}
		return c < 0
	}
	// Con claves iguales gana el run más antiguo, que es lo que hace estable el
	// merge respecto del orden de llegada.
	return s.runs[i].order < s.runs[j].order
}
func (s sorter[T]) Swap(i, j int) { s.runs[i], s.runs[j] = s.runs[j], s.runs[i] }
func (s *sorter[T]) Push(x any)   { s.runs = append(s.runs, x.(*Run[T])) }
func (s *sorter[T]) Pop() any {
	old := s.runs
	n := len(old)
	r := old[n-1]
	s.runs = old[:n-1]
	return r
}

// compare ordena dos valores sin conocer el esquema: usa comparación numérica
// cuando ambos son números, orden lexicográfico si son []byte o string, y
// comparación textual en el resto de casos.
func compare(a, b any) int {
	if ai, ok := asFloat(a); ok {
		if bi, ok := asFloat(b); ok {
			switch {
			case ai < bi:
				return -1
			case ai > bi:
				return 1
			default:
				return 0
			}
		}
	}
	as, aok := asText(a)
	bs, bok := asText(b)
	if aok && bok {
		switch {
		case as < bs:
			return -1
		case as > bs:
			return 1
		default:
			return 0
		}
	}
	return 0
}

func asFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case float32:
		return float64(x), true
	case float64:
		return x, true
	}
	return 0, false
}

func asText(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case []byte:
		return string(x), true
	}
	return "", false
}

// ExternalSort ordena rows según keyOf. Si rows no cabe en bufferSlots se
// generan varios runs y se combinan con k-way merge. Devuelve las filas
// ordenadas y cuántos runs hubo (1 significa que se resolveu en memoria).
func ExternalSort[T any](rows []T, keyOf func(T) (any, error), desc bool, bufferSlots int) ([]T, int, error) {
	if bufferSlots < 1 {
		bufferSlots = len(rows)
	}
	if bufferSlots < 1 {
		bufferSlots = 1
	}

	// Extraer las claves una sola vez: comparar sobre la clave evita recalcular
	// la función keyOf en cada comparación del merge.
	keys := make([]any, len(rows))
	for i, r := range rows {
		k, err := keyOf(r)
		if err != nil {
			return nil, 0, fmt.Errorf("sorting: no se pudo extraer la clave de la fila %d: %w", i, err)
		}
		keys[i] = k
	}

	var buf []keyed[T]

	// Fase de formación de runs.
	runs := make([]*Run[T], 0, len(rows)/bufferSlots+1)
	buf = make([]keyed[T], 0, bufferSlots)
	flush := func() {
		if len(buf) == 0 {
			return
		}
		sortKeyed(buf, desc)
		r := &Run[T]{Rows: make([]keyed[T], len(buf)), order: len(runs)}
		copy(r.Rows, buf)
		runs = append(runs, r)
		buf = buf[:0]
	}
	for i, r := range rows {
		buf = append(buf, keyed[T]{key: keys[i], row: r, ord: i})
		if len(buf) == bufferSlots {
			flush()
		}
	}
	flush()

	// Un solo run: ya está ordenado, no hace falta merge.
	if len(runs) <= 1 {
		out := make([]T, 0, len(rows))
		if len(runs) == 1 {
			for _, e := range runs[0].Rows {
				out = append(out, e.row)
			}
		}
		return out, 1, nil
	}

	// Fase de k-way merge con cola de prioridad.
	h := &sorter[T]{desc: desc}
	for _, r := range runs {
		if len(r.Rows) > 0 {
			h.runs = append(h.runs, r)
		}
	}
	heap.Init(h)

	out := make([]T, 0, len(rows))
	for h.Len() > 0 {
		top := h.runs[0]
		out = append(out, top.Rows[top.pos].row)
		top.pos++
		if top.pos >= len(top.Rows) {
			heap.Pop(h)
		} else {
			heap.Fix(h, 0)
		}
	}
	return out, len(runs), nil
}

// sortKeyed ordena por la clave y usa ord como desempate, de modo que el
// resultado sea determinista entre ejecuciones.
func sortKeyed[T any](s []keyed[T], desc bool) {
	sort.SliceStable(s, func(i, j int) bool {
		c := compare(s[i].key, s[j].key)
		if c != 0 {
			if desc {
				return c > 0
			}
			return c < 0
		}
		return s[i].ord < s[j].ord
	})
}
