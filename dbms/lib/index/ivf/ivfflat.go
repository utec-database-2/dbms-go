package ivf

import (
	"container/heap"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
)

// MetricType selecciona la métrica de distancia del índice.
type MetricType uint8

const (
	// MetricEuclidean ordena por distancia euclidiana: menor es más cercano.
	MetricEuclidean MetricType = iota
	// MetricCosine ordena por 1 - similitud coseno: menor es más cercano.
	MetricCosine
	// MetricDotProduct ordena por -producto punto: menor es mayor similitud.
	MetricDotProduct
)

var (
	ErrInvalidDimension = errors.New("ivf: dimensión inválida")
	ErrInvalidNList     = errors.New("ivf: número de listas inválido")
	ErrInvalidNProbe    = errors.New("ivf: nprobe inválido")
	ErrInvalidK         = errors.New("ivf: k inválido")
	ErrInvalidMetric    = errors.New("ivf: métrica inválida")
	ErrInvalidVector    = errors.New("ivf: vector inválido")
	ErrInvalidCentroids = errors.New("ivf: centroides inválidos")
	ErrInvalidState     = errors.New("ivf: estado interno inconsistente")
	ErrNotInitialized   = errors.New("ivf: índice no inicializado")
	ErrNotTrained       = errors.New("ivf: índice sin centroides (use SetCentroids)")
	ErrDuplicateID      = errors.New("ivf: ID duplicado")
	ErrIDNotFound       = errors.New("ivf: ID no encontrado")
)

// invertedList guarda los vectores de forma plana continua para evitar el
// overhead de GC de [][]float32.
type invertedList struct {
	ids     []uint64
	vectors []float32
}

// Candidate es un resultado de Query.
type Candidate struct {
	ID   uint64
	Dist float32
}

// IVFFlatIndex es un índice IVF-Flat. Después de llamar a SetCentroids, las
// listas y los centroides son inmutables salvo por Insert/Delete, que toman el
// lock de escritura del índice.
type IVFFlatIndex struct {
	mu sync.RWMutex

	dim       int
	nlist     int
	metric    MetricType
	centroids []float32
	lists     []invertedList
	// positions mapea el ID externo a la lista invertida donde vive. Evita
	// depender del vector original para poder borrar.
	positions map[uint64]int
}

func NewIVFFlatIndex(dim, nlist int, metric MetricType) (*IVFFlatIndex, error) {
	if dim <= 0 {
		return nil, fmt.Errorf("%w: %d", ErrInvalidDimension, dim)
	}
	if nlist <= 0 {
		return nil, fmt.Errorf("%w: %d", ErrInvalidNList, nlist)
	}
	if err := validateMetric(metric); err != nil {
		return nil, err
	}
	return &IVFFlatIndex{
		dim:       dim,
		nlist:     nlist,
		metric:    metric,
		lists:     make([]invertedList, nlist),
		positions: make(map[uint64]int),
	}, nil
}

func (idx *IVFFlatIndex) Dim() int { return idx.dim }

func (idx *IVFFlatIndex) NList() int { return idx.nlist }

func (idx *IVFFlatIndex) Metric() MetricType { return idx.metric }

func (idx *IVFFlatIndex) SetCentroids(centroids []float32) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if idx.dim <= 0 || idx.nlist <= 0 {
		return fmt.Errorf("%w: dim=%d nlist=%d", ErrNotInitialized, idx.dim, idx.nlist)
	}
	if err := validateMetric(idx.metric); err != nil {
		return err
	}
	if len(centroids) != idx.nlist*idx.dim {
		return fmt.Errorf("%w: se esperaban %d valores, se recibieron %d",
			ErrInvalidCentroids, idx.nlist*idx.dim, len(centroids))
	}
	for i := 0; i < idx.nlist; i++ {
		begin := i * idx.dim
		if err := validateComponents(centroids[begin : begin+idx.dim]); err != nil {
			return fmt.Errorf("%w: centroide %d: %v", ErrInvalidCentroids, i, err)
		}
	}

	idx.centroids = append([]float32(nil), centroids...)
	idx.lists = make([]invertedList, idx.nlist)
	idx.positions = make(map[uint64]int)
	return nil
}

func (idx *IVFFlatIndex) Centroids() []float32 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return append([]float32(nil), idx.centroids...)
}

func (idx *IVFFlatIndex) Insert(id uint64, vec []float32) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if err := idx.validateState(); err != nil {
		return err
	}
	if err := idx.validateVector(vec); err != nil {
		return err
	}
	if idx.positions == nil {
		idx.positions = make(map[uint64]int)
	}
	if _, exists := idx.positions[id]; exists {
		return fmt.Errorf("%w: %d", ErrDuplicateID, id)
	}

	listID, err := idx.findNearestCentroid(vec)
	if err != nil {
		return err
	}

	list := &idx.lists[listID]
	list.ids = append(list.ids, id)
	list.vectors = append(list.vectors, vec...)
	idx.positions[id] = listID
	return nil
}

