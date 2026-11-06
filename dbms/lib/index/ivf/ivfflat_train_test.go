package ivf

import (
	"errors"
	"math"
	"slices"
	"testing"
)

func TestTrainEuclideanClusters(t *testing.T) {
	vectors := [][]float32{
		{0, 0}, {0.1, 0}, {0, 0.1}, {-0.1, 0},
		{10, 10}, {10.1, 10}, {10, 10.1}, {9.9, 10},
	}
	idx, err := NewIVFFlatIndex(2, 2, MetricEuclidean)
	if err != nil {
		t.Fatalf("NewIVFFlatIndex: %v", err)
	}
	if err := idx.Train(vectors); err != nil {
		t.Fatalf("Train: %v", err)
	}
	if got := len(idx.Centroids()); got != 4 {
		t.Fatalf("Centroids() tiene %d valores, se esperaban 4", got)
	}
	if idx.Len() != 0 {
		t.Fatalf("Len() tras Train = %d, se esperaba 0", idx.Len())
	}

	for i, vec := range vectors {
		mustInsert(t, idx, uint64(i+1), vec)
	}

	got, err := idx.Query([]float32{0, 0}, 4, 2)
	if err != nil {
		t.Fatalf("Query cluster A: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("Query cluster A devolvió %d resultados", len(got))
	}
	for _, candidate := range got {
		if candidate.ID > 4 {
			t.Fatalf("Query cluster A devolvió el ID %d del otro cluster", candidate.ID)
		}
	}

	got, err = idx.Query([]float32{10, 10}, 4, 2)
	if err != nil {
		t.Fatalf("Query cluster B: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("Query cluster B devolvió %d resultados", len(got))
	}
	for _, candidate := range got {
		if candidate.ID <= 4 {
			t.Fatalf("Query cluster B devolvió el ID %d del otro cluster", candidate.ID)
		}
	}
}

func TestTrainValidation(t *testing.T) {
	idx, err := NewIVFFlatIndex(2, 2, MetricEuclidean)
	if err != nil {
		t.Fatalf("NewIVFFlatIndex: %v", err)
	}

	if err := idx.Train(nil); !errors.Is(err, ErrNotEnoughData) {
		t.Fatalf("Train(nil) = %v, se esperaba ErrNotEnoughData", err)
	}
	if err := idx.Train([][]float32{{1, 1}}); !errors.Is(err, ErrNotEnoughData) {
		t.Fatalf("Train con pocos vectores = %v, se esperaba ErrNotEnoughData", err)
	}
	if err := idx.Train([][]float32{{1, 1}, {1}}); !errors.Is(err, ErrInvalidDimension) {
		t.Fatalf("Train con dimensión incorrecta = %v, se esperaba ErrInvalidDimension", err)
	}
	if err := idx.Train([][]float32{{1, 1}, {float32(math.NaN()), 1}}); !errors.Is(err, ErrInvalidVector) {
		t.Fatalf("Train con NaN = %v, se esperaba ErrInvalidVector", err)
	}

	dot, err := NewIVFFlatIndex(2, 2, MetricDotProduct)
	if err != nil {
		t.Fatalf("NewIVFFlatIndex dot: %v", err)
	}
	if err := dot.Train([][]float32{{1, 1}, {2, 2}}); !errors.Is(err, ErrUnsupportedTrain) {
		t.Fatalf("Train dot product = %v, se esperaba ErrUnsupportedTrain", err)
	}
}

func TestTrainDeterministic(t *testing.T) {
	vectors := [][]float32{
		{0, 0}, {0.2, 0.1}, {-0.1, 0.2},
		{5, 5}, {5.2, 4.9}, {4.8, 5.1},
	}
	first, err := NewIVFFlatIndex(2, 2, MetricEuclidean)
	if err != nil {
		t.Fatalf("NewIVFFlatIndex: %v", err)
	}
	second, err := NewIVFFlatIndex(2, 2, MetricEuclidean)
	if err != nil {
		t.Fatalf("NewIVFFlatIndex: %v", err)
	}
	if err := first.Train(vectors); err != nil {
		t.Fatalf("Train first: %v", err)
	}
	if err := second.Train(vectors); err != nil {
		t.Fatalf("Train second: %v", err)
	}
	if !slices.Equal(first.Centroids(), second.Centroids()) {
		t.Fatalf("Train no es determinista:\n%v\n%v", first.Centroids(), second.Centroids())
	}
}

func TestTrainCosineNormalizesCentroids(t *testing.T) {
	vectors := [][]float32{
		{1, 0}, {0.9, 0.1}, {0.8, 0.2},
		{0, 1}, {0.1, 0.9}, {0.2, 0.8},
	}
	idx, err := NewIVFFlatIndex(2, 2, MetricCosine)
	if err != nil {
		t.Fatalf("NewIVFFlatIndex: %v", err)
	}
	if err := idx.Train(vectors); err != nil {
		t.Fatalf("Train: %v", err)
	}

	centroids := idx.Centroids()
	for i := 0; i < len(centroids); i += 2 {
		norm := math.Hypot(float64(centroids[i]), float64(centroids[i+1]))
		if math.Abs(norm-1) > 1e-5 {
			t.Fatalf("centroide %d no está normalizado: norma=%v", i/2, norm)
		}
	}

	for i, vec := range vectors {
		mustInsert(t, idx, uint64(i+1), vec)
	}
	got, err := idx.Query([]float32{1, 0}, 3, 2)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for _, candidate := range got {
		if candidate.ID > 3 {
			t.Fatalf("Query coseno devolvió el ID %d del otro grupo", candidate.ID)
		}
	}
}

func TestTrainResetsIndex(t *testing.T) {
	idx := newTestIndex(t, 2, 2, MetricEuclidean, []float32{0, 0, 10, 10})
	mustInsert(t, idx, 1, []float32{0, 0})
	if idx.Len() != 1 {
		t.Fatalf("Len() = %d, se esperaba 1", idx.Len())
	}

	if err := idx.Train([][]float32{{0, 0}, {10, 10}}); err != nil {
		t.Fatalf("Train: %v", err)
	}
	if idx.Len() != 0 {
		t.Fatalf("Len() tras Train = %d, se esperaba 0", idx.Len())
	}
	// El mapeo de IDs se reinició, así que el ID 1 puede reutilizarse.
	mustInsert(t, idx, 1, []float32{0, 0})
}

func TestTrainSingleList(t *testing.T) {
	idx, err := NewIVFFlatIndex(2, 1, MetricEuclidean)
	if err != nil {
		t.Fatalf("NewIVFFlatIndex: %v", err)
	}
	if err := idx.Train([][]float32{{0, 0}, {10, 10}}); err != nil {
		t.Fatalf("Train: %v", err)
	}
	centroids := idx.Centroids()
	if len(centroids) != 2 {
		t.Fatalf("Centroids() = %v, se esperaba un centroide de dimensión 2", centroids)
	}
	if math.Abs(float64(centroids[0])-5) > 1e-5 || math.Abs(float64(centroids[1])-5) > 1e-5 {
		t.Fatalf("centroide = %v, se esperaba [5 5]", centroids)
	}
}
