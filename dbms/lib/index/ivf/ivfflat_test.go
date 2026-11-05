package ivf

import (
	"errors"
	"math"
	"math/rand"
	"sort"
	"sync"
	"testing"
)

func newTestIndex(t *testing.T, dim, nlist int, metric MetricType, centroids []float32) *IVFFlatIndex {
	t.Helper()
	idx, err := NewIVFFlatIndex(dim, nlist, metric)
	if err != nil {
		t.Fatalf("NewIVFFlatIndex: %v", err)
	}
	if err := idx.SetCentroids(centroids); err != nil {
		t.Fatalf("SetCentroids: %v", err)
	}
	return idx
}

func mustInsert(t *testing.T, idx *IVFFlatIndex, id uint64, vec []float32) {
	t.Helper()
	if err := idx.Insert(id, vec); err != nil {
		t.Fatalf("Insert(%d): %v", id, err)
	}
}

func TestNewIVFFlatIndexValidation(t *testing.T) {
	tests := []struct {
		name    string
		dim     int
		nlist   int
		metric  MetricType
		wantErr error
	}{
		{"dim cero", 0, 2, MetricEuclidean, ErrInvalidDimension},
		{"dim negativa", -1, 2, MetricEuclidean, ErrInvalidDimension},
		{"nlist cero", 2, 0, MetricEuclidean, ErrInvalidNList},
		{"nlist negativa", 2, -1, MetricEuclidean, ErrInvalidNList},
		{"métrica inválida", 2, 2, MetricType(99), ErrInvalidMetric},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewIVFFlatIndex(tt.dim, tt.nlist, tt.metric)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, se esperaba %v", err, tt.wantErr)
			}
		})
	}

	idx, err := NewIVFFlatIndex(4, 3, MetricCosine)
	if err != nil {
		t.Fatalf("NewIVFFlatIndex válido: %v", err)
	}
	if idx.Dim() != 4 || idx.NList() != 3 || idx.Metric() != MetricCosine {
		t.Fatalf("accesores incorrectos: Dim=%d NList=%d Metric=%d", idx.Dim(), idx.NList(), idx.Metric())
	}
	if idx.Len() != 0 {
		t.Fatalf("Len() = %d, se esperaba 0", idx.Len())
	}
}

func TestIndexRequiresCentroids(t *testing.T) {
	idx, err := NewIVFFlatIndex(2, 2, MetricEuclidean)
	if err != nil {
		t.Fatalf("NewIVFFlatIndex: %v", err)
	}

	if err := idx.Insert(1, []float32{1, 1}); !errors.Is(err, ErrNotTrained) {
		t.Fatalf("Insert sin centroides: %v", err)
	}
	if _, err := idx.Query([]float32{1, 1}, 1, 1); !errors.Is(err, ErrNotTrained) {
		t.Fatalf("Query sin centroides: %v", err)
	}
	if err := idx.Delete(1); !errors.Is(err, ErrNotTrained) {
		t.Fatalf("Delete sin centroides: %v", err)
	}

	if err := idx.SetCentroids([]float32{0, 0, 1}); !errors.Is(err, ErrInvalidCentroids) {
		t.Fatalf("SetCentroids con longitud incorrecta: %v", err)
	}
	if err := idx.SetCentroids([]float32{0, 0, float32(math.NaN()), 1}); !errors.Is(err, ErrInvalidCentroids) {
		t.Fatalf("SetCentroids con NaN: %v", err)
	}
	if err := idx.SetCentroids([]float32{0, 0, 10, 10}); err != nil {
		t.Fatalf("SetCentroids válido: %v", err)
	}
	if idx.Len() != 0 {
		t.Fatalf("Len() = %d, se esperaba 0", idx.Len())
	}
}

