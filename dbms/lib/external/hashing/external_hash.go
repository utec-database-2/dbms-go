// Package hashing implementa external hashing: reparte un conjunto de filas en
// particiones según el hash de una clave, de modo que todas las filas con la
// misma clave caen en la misma partición. Así un GROUP BY o un JOIN se resuelven
// partición por partición, con una tabla hash que solo necesita caber en
// memoria para una partición a la vez (Grace hash).
//
// Si las filas no caben en el buffer, cada partición se vuelca a un archivo
// temporal con el formato
//
//	[u32 len][fila codificada] ...
//
// y se vuelve a leer cuando le toca. Con pocas filas todo queda en memoria.
package hashing

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"io"
	"os"
)

// Options configura el particionado.
type Options struct {
	// Partitions es el número de particiones (por defecto 8).
	Partitions int
	// BufferSlots es cuántas filas caben en memoria. Si la entrada lo supera,
	// las particiones se vuelcan a disco. Con 0 nunca se vuelca.
	BufferSlots int
	// Dir es el directorio de los temporales (vacío = directorio temporal).
	Dir string
	// Prefix da nombre a los temporales (por defecto "hashpart").
	Prefix string
	// NoSpill mantiene todo en memoria aunque se supere el buffer.
	NoSpill bool
}

// Partitioned es el resultado de Partition: las particiones, en memoria o en
// disco, y las estadísticas de E/S para el plan de ejecución.
type Partitioned[T any] struct {
	parts  [][]T
	files  []string
	counts []int
	decode func([]byte) (T, error)

	// Spilled indica que las particiones se escribieron a disco.
	Spilled bool
	// BytesWritten y BytesRead son la E/S del volcado.
	BytesWritten int64
	BytesRead    int64
}

// HashKey es la función de hash de las claves (FNV-1a de 32 bits).
func HashKey(key []byte) uint32 {
	h := fnv.New32a()
	_, _ = h.Write(key)
	return h.Sum32()
}

// Partition reparte items en particiones por el hash de keyOf. Dos llamadas con
// el mismo número de particiones mandan claves iguales a la misma partición,
// que es lo que permite el hash join.
func Partition[T any](
	items []T,
	keyOf func(T) ([]byte, error),
	encode func(T) ([]byte, error),
	decode func([]byte) (T, error),
	opt Options,
) (*Partitioned[T], error) {
	n := opt.Partitions
	if n <= 0 {
		n = 8
	}
	p := &Partitioned[T]{
		parts:  make([][]T, n),
		counts: make([]int, n),
		decode: decode,
	}
	spill := !opt.NoSpill && opt.BufferSlots > 0 && len(items) > opt.BufferSlots

	var (
		writers []*bufio.Writer
		files   []*os.File
	)
	if spill {
		prefix := opt.Prefix
		if prefix == "" {
			prefix = "hashpart"
		}
		dir := opt.Dir
		if dir == "" {
			dir = os.TempDir()
		}
		for i := 0; i < n; i++ {
			f, err := os.CreateTemp(dir, fmt.Sprintf("%s-%d-*.tmp", prefix, i))
			if err != nil {
				closeAll(files)
				p.files = names(files)
				_ = p.Close()
				return nil, fmt.Errorf("hashing: no se pudo crear la partición %d: %w", i, err)
			}
			files = append(files, f)
			writers = append(writers, bufio.NewWriter(f))
		}
		p.files = names(files)
		p.Spilled = true
	}

	head := make([]byte, 4)
	for _, it := range items {
		key, err := keyOf(it)
		if err != nil {
			closeAll(files)
			_ = p.Close()
			return nil, err
		}
		i := int(HashKey(key) % uint32(n))
		p.counts[i]++
		if !spill {
			p.parts[i] = append(p.parts[i], it)
			continue
		}
		buf, err := encode(it)
		if err != nil {
			closeAll(files)
			_ = p.Close()
			return nil, err
		}
		binary.LittleEndian.PutUint32(head, uint32(len(buf)))
		if _, err := writers[i].Write(head); err != nil {
			closeAll(files)
			_ = p.Close()
			return nil, err
		}
		if _, err := writers[i].Write(buf); err != nil {
			closeAll(files)
			_ = p.Close()
			return nil, err
		}
		p.BytesWritten += int64(4 + len(buf))
	}
	for i, w := range writers {
		if err := w.Flush(); err != nil {
			closeAll(files)
			_ = p.Close()
			return nil, fmt.Errorf("hashing: no se pudo escribir la partición %d: %w", i, err)
		}
	}
	closeAll(files)
	return p, nil
}

// Len devuelve el número de particiones.
func (p *Partitioned[T]) Len() int { return len(p.counts) }

// Count devuelve cuántas filas hay en la partición i.
func (p *Partitioned[T]) Count(i int) int { return p.counts[i] }

// Read devuelve las filas de la partición i, leyéndolas de disco si hizo falta.
func (p *Partitioned[T]) Read(i int) ([]T, error) {
	if !p.Spilled {
		return p.parts[i], nil
	}
	f, err := os.Open(p.files[i])
	if err != nil {
		return nil, fmt.Errorf("hashing: no se pudo abrir la partición %d: %w", i, err)
	}
	defer f.Close()
	r := bufio.NewReader(f)
	out := make([]T, 0, p.counts[i])
	head := make([]byte, 4)
	for {
		if _, err := io.ReadFull(r, head); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("hashing: partición %d truncada: %w", i, err)
		}
		size := binary.LittleEndian.Uint32(head)
		buf := make([]byte, size)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, fmt.Errorf("hashing: partición %d truncada: %w", i, err)
		}
		p.BytesRead += int64(4 + len(buf))
		it, err := p.decode(buf)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}

// Close borra los archivos temporales.
func (p *Partitioned[T]) Close() error {
	var first error
	for _, name := range p.files {
		if err := os.Remove(name); err != nil && !os.IsNotExist(err) && first == nil {
			first = err
		}
	}
	p.files = nil
	return first
}

func closeAll(files []*os.File) {
	for _, f := range files {
		_ = f.Close()
	}
}

func names(files []*os.File) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Name()
	}
	return out
}
