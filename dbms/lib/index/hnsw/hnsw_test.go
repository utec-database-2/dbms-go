package hnsw

import (
	"math"
	"math/rand"
	"strconv"
	"testing"
)

func closeIndex(t *testing.T, idx *HNSWIndex) {
	t.Helper()
	if err := idx.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestIndexPersistsAndSearches(t *testing.T) {
	path := t.TempDir() + "/vectors.graph"
	idx, err := Open(path, Euclidean)
	if err != nil {
		t.Fatal(err)
	}
	for id, vector := range map[uint64][]float32{
		1: {0, 0},
		2: {1, 1},
		3: {10, 10},
	} {
		if err := idx.Add(id, vector); err != nil {
			t.Fatal(err)
		}
	}
	closeIndex(t, idx)

	reopened, err := Open(path, Euclidean)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.KNNSearch([]float32{0.1, 0.1}, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 {
		t.Fatalf("vecinos inesperados: %+v", got)
	}
	closeIndex(t, reopened)
}

func TestIndexRejectsDimensionChanges(t *testing.T) {
	idx, err := Open(t.TempDir()+"/vectors.graph", Cosine)
	if err != nil {
		t.Fatal(err)
	}
	defer closeIndex(t, idx)
	if err := idx.Add(1, []float32{1, 0}); err != nil {
		t.Fatal(err)
	}
	if err := idx.Add(2, []float32{1, 0, 0}); err == nil {
		t.Fatal("se esperaba rechazar una dimensión distinta")
	}
}

func TestIndexConfiguresEfConstruction(t *testing.T) {
	idx, err := Open(t.TempDir()+"/vectors.graph", Euclidean, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer closeIndex(t, idx)
	if got := idx.EfConstruction(); got != 64 {
		t.Fatalf("ef_constructor inesperado: got %d, want 64", got)
	}
	if err := idx.Add(1, []float32{0, 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.KNNSearch([]float32{0, 0}, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := idx.Add(2, []float32{1, 1}); err != nil {
		t.Fatal(err)
	}
}

func TestIndexEfConstructionValues(t *testing.T) {
	for _, efConstruction := range []int{32, 64, 128} {
		t.Run("ef_construction="+strconv.Itoa(efConstruction), func(t *testing.T) {
			idx, err := Open(t.TempDir()+"/vectors.graph", Euclidean, efConstruction)
			if err != nil {
				t.Fatal(err)
			}
			defer closeIndex(t, idx)
			if got := idx.EfConstruction(); got != efConstruction {
				t.Fatalf("ef_constructor inesperado: got %d, want %d", got, efConstruction)
			}
			for id, vector := range map[uint64][]float32{
				1: {0, 0},
				2: {1, 1},
				3: {2, 2},
			} {
				if err := idx.Add(id, vector); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := idx.KNNSearch([]float32{0.1, 0.1}, 2, efConstruction); err != nil {
				t.Fatal(err)
			} else if len(got) != 2 {
				t.Fatalf("se esperaban 2 vecinos, se obtuvieron %d", len(got))
			}
		})
	}
}

func TestIndexRejectsInvalidEfConstruction(t *testing.T) {
	if _, err := Open(t.TempDir()+"/vectors.graph", Euclidean, 0); err == nil {
		t.Fatal("se esperaba rechazar ef_constructor cero")
	}
}

func TestProductDotSimilarityRanksHigherProductsFirst(t *testing.T) {
	idx, err := Open(t.TempDir()+"/vectors.graph", ProductDot)
	if err != nil {
		t.Fatal(err)
	}
	defer closeIndex(t, idx)
	for _, entry := range []struct {
		id     uint64
		vector []float32
	}{
		{id: 1, vector: []float32{1, 0}},
		{id: 2, vector: []float32{0.5, 0}},
		{id: 3, vector: []float32{0, 1}},
	} {
		id, vector := entry.id, entry.vector
		if err := idx.Add(id, vector); err != nil {
			t.Fatal(err)
		}
	}
	got, err := idx.KNNSearch([]float32{1, 0}, 3, 3)
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

func TestProductDotPersistsAndSearches(t *testing.T) {
	path := t.TempDir() + "/vectors.graph"
	idx, err := Open(path, ProductDot)
	if err != nil {
		t.Fatal(err)
	}
	if err := idx.Add(1, []float32{1, 0}); err != nil {
		t.Fatal(err)
	}
	if err := idx.Add(2, []float32{0.5, 0}); err != nil {
		t.Fatal(err)
	}
	closeIndex(t, idx)

	reopened, err := Open(path, ProductDot)
	if err != nil {
		t.Fatalf("reabrir producto punto: %v", err)
	}
	got, err := reopened.KNNSearch([]float32{1, 0}, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 {
		t.Fatalf("vecinos persistidos inesperados: %+v", got)
	}
	closeIndex(t, reopened)
}

func TestIndexRejectsPersistedMetricMismatch(t *testing.T) {
	path := t.TempDir() + "/vectors.graph"
	idx, err := Open(path, ProductDot)
	if err != nil {
		t.Fatal(err)
	}
	if err := idx.Add(1, []float32{1, 0}); err != nil {
		t.Fatal(err)
	}
	closeIndex(t, idx)
	if _, err := Open(path, Euclidean); err == nil {
		t.Fatal("se esperaba rechazar una métrica distinta a la persistida")
	}
}

func TestProductDotDistance(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{0.5, 0}
	c := []float32{0, 1}

	if got, _ := DotProductSimilarity(a, b); got != 0.5 {
		t.Fatalf("producto punto inesperado: got %f, want 0.5", got)
	}
	if got, _ := ProductDotDistance(a, b); got != 0.5 {
		t.Fatalf("distancia inesperada: got %f, want 0.5", got)
	}
	if got, _ := ProductDotDistance(a, c); got != 1 {
		t.Fatalf("distancia ortogonal inesperada: got %f, want 1", got)
	}
}

func TestDotProductSimilarityPanicsOnDifferentLengths(t *testing.T) {
	got, err := DotProductSimilarity([]float32{1, 0}, []float32{1, 0, 0})
	if err == nil {
		t.Fatal("se esperaba un error por vectores de diferente longitud")
	}
	if got != 0 {
		t.Fatalf("producto punto inesperado: got %f, want 0", got)
	}
}

func TestProductDotDistancePanicsOnDifferentLengths(t *testing.T) {
	got, err := ProductDotDistance([]float32{1, 0}, []float32{1, 0, 0})
	if err == nil {
		t.Fatal("se esperaba un error por vectores de diferente longitud")
	}
	if got != 0 {
		t.Fatalf("distancia inesperada: got %f, want 0", got)
	}
}

func Test_1000Vectors128Dims_Euclidean(t *testing.T) {
	idx, err := Open(t.TempDir()+"/vectors.graph", Euclidean)
	if err != nil {
		t.Fatal(err)
	}
	defer closeIndex(t, idx)
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 1000; i++ {
		vector := make([]float32, 128)
		for j := 0; j < 128; j++ {
			vector[j] = rng.Float32()
		}
		if err := idx.Add(uint64(i), vector); err != nil {
			t.Fatal(err)
		}
	}
	if idx.Len() != 1000 {
		t.Fatalf("longitud inesperada: got %d, want 1000", idx.Len())
	}
	if idx.Dims() != 128 {
		t.Fatalf("dimensión inesperada: got %d, want 128", idx.Dims())
	}
	queries := make([][]float32, 3)
	for i := range queries {
		queries[i] = make([]float32, 128)
		for j := range queries[i] {
			queries[i][j] = rng.Float32()
		}
	}
	neighborsPerQuery := make([][]Result, len(queries))
	for queryIndex, query := range queries {
		if len(query) != 128 {
			t.Fatalf("dimensión de consulta inesperada: got %d, want 128", len(query))
		}
		got, err := idx.KNNSearch(query, 10, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 10 {
			t.Fatalf("se esperaban 10 vecinos, se obtuvieron %d", len(got))
		}
		for i, neighbor := range got {
			if neighbor.ID >= 1000 {
				t.Fatalf("vecino inesperado en la posición %d: got %d, want >= 1000", i, neighbor.ID)
			}
		}
		neighborsPerQuery[queryIndex] = append([]Result(nil), got...)
	}

	closeIndex(t, idx)
	reopened, err := Open(idx.path, Euclidean)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Len() != 1000 {
		t.Fatalf("longitud inesperada después de reabrir: got %d, want 1000", reopened.Len())
	}
	if reopened.Dims() != 128 {
		t.Fatalf("dimensión inesperada después de reabrir: got %d, want 128", reopened.Dims())
	}
	for queryIndex, query := range queries {
		got, err := reopened.KNNSearch(query, 10, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 10 {
			t.Fatalf("se esperaban 10 vecinos después de reabrir, se obtuvieron %d", len(got))
		}
		before := neighborsPerQuery[queryIndex]
		if len(got) != len(before) {
			t.Fatalf("cantidad de vecinos distinta después de reabrir en query %d: got %d, want %d", queryIndex, len(got), len(before))
		}
		for i, neighbor := range got {
			if neighbor.ID >= 1000 {
				t.Fatalf("vecino inesperado después de reabrir en la posición %d: got %d, want >= 1000", i, neighbor.ID)
			}
			want := before[i]
			if neighbor.ID != want.ID {
				t.Fatalf("vecino distinto después de reabrir en query %d posición %d: got %d, want %d", queryIndex, i, neighbor.ID, want.ID)
			}
			if math.Abs(float64(neighbor.Distance-want.Distance)) > 1e-6 {
				t.Fatalf("distancia distinta después de reabrir en query %d posición %d: got %f, want %f", queryIndex, i, neighbor.Distance, want.Distance)
			}
			if math.Abs(float64(neighbor.Similarity-want.Similarity)) > 1e-6 {
				t.Fatalf("similitud distinta después de reabrir en query %d posición %d: got %f, want %f", queryIndex, i, neighbor.Similarity, want.Similarity)
			}
		}
	}

}

func Test_1000Vectors128Dims_ProductDot(t *testing.T) {
	idx, err := Open(t.TempDir()+"/vectors.graph", ProductDot)
	if err != nil {
		t.Fatal(err)
	}
	defer closeIndex(t, idx)
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 1000; i++ {
		vector := make([]float32, 128)
		for j := 0; j < 128; j++ {
			vector[j] = rng.Float32()
		}
		if err := idx.Add(uint64(i), vector); err != nil {
			t.Fatal(err)
		}
	}
	if idx.Len() != 1000 {
		t.Fatalf("longitud inesperada: got %d, want 1000", idx.Len())
	}
	if idx.Dims() != 128 {
		t.Fatalf("dimensión inesperada: got %d, want 128", idx.Dims())
	}
	queries := make([][]float32, 3)
	for i := range queries {
		queries[i] = make([]float32, 128)
		for j := range queries[i] {
			queries[i][j] = rng.Float32()
		}
	}
	neighborsPerQuery := make([][]Result, len(queries))
	for queryIndex, query := range queries {
		if len(query) != 128 {
			t.Fatalf("dimensión de consulta inesperada: got %d, want 128", len(query))
		}
		got, err := idx.KNNSearch(query, 10, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 10 {
			t.Fatalf("se esperaban 10 vecinos, se obtuvieron %d", len(got))
		}
		for i, neighbor := range got {
			if neighbor.ID >= 1000 {
				t.Fatalf("vecino inesperado en la posición %d: got %d, want >= 1000", i, neighbor.ID)
			}
		}
		neighborsPerQuery[queryIndex] = append([]Result(nil), got...)
	}

	closeIndex(t, idx)
	reopened, err := Open(idx.path, ProductDot)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Len() != 1000 {
		t.Fatalf("longitud inesperada después de reabrir: got %d, want 1000", reopened.Len())
	}
	if reopened.Dims() != 128 {
		t.Fatalf("dimensión inesperada después de reabrir: got %d, want 128", reopened.Dims())
	}
	for queryIndex, query := range queries {
		got, err := reopened.KNNSearch(query, 10, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 10 {
			t.Fatalf("se esperaban 10 vecinos después de reabrir, se obtuvieron %d", len(got))
		}
		before := neighborsPerQuery[queryIndex]
		if len(got) != len(before) {
			t.Fatalf("cantidad de vecinos distinta después de reabrir en query %d: got %d, want %d", queryIndex, len(got), len(before))
		}
		for i, neighbor := range got {
			if neighbor.ID >= 1000 {
				t.Fatalf("vecino inesperado después de reabrir en la posición %d: got %d, want >= 1000", i, neighbor.ID)
			}
			want := before[i]
			if neighbor.ID != want.ID {
				t.Fatalf("vecino distinto después de reabrir en query %d posición %d: got %d, want %d", queryIndex, i, neighbor.ID, want.ID)
			}
			if math.Abs(float64(neighbor.Distance-want.Distance)) > 1e-6 {
				t.Fatalf("distancia distinta después de reabrir en query %d posición %d: got %f, want %f", queryIndex, i, neighbor.Distance, want.Distance)
			}
			if math.Abs(float64(neighbor.Similarity-want.Similarity)) > 1e-6 {
				t.Fatalf("similitud distinta después de reabrir en query %d posición %d: got %f, want %f", queryIndex, i, neighbor.Similarity, want.Similarity)
			}
		}
	}

}

func Test_1000Vectors128Dims_Cosine(t *testing.T) {
	idx, err := Open(t.TempDir()+"/vectors.graph", Cosine)
	if err != nil {
		t.Fatal(err)
	}
	defer closeIndex(t, idx)
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 1000; i++ {
		vector := make([]float32, 128)
		for j := 0; j < 128; j++ {
			vector[j] = rng.Float32()
		}
		if err := idx.Add(uint64(i), vector); err != nil {
			t.Fatal(err)
		}
	}
	if idx.Len() != 1000 {
		t.Fatalf("longitud inesperada: got %d, want 1000", idx.Len())
	}
	if idx.Dims() != 128 {
		t.Fatalf("dimensión inesperada: got %d, want 128", idx.Dims())
	}
	queries := make([][]float32, 3)
	for i := range queries {
		queries[i] = make([]float32, 128)
		for j := range queries[i] {
			queries[i][j] = rng.Float32()
		}
	}
	neighborsPerQuery := make([][]Result, len(queries))
	for queryIndex, query := range queries {
		if len(query) != 128 {
			t.Fatalf("dimensión de consulta inesperada: got %d, want 128", len(query))
		}
		got, err := idx.KNNSearch(query, 10, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 10 {
			t.Fatalf("se esperaban 10 vecinos, se obtuvieron %d", len(got))
		}
		for i, neighbor := range got {
			if neighbor.ID >= 1000 {
				t.Fatalf("vecino inesperado en la posición %d: got %d, want >= 1000", i, neighbor.ID)
			}
		}
		neighborsPerQuery[queryIndex] = append([]Result(nil), got...)
	}

	closeIndex(t, idx)
	reopened, err := Open(idx.path, Cosine)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Len() != 1000 {
		t.Fatalf("longitud inesperada después de reabrir: got %d, want 1000", reopened.Len())
	}
	if reopened.Dims() != 128 {
		t.Fatalf("dimensión inesperada después de reabrir: got %d, want 128", reopened.Dims())
	}
	for queryIndex, query := range queries {
		got, err := reopened.KNNSearch(query, 10, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 10 {
			t.Fatalf("se esperaban 10 vecinos después de reabrir, se obtuvieron %d", len(got))
		}
		before := neighborsPerQuery[queryIndex]
		if len(got) != len(before) {
			t.Fatalf("cantidad de vecinos distinta después de reabrir en query %d: got %d, want %d", queryIndex, len(got), len(before))
		}
		for i, neighbor := range got {
			if neighbor.ID >= 1000 {
				t.Fatalf("vecino inesperado después de reabrir en la posición %d: got %d, want >= 1000", i, neighbor.ID)
			}
			want := before[i]
			if neighbor.ID != want.ID {
				t.Fatalf("vecino distinto después de reabrir en query %d posición %d: got %d, want %d", queryIndex, i, neighbor.ID, want.ID)
			}
			if math.Abs(float64(neighbor.Distance-want.Distance)) > 1e-6 {
				t.Fatalf("distancia distinta después de reabrir en query %d posición %d: got %f, want %f", queryIndex, i, neighbor.Distance, want.Distance)
			}
			if math.Abs(float64(neighbor.Similarity-want.Similarity)) > 1e-6 {
				t.Fatalf("similitud distinta después de reabrir en query %d posición %d: got %f, want %f", queryIndex, i, neighbor.Similarity, want.Similarity)
			}
		}
	}
}
