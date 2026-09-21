package integration

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"

	"github.com/dbms-go/v2/dbms/lib/concurrency/sequential"
	"github.com/dbms-go/v2/dbms/lib/storage/heap"
	"github.com/dbms-go/v2/indexes/bplus"
)

func TestTreeStress100k(t *testing.T) {
	if testing.Short() {
		t.Skip("100k stress test")
	}
	tree := bplus.MustNew[int64, int64](64)
	const n = 100000
	for i := int64(0); i < n; i++ {
		key := (i * 7919) % 20003
		if err := tree.Insert(key, i); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	if err := tree.Validate(); err != nil {
		t.Fatalf("validate after insert: %v", err)
	}
	if tree.Len() != n {
		t.Fatalf("len=%d", tree.Len())
	}
	if _, err := tree.RangeSearch(500, 1500); err != nil {
		t.Fatal(err)
	}
	for i := int64(0); i < n; i += 2 {
		key := (i * 7919) % 20003
		if err := tree.Delete(key, i); err != nil {
			t.Fatalf("delete %d: %v", i, err)
		}
	}
	if err := tree.Validate(); err != nil {
		t.Fatalf("validate after deletes: %v", err)
	}
	if tree.Len() != n/2 {
		t.Fatalf("len after delete=%d", tree.Len())
	}
}

// Regresión: el B+ agrupado guarda en memoria el RID que le devuelve el
// SeqFile al insertar. Con el diseño viejo del Archivo Secuencial, ese RID
// codificaba "posición entre las claves vivas de la página", así que
// insertar una clave MENOR corría de lugar a las que ya estaban indexadas.
// El resultado: Search(10) devolvía el payload de otra clave sin ningún
// error. Ver dbms/lib/concurrency/sequential para el RID lógico estable.
func TestClusteredIndexRIDStableAfterLaterInsert(t *testing.T) {
	dir := t.TempDir()
	sf, err := sequential.Create(filepath.Join(dir, "m.seq"), filepath.Join(dir, "o.seq"), 4, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer sf.Close()

	idx, err := bplus.NewClusteredIndex(4, sf)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := idx.Insert(10, []byte("diez")); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.Insert(5, []byte("cinco")); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.Insert(1, []byte("uno")); err != nil {
		t.Fatal(err)
	}

	got, err := idx.Search(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || string(got[0].Payload) != "diez" {
		t.Fatalf("Search(10) = %+v, want payload %q", got, "diez")
	}

	if err := idx.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// Stress aleatorizado del B+ agrupado contra el Archivo Secuencial real:
// intercala inserts y deletes, verificando en cada paso que el índice y el
// storage sigan de acuerdo (Validate) y que cada RID vivo siga resolviendo
// al payload correcto vía Search.
func TestClusteredIndexRandomizedStress(t *testing.T) {
	dir := t.TempDir()
	sf, err := sequential.Create(filepath.Join(dir, "m.seq"), filepath.Join(dir, "o.seq"), 3, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer sf.Close()

	idx, err := bplus.NewClusteredIndex(4, sf)
	if err != nil {
		t.Fatal(err)
	}

	rng := rand.New(rand.NewSource(7))
	type liveRow struct {
		key     int64
		rid     sequential.RecordID
		payload string
	}
	live := map[int64]liveRow{}

	for step := 0; step < 500; step++ {
		if len(live) == 0 || rng.Intn(100) < 70 {
			key := int64(rng.Intn(60) - 30)
			if _, exists := live[key]; exists {
				continue
			}
			val := fmt.Sprintf("v-%d", step)
			rid, err := idx.Insert(key, []byte(val))
			if err != nil {
				t.Fatalf("step %d Insert(%d): %v", step, key, err)
			}
			live[key] = liveRow{key: key, rid: rid, payload: val}
		} else {
			var victim int64
			i, target := 0, rng.Intn(len(live))
			for k := range live {
				if i == target {
					victim = k
					break
				}
				i++
			}
			row := live[victim]
			if err := idx.Delete(row.key, row.rid); err != nil {
				t.Fatalf("step %d Delete(%d): %v", step, row.key, err)
			}
			delete(live, victim)
		}

		if step%25 != 0 {
			continue
		}
		if err := idx.Validate(); err != nil {
			t.Fatalf("step %d Validate: %v", step, err)
		}
		for _, row := range live {
			got, err := idx.Search(row.key)
			if err != nil {
				t.Fatalf("step %d Search(%d): %v", step, row.key, err)
			}
			if len(got) != 1 || string(got[0].Payload) != row.payload {
				t.Fatalf("step %d Search(%d) = %+v, want payload %q", step, row.key, got, row.payload)
			}
		}
	}
}

func encodeIntPayload(v int) []byte {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(int64(v)))
	return append([]byte("v"), buf[:]...)
}

func decodeIntPayload(payload []byte) (int, error) {
	if len(payload) != 9 || payload[0] != 'v' {
		return 0, fmt.Errorf("payload inesperado: %v", payload)
	}
	return int(int64(binary.BigEndian.Uint64(payload[1:]))), nil
}

// Stress aleatorizado del B+ NO agrupado contra un Heap File real: mezcla
// inserts y deletes, verificando en cada paso que el índice y el heap
// sigan de acuerdo (Validate) y que cada clave viva resuelva al payload
// correcto a través de su RID físico.
func TestUnclusteredIndexRandomizedStress(t *testing.T) {
	dir := t.TempDir()
	h, err := heap.Create(filepath.Join(dir, "data.heap"), heap.DefaultPageSize)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	keyOf := func(payload []byte) (int, error) { return decodeIntPayload(payload) }
	idx, err := bplus.NewUnclusteredIndex[int](4, h, keyOf)
	if err != nil {
		t.Fatal(err)
	}

	rng := rand.New(rand.NewSource(99))
	type liveRow struct {
		key int
		rid heap.RecordID
	}
	live := map[int]liveRow{}

	for step := 0; step < 500; step++ {
		if len(live) == 0 || rng.Intn(100) < 70 {
			key := rng.Intn(80) - 40
			if _, exists := live[key]; exists {
				continue
			}
			rid, err := idx.InsertWithKey(key, encodeIntPayload(key))
			if err != nil {
				t.Fatalf("step %d InsertWithKey(%d): %v", step, key, err)
			}
			live[key] = liveRow{key: key, rid: rid}
		} else {
			var victim int
			i, target := 0, rng.Intn(len(live))
			for k := range live {
				if i == target {
					victim = k
					break
				}
				i++
			}
			row := live[victim]
			if err := idx.Delete(row.key, row.rid); err != nil {
				t.Fatalf("step %d Delete(%d): %v", step, row.key, err)
			}
			delete(live, victim)
		}

		if step%25 != 0 {
			continue
		}
		if err := idx.Validate(); err != nil {
			t.Fatalf("step %d Validate: %v", step, err)
		}
		for _, row := range live {
			got, err := idx.Search(row.key)
			if err != nil {
				t.Fatalf("step %d Search(%d): %v", step, row.key, err)
			}
			if len(got) != 1 || !bytes.Equal(got[0].Payload, encodeIntPayload(row.key)) {
				t.Fatalf("step %d Search(%d) = %+v", step, row.key, got)
			}
		}
	}
}