func TestInsertQueryDeleteEuclidean(t *testing.T) {
	idx := newTestIndex(t, 2, 2, MetricEuclidean, []float32{0, 0, 10, 10})

	mustInsert(t, idx, 1, []float32{0.1, 0.1})
	mustInsert(t, idx, 2, []float32{0.2, 0.2})
	mustInsert(t, idx, 3, []float32{9.9, 9.9})
	mustInsert(t, idx, 4, []float32{10.1, 10.1})

	if idx.Len() != 4 {
		t.Fatalf("Len() = %d, se esperaba 4", idx.Len())
	}

	got, err := idx.Query([]float32{0, 0}, 2, 2)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 {
		t.Fatalf("Query cercanos a (0,0) = %+v, se esperaban IDs [1 2]", got)
	}
	if diff := math.Abs(float64(got[0].Dist) - math.Sqrt(0.02)); diff > 1e-5 {
		t.Fatalf("distancia de 1 = %v, se esperaba %v", got[0].Dist, math.Sqrt(0.02))
	}

	got, err = idx.Query([]float32{10, 10}, 2, 2)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 2 || got[0].ID != 3 || got[1].ID != 4 {
		t.Fatalf("Query cercanos a (10,10) = %+v, se esperaban IDs [3 4]", got)
	}

	if err := idx.Delete(1); err != nil {
		t.Fatalf("Delete(1): %v", err)
	}
	if idx.Len() != 3 {
		t.Fatalf("Len() = %d, se esperaba 3", idx.Len())
	}
	got, err = idx.Query([]float32{0, 0}, 2, 2)
	if err != nil {
		t.Fatalf("Query tras Delete: %v", err)
	}
	if len(got) == 0 || got[0].ID != 2 {
		t.Fatalf("Query tras Delete = %+v, se esperaba primero el ID 2", got)
	}

	if err := idx.Delete(1); !errors.Is(err, ErrIDNotFound) {
		t.Fatalf("Delete repetido: %v", err)
	}
	if err := idx.Delete(3); err != nil {
		t.Fatalf("Delete(3): %v", err)
	}
	got, err = idx.Query([]float32{10, 10}, 2, 2)
	if err != nil {
		t.Fatalf("Query tras Delete(3): %v", err)
	}
	if len(got) == 0 || got[0].ID != 4 {
		t.Fatalf("Query tras Delete(3) = %+v, se esperaba primero el ID 4", got)
	}
}

func TestInsertRejectsDuplicateID(t *testing.T) {
	idx := newTestIndex(t, 2, 1, MetricEuclidean, []float32{0, 0})

	mustInsert(t, idx, 7, []float32{1, 1})
	if err := idx.Insert(7, []float32{2, 2}); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("Insert duplicado: %v", err)
	}
	if idx.Len() != 1 {
		t.Fatalf("Len() = %d, se esperaba 1", idx.Len())
	}
	got, err := idx.Query([]float32{0, 0}, 10, 1)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 || got[0].ID != 7 {
		t.Fatalf("Query = %+v, se esperaba solo el ID 7", got)
	}
}

func TestValidation(t *testing.T) {
	idx := newTestIndex(t, 2, 2, MetricEuclidean, []float32{0, 0, 10, 10})
	mustInsert(t, idx, 1, []float32{1, 1})

	if err := idx.Insert(2, []float32{1}); !errors.Is(err, ErrInvalidDimension) {
		t.Fatalf("Insert con dimensión incorrecta: %v", err)
	}
	if err := idx.Insert(2, []float32{float32(math.NaN()), 0}); !errors.Is(err, ErrInvalidVector) {
		t.Fatalf("Insert con NaN: %v", err)
	}
	if err := idx.Insert(2, []float32{float32(math.Inf(1)), 0}); !errors.Is(err, ErrInvalidVector) {
		t.Fatalf("Insert con Inf: %v", err)
	}

	if _, err := idx.Query([]float32{1}, 1, 1); !errors.Is(err, ErrInvalidDimension) {
		t.Fatalf("Query con dimensión incorrecta: %v", err)
	}
	if _, err := idx.Query([]float32{float32(math.NaN()), 0}, 1, 1); !errors.Is(err, ErrInvalidVector) {
		t.Fatalf("Query con NaN: %v", err)
	}
	if _, err := idx.Query([]float32{1, 1}, -1, 1); !errors.Is(err, ErrInvalidK) {
		t.Fatalf("Query con k negativo: %v", err)
	}
	if _, err := idx.Query([]float32{1, 1}, 1, 0); !errors.Is(err, ErrInvalidNProbe) {
		t.Fatalf("Query con nprobe cero: %v", err)
	}
	if _, err := idx.Query([]float32{1, 1}, 1, -1); !errors.Is(err, ErrInvalidNProbe) {
		t.Fatalf("Query con nprobe negativo: %v", err)
	}

	got, err := idx.Query([]float32{1, 1}, 0, 1)
	if err != nil {
		t.Fatalf("Query con k=0: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Query con k=0 devolvió %d resultados", len(got))
	}

	// nprobe > NList se recorta; k > número de vectores devuelve todos.
	got, err = idx.Query([]float32{1, 1}, 100, 99)
	if err != nil {
		t.Fatalf("Query con nprobe grande: %v", err)
	}
	if len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("Query con nprobe grande = %+v, se esperaba el ID 1", got)
	}
}

