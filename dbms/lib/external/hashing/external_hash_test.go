package hashing

import (
	"encoding/binary"
	"fmt"
	"os"
	"testing"
)

func intKey(v int) ([]byte, error) { return []byte(fmt.Sprint(v % 37)), nil }

func encInt(v int) ([]byte, error) {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, uint64(v))
	return b, nil
}

func decInt(b []byte) (int, error) { return int(binary.LittleEndian.Uint64(b)), nil }

func checkPartitions(t *testing.T, p *Partitioned[int], total int) {
	t.Helper()
	seen := 0
	owner := map[int]int{} // clave -> partición
	for i := 0; i < p.Len(); i++ {
		rows, err := p.Read(i)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != p.Count(i) {
			t.Fatalf("partición %d: %d filas, Count dice %d", i, len(rows), p.Count(i))
		}
		for _, v := range rows {
			k := v % 37
			if prev, ok := owner[k]; ok && prev != i {
				t.Fatalf("la clave %d quedó en las particiones %d y %d", k, prev, i)
			}
			owner[k] = i
			seen++
		}
	}
	if seen != total {
		t.Fatalf("se leyeron %d filas de %d", seen, total)
	}
}

func TestPartitionEnMemoria(t *testing.T) {
	items := make([]int, 500)
	for i := range items {
		items[i] = i
	}
	p, err := Partition(items, intKey, encInt, decInt, Options{Partitions: 4, BufferSlots: 1000})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.Spilled {
		t.Fatal("500 filas caben en un buffer de 1000: no debería volcar")
	}
	checkPartitions(t, p, len(items))
}

func TestPartitionVuelcaADisco(t *testing.T) {
	dir := t.TempDir()
	items := make([]int, 5000)
	for i := range items {
		items[i] = i
	}
	p, err := Partition(items, intKey, encInt, decInt, Options{Partitions: 8, BufferSlots: 100, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Spilled || p.BytesWritten == 0 {
		t.Fatal("5000 filas con buffer de 100 deberían volcarse a disco")
	}
	checkPartitions(t, p, len(items))
	if p.BytesRead != p.BytesWritten {
		t.Fatalf("leídos %d bytes, escritos %d", p.BytesRead, p.BytesWritten)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	left, _ := os.ReadDir(dir)
	if len(left) != 0 {
		t.Fatalf("quedaron %d temporales", len(left))
	}
}