func (idx *IVFFlatIndex) Delete(id uint64) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if err := idx.validateState(); err != nil {
		return err
	}

	listID, ok := idx.positions[id]
	if !ok {
		return fmt.Errorf("%w: %d", ErrIDNotFound, id)
	}
	if listID < 0 || listID >= len(idx.lists) {
		delete(idx.positions, id)
		return fmt.Errorf("%w: lista %d", ErrInvalidState, listID)
	}

	list := &idx.lists[listID]
	found := -1
	for i, elem := range list.ids {
		if elem == id {
			found = i
			break
		}
	}
	if found < 0 {
		delete(idx.positions, id)
		return fmt.Errorf("%w: %d", ErrIDNotFound, id)
	}

	copy(list.ids[found:], list.ids[found+1:])
	list.ids = list.ids[:len(list.ids)-1]

	offset := found * idx.dim
	if offset+idx.dim > len(list.vectors) {
		delete(idx.positions, id)
		return fmt.Errorf("%w: lista %d", ErrInvalidState, listID)
	}
	copy(list.vectors[offset:], list.vectors[offset+idx.dim:])
	list.vectors = list.vectors[:len(list.vectors)-idx.dim]

	delete(idx.positions, id)
	return nil
}

// Query devuelve hasta topK candidatos ordenados de menor a mayor distancia.
// nprobes debe ser al menos 1; si supera el número de listas se ajusta a NList.
func (idx *IVFFlatIndex) Query(query []float32, topK, nprobes int) ([]Candidate, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if err := idx.validateState(); err != nil {
		return nil, err
	}
	if err := idx.validateVector(query); err != nil {
		return nil, err
	}
	if topK < 0 {
		return nil, fmt.Errorf("%w: %d", ErrInvalidK, topK)
	}
	if nprobes <= 0 {
		return nil, fmt.Errorf("%w: %d", ErrInvalidNProbe, nprobes)
	}
	if nprobes > idx.nlist {
		nprobes = idx.nlist
	}
	if topK == 0 {
		return []Candidate{}, nil
	}

	buckets := idx.knnDistSearch(query, nprobes)

	// max-heap acotado: la raíz es el peor de los topK candidatos. No se
	// materializan todos los candidatos ni se ordena el conjunto completo.
	capHint := topK
	if capHint > 1024 {
		capHint = 1024
	}
	best := make(maxCandidateHeap, 0, capHint)

	for _, bucketID := range buckets {
		if bucketID < 0 || bucketID >= len(idx.lists) {
			continue
		}
		list := &idx.lists[bucketID]
		if len(list.vectors) != len(list.ids)*idx.dim {
			return nil, fmt.Errorf("%w: lista %d", ErrInvalidState, bucketID)
		}
		for i := 0; i < len(list.ids); i++ {
			offset := i * idx.dim
			vector := list.vectors[offset : offset+idx.dim]
			dist := rankingDistance64(query, vector, idx.metric)
			candidate := rankedCandidate{ID: list.ids[i], Dist: dist}

			switch {
			case len(best) < topK:
				heap.Push(&best, candidate)
			case candidate.Dist < best[0].Dist:
				best[0] = candidate
				heap.Fix(&best, 0)
			}
		}
	}

	result := make([]Candidate, len(best))
	for i, candidate := range best {
		result[i] = Candidate{
			ID:   candidate.ID,
			Dist: float32(finalDistance64(candidate.Dist, idx.metric)),
		}
	}
	slices.SortFunc(result, func(a, b Candidate) int {
		switch {
		case a.Dist < b.Dist:
			return -1
		case a.Dist > b.Dist:
			return 1
		default:
			return 0
		}
	})
	return result, nil
}

// Len devuelve el número de vectores indexados.
func (idx *IVFFlatIndex) Len() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	total := 0
	for i := range idx.lists {
		total += len(idx.lists[i].ids)
	}
	return total
}

func (idx *IVFFlatIndex) validateState() error {
	if idx.dim <= 0 {
		return fmt.Errorf("%w: dim=%d", ErrNotInitialized, idx.dim)
	}
	if idx.nlist <= 0 {
		return fmt.Errorf("%w: nlist=%d", ErrNotInitialized, idx.nlist)
	}
	if err := validateMetric(idx.metric); err != nil {
		return err
	}
	if len(idx.centroids) != idx.nlist*idx.dim {
		return ErrNotTrained
	}
	if len(idx.lists) != idx.nlist {
		return fmt.Errorf("%w: listas=%d, nlist=%d", ErrInvalidState, len(idx.lists), idx.nlist)
	}
	return nil
}

