package extendible

import (
	"testing"

	"github.com/dbms-go/v2/dbms/lib/index/common"
	"github.com/dbms-go/v2/dbms/lib/shared"
)

func TestIndex_SearchOnEmptyIndex(t *testing.T) {
	idx := New(4)
	rids, err := idx.Search("clave-inexistente")
	if err != nil {
		t.Fatalf("Search no debería devolver error en índice vacío: %v", err)
	}
	if len(rids) != 0 {
		t.Fatalf("esperaba 0 resultados, obtuve %d", len(rids))
	}
}

func TestIndex_RangeSearchNotSupported(t *testing.T) {
	idx := New(4)
	_, err := idx.RangeSearch(1, 10)
	if err != common.ErrRangeNotSupported {
		t.Fatalf("esperaba ErrRangeNotSupported, obtuve %v", err)
	}
}

func TestIndex_SupportsRangeIsFalse(t *testing.T) {
	idx := New(4)
	if idx.SupportsRange() {
		t.Fatal("Extendible Hashing no debe soportar range search")
	}
}

func TestIndex_InsertAndSearch(t *testing.T) {
	t.Skip("TODO: implementar Insert antes de habilitar este test")

	idx := New(4)
	rid := shared.RID{PageID: 0, SlotID: 0}
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
	t.Skip("TODO: implementar split de bucket + duplicación de directorio")

	idx := New(2) // bucketSize chico a propósito, para forzar el split
	for i := 0; i < 10; i++ {
		rid := shared.RID{PageID: 0, SlotID: i}
		if err := idx.Insert(i, rid); err != nil {
			t.Fatalf("Insert(%d) falló: %v", i, err)
		}
	}
	if idx.globalDepth <= 1 {
		t.Fatalf("esperaba globalDepth > 1 tras varios splits, quedó en %d", idx.globalDepth)
	}
}
