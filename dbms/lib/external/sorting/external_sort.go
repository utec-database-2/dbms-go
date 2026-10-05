// Package sorting implementa el external sorting con k-way merge que usa el
// motor para ORDER BY y para GROUP BY cuando el conjunto no cabe en memoria.
//
// El algoritmo es el clásico de runs: se acumulan filas en un buffer de
// SortBufferSlots; al llenarse se ordena el run y se vuelca a un archivo
// temporal; al final, si hubo más de un run, se hace un k-way merge sobre los
// runs parciales con una cola de prioridad, para no comparar todos los pares en
// cada paso.
//
// Hay dos modos. ExternalSort mantiene los runs en memoria (rápido y sin tocar
// el disco) y ExternalSortSpilling los escribe en archivos temporales, que es lo
// que acota la memoria al tamaño del buffer.
package sorting

import "sort"

// keyed asocia una fila con su clave de orden y su posición original.
type keyed[T any] struct {
	key any
	row T
	ord int
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

// ExternalSort ordena rows según keyOf sin tocar el disco: cada run se ordena y
// se conserva en memoria, y si hubo más de uno se combinan con un k-way merge.
// Devuelve las filas ordenadas y cuántos runs hubo (1 significa que se resolvió
// en memoria con un solo run).
//
// Es la misma implementación que ExternalSortSpilling en modo NoSpill, que
// además devuelve las estadísticas del volcado.
func ExternalSort[T any](rows []T, keyOf func(T) (any, error), desc bool, bufferSlots int) ([]T, int, error) {
	res, err := ExternalSortSpilling(rows, keyOf, nil, nil, SpillOptions{
		BufferSlots: bufferSlots,
		Desc:        desc,
		NoSpill:     true,
	})
	if err != nil {
		return nil, 0, err
	}
	runs := res.Runs
	if len(rows) == 0 {
		runs = 0
	}
	return res.Rows, runs, nil
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
