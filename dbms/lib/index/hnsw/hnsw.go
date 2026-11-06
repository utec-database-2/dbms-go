package hnsw

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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
	ID         uint64
	Similarity float32
	Distance   float32
}

// Index es una grafo HNSW cuyos contenidos pueden guardarse en un solo archivo.
// Las mutaciones se mantienen en memoria hasta que se llama a Save, lo que permite a los llamadores agrupar
// escrituras mientras que aún hace que reabrir el índice sea económico.
type HNSWIndex struct {
	mu              sync.RWMutex
	graph           *graph.SavedGraph[uint64]
	path            string
	metric          Metric
	dims            int
	ef_construction int
	ef_search       int
	M               int
	Ml              int
}

const (
	defaultEfConstruction = 64
	defaultM              = 16 // cantidad maxima de vecinos por nodo
	defaultMl             = 8  // cantidad maxima de vecinos por nodo en el nivel superior
)

var (
	errInvalidMetric = fmt.Errorf("hnsw: métrica no soportada")
	errInvalidVector = fmt.Errorf("hnsw: vector inválido")
)

// Open carga un indice existente o crea uno nuevo. La ruta debe ser un archivo, no un directorio.
func Open(path string, metric Metric, efConstruction ...int) (*HNSWIndex, error) {
	if path == "" {
		return nil, fmt.Errorf("hnsw: la ruta no puede estar vacía")
	}
	// Identifica si estamos abriendo o creando un nuevo índice.
	_, err := os.Stat(path)
	isNewIndex := os.IsNotExist(err)
	if err != nil && !isNewIndex {
		return nil, fmt.Errorf("hnsw: error al verificar la existencia del archivo: %w", err)
	}
	if metric != Cosine && metric != Euclidean && metric != ProductDot {
		return nil, errInvalidMetric
	}
	ef := defaultEfConstruction
	if len(efConstruction) > 0 {
		ef = efConstruction[0]
	}
	if ef < 1 {
		return nil, fmt.Errorf("hnsw: ef_constructor debe ser mayor que cero")
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("hnsw: crear directorio: %w", err)
		}
	}
	g, err := graph.LoadSavedGraph[uint64](path)
	if err != nil {
		return nil, fmt.Errorf("hnsw: abrir %s: %w", path, err)
	}
	if !isNewIndex {
		persistedMetric, ok := metricFromDistance(g.Distance)
		if !ok {
			return nil, fmt.Errorf("hnsw: %s contiene una función de distancia desconocida", path)
		}
		if persistedMetric != metric {
			return nil, fmt.Errorf("hnsw: %s usa la métrica %s, no %s", path, persistedMetric, metric)
		}
	}
	g.Distance = distance(metric)
	g.EfSearch = ef
	return &HNSWIndex{
		graph:           g,
		path:            path,
		metric:          metric,
		dims:            g.Dims(),
		ef_construction: ef,
		ef_search:       ef,
	}, nil
}

// Add inserta o reemplaza el vector asociado con id.
func (idx *HNSWIndex) Add(id uint64, vector []float32) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if err := idx.validateVector(vector); err != nil {
		return err
	}
	if idx.dims == 0 {
		idx.dims = len(vector)
	}
	// coder/hnsw usa EfSearch también durante Add para explorar candidatos de
	// construcción; se restaura aquí después de cualquier búsqueda.
	idx.graph.EfSearch = idx.ef_construction
	idx.graph.Add(graph.MakeNode(id, append([]float32(nil), vector...)))
	return nil
}

// Delete elimina el vector asociado con id y reporta si existía.
func (idx *HNSWIndex) Delete(id uint64) bool {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.graph.Delete(id)
}

// KNNSearch devuelve hasta k vecinos más cercanos al vector. (ef_search: parametro para controlar la calidad de la búsqueda, mayor es mejor pero más lento)
func (idx *HNSWIndex) KNNSearch(vector []float32, k int, ef_search int) ([]Result, error) {
	if ef_search < k {
		ef_search = k
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if err := idx.validateVector(vector); err != nil {
		return nil, err
	}
	if k <= 0 {
		return []Result{}, nil
	}
	previousEf := idx.graph.EfSearch
	idx.graph.EfSearch = ef_search
	defer func() { idx.graph.EfSearch = previousEf }()
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
	idx.ef_search = ef_search
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].Distance < results[j].Distance
	})
	return results, nil
}

// Save persiste la gráfica actual usando el formato de archivo de la biblioteca.
func (idx *HNSWIndex) Save() error {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if err := idx.graph.Save(); err != nil {
		return fmt.Errorf("hnsw: guardar %s: %w", idx.path, err)
	}
	return nil
}

func (idx *HNSWIndex) Close() error {
	if idx == nil {
		return nil
	}
	return idx.Save()
}

// Path devuelve la ruta del archivo de índice.
func (idx *HNSWIndex) Path() string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.path
}

// Len devuelve el número de vectores indexados.
func (idx *HNSWIndex) Len() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.graph.Len()
}

// Dims devuelve el vector dimension, o cero para un índice vacío.
func (idx *HNSWIndex) Dims() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.dims
}

func (idx *HNSWIndex) validateVector(vector []float32) error {
	if len(vector) == 0 {
		return errInvalidVector
	}
	if idx.dims != 0 && len(vector) != idx.dims {
		return fmt.Errorf("hnsw: dimensión %d, se esperaba %d", len(vector), idx.dims)
	}
	return nil
}

// Metric devuelve la métrica usada por el índice.
func (idx *HNSWIndex) Metric() Metric {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.metric
}

// EfConstruction devuelve el esfuerzo usado al construir nuevos nodos.
func (idx *HNSWIndex) EfConstruction() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.ef_construction
}

// DotProductSimilarity calcula el producto punto entre dos vectores.
func DotProductSimilarity(a, b []float32) (float32, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("DotProductSimilarity: vectores de diferente longitud")
	}
	var dot float32
	for i := range a {
		dot += a[i] * b[i]
	}
	return dot, nil
}

// ProductDotDistance adapta el producto punto a la convención de HNSW:
// valores menores representan vectores más cercanos.
func ProductDotDistance(a, b []float32) (float32, error) {
	sim, err := DotProductSimilarity(a, b)
	if err != nil {
		return 0, err
	}
	return 1 - sim, nil
}

func ProductDotDistanceFunc(a, b []float32) float32 {
	distance, err := ProductDotDistance(a, b)
	if err != nil {
		panic(err)
	}
	return distance
}

func init() {
	graph.RegisterDistanceFunc("product_dot", ProductDotDistanceFunc)
}

func metricFromDistance(distance graph.DistanceFunc) (Metric, bool) {
	if distance == nil {
		return "", false
	}
	pointer := reflect.ValueOf(distance).Pointer()
	switch {
	case pointer == reflect.ValueOf(graph.EuclideanDistance).Pointer():
		return Euclidean, true
	case pointer == reflect.ValueOf(graph.CosineDistance).Pointer():
		return Cosine, true
	case pointer == reflect.ValueOf(ProductDotDistanceFunc).Pointer():
		return ProductDot, true
	default:
		return "", false
	}
}

// distance devuelve la función de distancia correspondiente a la métrica.
func distance(metric Metric) graph.DistanceFunc {
	if metric == Euclidean {
		return graph.EuclideanDistance
	}
	if metric == ProductDot {
		return ProductDotDistanceFunc
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