func TestSetCentroidsResetsIndex(t *testing.T) {
	idx := newTestIndex(t, 2, 2, MetricEuclidean, []float32{0, 0, 10, 10})
	mustInsert(t, idx, 1, []float32{1, 1})
	if idx.Len() != 1 {
		t.Fatalf("Len() = %d, se esperaba 1", idx.Len())
	}

	if err := idx.SetCentroids([]float32{5, 5, 15, 15}); err != nil {
		t.Fatalf("SetCentroids: %v", err)
	}
	if idx.Len() != 0 {
		t.Fatalf("Len() tras SetCentroids = %d, se esperaba 0", idx.Len())
	}
	got, err := idx.Query([]float32{5, 5}, 10, 2)
	if err != nil {
		t.Fatalf("Query tras SetCentroids: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Query tras SetCentroids devolvió %d resultados", len(got))
	}

	// El ID puede reutilizarse porque el mapeo se reinició.
	mustInsert(t, idx, 1, []float32{5, 5})

	// Centroids devuelve una copia: mutar el resultado no debe afectar al índice.
	copyCentroids := idx.Centroids()
	copyCentroids[0] = 99
	if idx.Centroids()[0] == 99 {
		t.Fatal("Centroids() expuso el slice interno")
	}
}

func TestQueryBruteForceEuclidean(t *testing.T) {
	const (
		dim   = 4
		nlist = 4
		total = 100
	)
	centroids := []float32{
		0, 0, 0, 0,
		10, 0, 0, 0,
		0, 10, 0, 0,
		0, 0, 10, 0,
	}
	idx := newTestIndex(t, dim, nlist, MetricEuclidean, centroids)

	rng := rand.New(rand.NewSource(7))
	vectors := make([][]float32, total)
	ids := make([]uint64, total)
	for i := 0; i < total; i++ {
		vec := make([]float32, dim)
		for j := range vec {
			vec[j] = float32(rng.Float64()*20 - 5)
		}
		vectors[i] = vec
		ids[i] = uint64(i + 1)
		mustInsert(t, idx, ids[i], vec)
	}

	query := []float32{3.5, 4.5, 5.5, 6.5}
	got, err := idx.Query(query, 10, nlist)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	want := bruteForceEuclidean(vectors, ids, query, 10)
	if len(got) != len(want) {
		t.Fatalf("Query devolvió %d resultados, se esperaban %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID {
			t.Fatalf("posición %d: ID=%d, se esperaba %d", i, got[i].ID, want[i].ID)
		}
		if diff := math.Abs(float64(got[i].Dist) - float64(want[i].Dist)); diff > 1e-4 {
			t.Fatalf("posición %d: distancia=%v, se esperaba %v", i, got[i].Dist, want[i].Dist)
		}
	}
}

func TestQueryBruteForceCosine(t *testing.T) {
	const (
		dim   = 4
		nlist = 3
		total = 80
	)
	centroids := []float32{
		1, 0, 0, 0,
		0, 1, 0, 0,
		0, 0, 1, 0,
	}
	idx := newTestIndex(t, dim, nlist, MetricCosine, centroids)

	rng := rand.New(rand.NewSource(11))
	vectors := make([][]float32, total)
	ids := make([]uint64, total)
	for i := 0; i < total; i++ {
		vec := make([]float32, dim)
		for j := range vec {
			vec[j] = float32(rng.Float64()*2 - 1)
		}
		vectors[i] = vec
		ids[i] = uint64(i + 1)
		mustInsert(t, idx, ids[i], vec)
	}

	query := []float32{0.7, -0.2, 0.4, 0.1}
	got, err := idx.Query(query, 8, nlist)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	want := bruteForceCosine(vectors, ids, query, 8)
	if len(got) != len(want) {
		t.Fatalf("Query devolvió %d resultados, se esperaban %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID {
			t.Fatalf("posición %d: ID=%d, se esperaba %d", i, got[i].ID, want[i].ID)
		}
		if diff := math.Abs(float64(got[i].Dist) - float64(want[i].Dist)); diff > 1e-5 {
			t.Fatalf("posición %d: distancia=%v, se esperaba %v", i, got[i].Dist, want[i].Dist)
		}
	}
}

