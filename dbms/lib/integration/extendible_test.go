package integration

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/dbms-go/v2/dbms/lib/index/common"
	"github.com/dbms-go/v2/dbms/lib/index/extendible"
	"github.com/dbms-go/v2/dbms/lib/storage"
)

func TestIndex_SearchOnEmptyIndex(t *testing.T) {
	idx := extendible.New(4)
	rids, err := idx.Search("clave-inexistente")
	if err != nil {
		t.Fatalf("Search no debería devolver error en índice vacío: %v", err)
	}
	if len(rids) != 0 {
		t.Fatalf("esperaba 0 resultados, obtuve %d", len(rids))
	}
}

func TestIndex_RangeSearchNotSupported(t *testing.T) {
	idx := extendible.New(4)
	_, err := idx.RangeSearch(1, 10)
	if err != common.ErrRangeNotSupported {
		t.Fatalf("esperaba ErrRangeNotSupported, obtuve %v", err)
	}
}

func TestIndex_SupportsRangeIsFalse(t *testing.T) {
	idx := extendible.New(4)
	if idx.SupportsRange() {
		t.Fatal("Extendible Hashing no debe soportar range search")
	}
}

func TestIndex_InsertAndSearch(t *testing.T) {
	idx := extendible.New(4)
	rid := storage.RID{PageID: 0, SlotID: 0}
	if err := idx.Insert("ana", rid); err != nil {
		t.Fatalf("Insert falló: %v", err)
	}
	got, err := idx.Search("ana")
	if err != nil {
		t.Fatalf("Search falló: %v", err)
	}
	if len(got) != 1 || got[0] != rid {
		t.Fatalf("esperaba [%v], obtuve %v", rid, got)
	}
}

func TestIndex_SplitsWhenBucketFull(t *testing.T) {
	idx := extendible.New(2) // bucketSize chico a propósito, para forzar el split
	for i := 0; i < 10; i++ {
		rid := storage.RID{PageID: 0, SlotID: uint16(i)}
		if err := idx.Insert(i, rid); err != nil {
			t.Fatalf("Insert(%d) falló: %v", i, err)
		}
	}
	// Tras varios splits todos los elementos deben seguir siendo recuperables.
	for i := 0; i < 10; i++ {
		got, err := idx.Search(i)
		if err != nil {
			t.Fatalf("Search(%d) falló: %v", i, err)
		}
		want := storage.RID{PageID: 0, SlotID: uint16(i)}
		if len(got) != 1 || got[0] != want {
			t.Fatalf("Search(%d) = %v, esperaba [%v]", i, got, want)
		}
	}
}

// Regresión: el índice no tenía límite de profundidad. Cuando una sola
// clave acumulaba más RIDs que bucketSize, el algoritmo intentaba splitear
// su cubeta para siempre (todos esos RIDs comparten la misma clave, y por
// lo tanto el mismo hash: nunca se separan sin importar cuánto se
// profundice), duplicando el directorio en un bucle infinito hasta agotar
// memoria. Este test debe terminar rápido y no colgarse.
func TestIndex_ManyRIDsForSameKeyDoesNotHang(t *testing.T) {
	idx := extendible.New(1) // bucketSize=1: cualquier RID extra de la misma clave sobra
	const n = 500
	for i := 0; i < n; i++ {
		rid := storage.RID{PageID: 0, SlotID: uint16(i % 65536)}
		if err := idx.Insert("clave-repetida", rid); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	got, err := idx.Search("clave-repetida")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != n {
		t.Fatalf("Search devolvió %d RIDs, esperaba %d", len(got), n)
	}
	if idx.GlobalDepth() > extendible.MaxGlobalDepth {
		t.Fatalf("globalDepth=%d superó MaxGlobalDepth=%d", idx.GlobalDepth(), extendible.MaxGlobalDepth)
	}
}

// Con muchas claves DISTINTAS y bucketSize chico, el directorio sí debe
// crecer para mantener las cubetas dentro de un tamaño razonable, pero sin
// pasarse nunca de MaxGlobalDepth.
func TestIndex_ManyDistinctKeysStaysWithinMaxDepth(t *testing.T) {
	idx := extendible.New(4)
	const n = 5000
	rids := make(map[int]storage.RID, n)
	for i := 0; i < n; i++ {
		rid := storage.RID{PageID: uint32(i), SlotID: 0}
		if err := idx.Insert(i, rid); err != nil {
			t.Fatalf("Insert(%d): %v", i, err)
		}
		rids[i] = rid
	}
	if idx.GlobalDepth() > extendible.MaxGlobalDepth {
		t.Fatalf("globalDepth=%d superó MaxGlobalDepth=%d", idx.GlobalDepth(), extendible.MaxGlobalDepth)
	}
	for i := 0; i < n; i++ {
		got, err := idx.Search(i)
		if err != nil {
			t.Fatalf("Search(%d): %v", i, err)
		}
		if len(got) != 1 || got[0] != rids[i] {
			t.Fatalf("Search(%d) = %v, esperaba [%v]", i, got, rids[i])
		}
	}
}

// Stress aleatorizado: intercala inserts y deletes de claves distintas y
// verifica que cada clave viva siga siendo encontrable con el RID correcto.
func TestIndex_RandomizedStress(t *testing.T) {
	idx := extendible.New(3)
	rng := rand.New(rand.NewSource(42))
	live := map[string]storage.RID{}

	for step := 0; step < 2000; step++ {
		key := fmt.Sprintf("k-%d", rng.Intn(300))
		if rid, exists := live[key]; exists && rng.Intn(100) < 40 {
			ok, err := idx.Delete(key, rid)
			if err != nil || !ok {
				t.Fatalf("step %d Delete(%s): ok=%v err=%v", step, key, ok, err)
			}
			delete(live, key)
			continue
		}
		if _, exists := live[key]; exists {
			continue
		}
		rid := storage.RID{PageID: uint32(step), SlotID: 0}
		if err := idx.Insert(key, rid); err != nil {
			t.Fatalf("step %d Insert(%s): %v", step, key, err)
		}
		live[key] = rid
	}

	for key, rid := range live {
		got, err := idx.Search(key)
		if err != nil {
			t.Fatalf("Search(%s): %v", key, err)
		}
		if len(got) != 1 || got[0] != rid {
			t.Fatalf("Search(%s) = %v, esperaba [%v]", key, got, rid)
		}
	}
}
