package hnsw

import "testing"

func TestIndexPersistsAndSearches(t *testing.T) {
	path := t.TempDir() + "/vectors.graph"
	idx, err := Open(path, Euclidean)
	if err != nil {
		t.Fatal(err)
	}
	for id, vector := range map[int64][]float32{
		1: {0, 0},
		2: {1, 1},
		3: {10, 10},
	} {
		if err := idx.Add(id, vector); err != nil {
			t.Fatal(err)
		}
	}
	if err := idx.Save(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path, Euclidean)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Search([]float32{0.1, 0.1}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 {
		t.Fatalf("vecinos inesperados: %+v", got)
	}
}

func TestIndexRejectsDimensionChanges(t *testing.T) {
	idx, err := Open(t.TempDir()+"/vectors.graph", Cosine)
	if err != nil {
		t.Fatal(err)
	}
	if err := idx.Add(1, []float32{1, 0}); err != nil {
		t.Fatal(err)
	}
	if err := idx.Add(2, []float32{1, 0, 0}); err == nil {
		t.Fatal("se esperaba rechazar una dimensión distinta")
	}
}

func TestProductDotSimilarityRanksHigherProductsFirst(t *testing.T) {
	idx, err := Open(t.TempDir()+"/vectors.graph", ProductDot)
	if err != nil {
		t.Fatal(err)
	}
	for id, vector := range map[int64][]float32{
		1: {1, 0},
		2: {0.5, 0},
		3: {0, 1},
	} {
		if err := idx.Add(id, vector); err != nil {
			t.Fatal(err)
		}
	}
	got, err := idx.Search([]float32{1, 0}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != 1 || got[1].ID != 2 || got[2].ID != 3 {
		t.Fatalf("orden por producto punto inesperado: %+v", got)
	}
	if got[0].Similarity <= got[1].Similarity || got[1].Similarity <= got[2].Similarity {
		t.Fatalf("similitudes inesperadas: %+v", got)
	}
}