func (idx *IVFFlatIndex) validateVector(vec []float32) error {
	if len(vec) != idx.dim {
		return fmt.Errorf("%w: se esperaban %d dimensiones, se recibieron %d",
			ErrInvalidDimension, idx.dim, len(vec))
	}
	return validateComponents(vec)
}

func validateComponents(vec []float32) error {
	for _, component := range vec {
		value := float64(component)
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return ErrInvalidVector
		}
	}
	return nil
}

func validateMetric(metric MetricType) error {
	switch metric {
	case MetricEuclidean, MetricCosine, MetricDotProduct:
		return nil
	default:
		return fmt.Errorf("%w: %d", ErrInvalidMetric, metric)
	}
}

type centroidDist struct {
	id   int
	dist float64
}

func (idx *IVFFlatIndex) knnDistSearch(vec []float32, nprobes int) []int {
	if nprobes > idx.nlist {
		nprobes = idx.nlist
	}
	if nprobes < 1 {
		nprobes = 1
	}

	dists := make([]centroidDist, idx.nlist)
	for i := 0; i < idx.nlist; i++ {
		begin := i * idx.dim
		centroid := idx.centroids[begin : begin+idx.dim]
		dists[i] = centroidDist{
			id:   i,
			dist: rankingDistance64(centroid, vec, idx.metric),
		}
	}

	slices.SortFunc(dists, func(a, b centroidDist) int {
		switch {
		case a.dist < b.dist:
			return -1
		case a.dist > b.dist:
			return 1
		default:
			return 0
		}
	})

	result := make([]int, nprobes)
	for i := 0; i < nprobes; i++ {
		result[i] = dists[i].id
	}
	return result
}

func (idx *IVFFlatIndex) findNearestCentroid(vec []float32) (int, error) {
	minDist := math.Inf(1)
	bestIdx := -1

	for i := 0; i < idx.nlist; i++ {
		begin := i * idx.dim
		centroid := idx.centroids[begin : begin+idx.dim]
		dist := rankingDistance64(centroid, vec, idx.metric)
		if dist < minDist {
			minDist = dist
			bestIdx = i
		}
	}

	if bestIdx < 0 {
		return 0, ErrInvalidVector
	}
	return bestIdx, nil
}

type rankedCandidate struct {
	ID   uint64
	Dist float64
}

type maxCandidateHeap []rankedCandidate

func (h maxCandidateHeap) Len() int           { return len(h) }
func (h maxCandidateHeap) Less(i, j int) bool { return h[i].Dist > h[j].Dist }
func (h maxCandidateHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *maxCandidateHeap) Push(value any) {
	*h = append(*h, value.(rankedCandidate))
}

func (h *maxCandidateHeap) Pop() any {
	old := *h
	n := len(old)
	value := old[n-1]
	*h = old[:n-1]
	return value
}

func distance64(a, b []float32, metric MetricType) float64 {
	switch metric {
	case MetricEuclidean:
		return math.Sqrt(squaredEuclidean64(a, b))

	case MetricCosine:
		var dot, normA, normB float64
		for i := range a {
			ai := float64(a[i])
			bi := float64(b[i])
			dot += ai * bi
			normA += ai * ai
			normB += bi * bi
		}
		if normA == 0 || normB == 0 {
			return 1
		}
		return 1 - dot/(math.Sqrt(normA)*math.Sqrt(normB))

	case MetricDotProduct:
		var dot float64
		for i := range a {
			dot += float64(a[i]) * float64(b[i])
		}
		return -dot

	default:
		panic("ivf: métrica inválida")
	}
}

func squaredEuclidean64(a, b []float32) float64 {
	var sum float64
	for i := range a {
		diff := float64(a[i]) - float64(b[i])
		sum += diff * diff
	}
	return sum
}

// rankingDistance64 usa distancia al cuadrado para Euclidiana: es monótona y
// evita un sqrt por cada vector candidato.
func rankingDistance64(a, b []float32, metric MetricType) float64 {
	if metric == MetricEuclidean {
		return squaredEuclidean64(a, b)
	}
	return distance64(a, b, metric)
}

func finalDistance64(dist float64, metric MetricType) float64 {
	if metric == MetricEuclidean {
		return math.Sqrt(dist)
	}
	return dist
}

// computeDistance se conserva como utilidad para pruebas y usos internos que
// necesiten la distancia final en float32.
func computeDistance(a, b []float32, metric MetricType) float32 {
	return float32(distance64(a, b, metric))
}