func TestQueryDotProduct(t *testing.T) {
	idx := newTestIndex(t, 2, 2, MetricDotProduct, []float32{1, 0, 0, 1})
	mustInsert(t, idx, 1, []float32{5, 0})
	mustInsert(t, idx, 2, []float32{0, 3})
	mustInsert(t, idx, 3, []float32{2, 2})

	got, err := idx.Query([]float32{1, 1}, 2, 2)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 3 {
		t.Fatalf("Query = %+v, se esperaban IDs [1 3]", got)
	}
	if got[0].Dist != -5 || got[1].Dist != -4 {
		t.Fatalf("distancias = [%v %v], se esperaban [-5 -4]", got[0].Dist, got[1].Dist)
	}
}

func TestComputeDistance(t *testing.T) {
	if got := computeDistance([]float32{0, 0}, []float32{3, 4}, MetricEuclidean); math.Abs(float64(got)-5) > 1e-6 {
		t.Fatalf("euclidiana = %v, se esperaba 5", got)
	}
	if got := computeDistance([]float32{1, 0}, []float32{0, 1}, MetricCosine); math.Abs(float64(got)-1) > 1e-6 {
		t.Fatalf("coseno = %v, se esperaba 1", got)
	}
	if got := computeDistance([]float32{1, 2}, []float32{3, 4}, MetricDotProduct); math.Abs(float64(got)+11) > 1e-6 {
		t.Fatalf("producto punto = %v, se esperaba -11", got)
	}
}

func TestConcurrentAccess(t *testing.T) {
	const (
		dim          = 2
		nlist        = 4
		initial      = 200
		inserters    = 4
		perInserter  = 250
		queryWorkers = 4
		perQuery     = 200
		deleters     = 2
		perDeleter   = 50
	)
	centroids := []float32{
		0, 0,
		10, 0,
		0, 10,
		10, 10,
	}
	idx := newTestIndex(t, dim, nlist, MetricEuclidean, centroids)

	rng := rand.New(rand.NewSource(123))
	for i := 0; i < initial; i++ {
		mustInsert(t, idx, uint64(i+1), []float32{
			float32(rng.Float64() * 10),
			float32(rng.Float64() * 10),
		})
	}

	var wg sync.WaitGroup
	errCh := make(chan error, inserters+queryWorkers+deleters)

	for g := 0; g < inserters; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(1000 + g)))
			for i := 0; i < perInserter; i++ {
				id := uint64(1000 + g*perInserter + i)
				vec := []float32{
					float32(rng.Float64() * 10),
					float32(rng.Float64() * 10),
				}
				if err := idx.Insert(id, vec); err != nil {
					errCh <- err
					return
				}
			}
		}(g)
	}

	for g := 0; g < queryWorkers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(2000 + g)))
			for i := 0; i < perQuery; i++ {
				query := []float32{
					float32(rng.Float64() * 10),
					float32(rng.Float64() * 10),
				}
				if _, err := idx.Query(query, 5, 2); err != nil {
					errCh <- err
					return
				}
			}
		}(g)
	}

	for g := 0; g < deleters; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perDeleter; i++ {
				id := uint64(1 + g*perDeleter + i)
				if err := idx.Delete(id); err != nil {
					errCh <- err
					return
				}
			}
		}(g)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("acceso concurrente: %v", err)
	}

	want := initial + inserters*perInserter - deleters*perDeleter
	if got := idx.Len(); got != want {
		t.Fatalf("Len() = %d, se esperaba %d", got, want)
	}
}

func bruteForceEuclidean(vectors [][]float32, ids []uint64, query []float32, k int) []Candidate {
	type scored struct {
		id   uint64
		dist float64
	}
	all := make([]scored, 0, len(vectors))
	for i, vec := range vectors {
		all = append(all, scored{id: ids[i], dist: distance64(query, vec, MetricEuclidean)})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].dist < all[j].dist })
	if k > len(all) {
		k = len(all)
	}
	result := make([]Candidate, k)
	for i := 0; i < k; i++ {
		result[i] = Candidate{ID: all[i].id, Dist: float32(all[i].dist)}
	}
	return result
}

func bruteForceCosine(vectors [][]float32, ids []uint64, query []float32, k int) []Candidate {
	type scored struct {
		id   uint64
		dist float64
	}
	all := make([]scored, 0, len(vectors))
	for i, vec := range vectors {
		all = append(all, scored{id: ids[i], dist: distance64(query, vec, MetricCosine)})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].dist < all[j].dist })
	if k > len(all) {
		k = len(all)
	}
	result := make([]Candidate, k)
	for i := 0; i < k; i++ {
		result[i] = Candidate{ID: all[i].id, Dist: float32(all[i].dist)}
	}
	return result
}
