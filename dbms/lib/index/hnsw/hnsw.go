package hnsw

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	graph "github.com/coder/hnsw"
)

// Metric selecciona la métrica de similitud usada por el grafo.
type Metric string

const (
	Cosine     Metric = "cosine"
	Euclidean  Metric = "euclidean"
	ProductDot Metric = "product_dot"
)

// Result es un vecino más cercano. Similarity es mayor para vectores más cercanos.
type Result struct {
	ID         int64
	Similarity float32
	Distance   float32
}

// Index es una grafo HNSW cuyos contenidos pueden guardarse en un solo archivo.
// Las mutaciones se mantienen en memoria hasta que se llama a Save, lo que permite a los llamadores agrupar
// escrituras mientras que aún hace que reabrir el índice sea económico.
type Index struct {
	mu     sync.RWMutex
	graph  *graph.SavedGraph[int64]
	path   string
	metric Metric
	dims   int
}

// Open carga un indice existente o crea uno nuevo. La ruta debe ser un archivo, no un directorio.
func Open(path string, metric Metric) (*Index, error) {
	if path == "" {
		return nil, fmt.Errorf("hnsw: la ruta no puede estar vacía")
	}
	if metric != Cosine && metric != Euclidean && metric != ProductDot {
		return nil, fmt.Errorf("hnsw: métrica no soportada %q", metric)
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("hnsw: crear directorio: %w", err)
		}
	}
	g, err := graph.LoadSavedGraph[int64](path)
	if err != nil {
		return nil, fmt.Errorf("hnsw: abrir %s: %w", path, err)
	}
	g.Distance = distance(metric)
	return &Index{graph: g, path: path, metric: metric, dims: g.Dims()}, nil
}

// Add inserta o reemplaza el vector asociado con id.
func (idx *Index) Add(id int64, vector []float32) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if err := idx.validateVector(vector); err != nil {
		return err
	}
	if idx.dims == 0 {
		idx.dims = len(vector)
	}
	idx.graph.Add(graph.MakeNode(id, append([]float32(nil), vector...)))
	return nil
}

// Delete elimina el vector asociado con id y reporta si existía.
func (idx *Index) Delete(id int64) bool {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.graph.Delete(id)
}

// Search devuelve hasta k vecinos más cercanos al vector.
func (idx *Index) Search(vector []float32, k int) ([]Result, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if err := idx.validateVector(vector); err != nil {
		return nil, err
	}
	if k <= 0 {
		return []Result{}, nil
	}
	neighbors := idx.graph.Search(vector, k)
	results := make([]Result, 0, len(neighbors))
	for _, neighbor := range neighbors {
		distance := idx.graph.Distance(neighbor.Value, vector)
		results = append(results, Result{
			ID:         neighbor.Key,
			Distance:   distance,
			Similarity: similarity(idx.metric, distance),
		})
	}
	return results, nil
}

// Save persiste la gráfica actual usando el formato de archivo de la biblioteca.
func (idx *Index) Save() error {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if err := idx.graph.Save(); err != nil {
		return fmt.Errorf("hnsw: guardar %s: %w", idx.path, err)
	}
	return nil
}

// Len devuelve el número de vectores indexados.
func (idx *Index) Len() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.graph.Len()
}

// Dims devuelve el vector dimension, o cero para un índice vacío.
func (idx *Index) Dims() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.dims
}

func (idx *Index) validateVector(vector []float32) error {
	if len(vector) == 0 {
		return fmt.Errorf("hnsw: el vector no puede estar vacío")
	}
	if idx.dims != 0 && len(vector) != idx.dims {
		return fmt.Errorf("hnsw: dimensión %d, se esperaba %d", len(vector), idx.dims)
	}
	return nil
}

// Metric devuelve la métrica usada por el índice.
func (idx *Index) Metric() Metric {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.metric
}

// DotProductSimilarity calcula el producto punto entre dos vectores.
func DotProductSimilarity(a, b []float32) float32 {
	if len(a) != len(b) {
		panic("DotProductSimilarity: vectores de diferente longitud")
	}
	var dot float32
	for i := range a {
		dot += a[i] * b[i]
	}
	return dot
}

// ProductDotDistance adapta el producto punto a la convención de HNSW:
// valores menores representan vectores más cercanos.
func ProductDotDistance(a, b []float32) float32 {
	return 1 - DotProductSimilarity(a, b)
}

// distance devuelve la función de distancia correspondiente a la métrica.
func distance(metric Metric) graph.DistanceFunc {
	if metric == Euclidean {
		return graph.EuclideanDistance
	}
	if metric == ProductDot {
		return ProductDotDistance
	}
	return graph.CosineDistance
}

func similarity(metric Metric, distance float32) float32 {
	switch metric {
	case Euclidean:
		return -distance
	case Cosine, ProductDot:
		return 1 - distance
	default:
		return 0
	}
}
