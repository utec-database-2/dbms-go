package ivf

import (
	"container/heap"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"slices"
	"sync"

	metricpkg "github.com/dbms-go/v2/dbms/lib/vector/metric"
)

// MetricType selecciona la métrica de distancia del índice. Es un alias del
// tipo compartido con los extractores de features.
type MetricType = metricpkg.Metric

const (
	// MetricEuclidean ordena por distancia euclidiana: menor es más cercano.
	MetricEuclidean = metricpkg.Euclidean
	// MetricCosine ordena por 1 - similitud coseno: menor es más cercano.
	MetricCosine = metricpkg.Cosine
	// MetricDotProduct ordena por -producto punto: menor es mayor similitud.
	MetricDotProduct = metricpkg.DotProduct
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
	ErrNotTrained       = errors.New("ivf: índice sin centroides (use SetCentroids o Train)")
	ErrDuplicateID      = errors.New("ivf: ID duplicado")
	ErrIDNotFound       = errors.New("ivf: ID no encontrado")
	ErrNotEnoughData    = errors.New("ivf: datos de entrenamiento insuficientes")
	ErrUnsupportedTrain = errors.New("ivf: métrica no soportada para entrenamiento")
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

type TrainingData struct {
	maxIters int
	tol      float64
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

// Train entrena los centroides gruesos con k-means sobre vectores ya
// extraídos. No conoce imágenes ni audio: el llamador debe convertir cada
// objeto a un []float32 de dimensión Dim().
//
// La métrica Euclidiana usa k-means clásico. Cosine usa k-means esférico
// (normaliza vectores y centroides). DotProduct no define un k-means estable:
// para esa métrica hay que cargar centroides con SetCentroids.
//
// Train reinicia las listas y el mapeo de IDs, igual que SetCentroids.
func (idx *IVFFlatIndex) Train(vectors [][]float32) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if idx.dim <= 0 || idx.nlist <= 0 {
		return fmt.Errorf("%w: dim=%d nlist=%d", ErrNotInitialized, idx.dim, idx.nlist)
	}
	if err := metricpkg.Validate(idx.metric); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidMetric, err)
	}
	if idx.metric == MetricDotProduct {
		return fmt.Errorf("%w: use SetCentroids para dot product", ErrUnsupportedTrain)
	}
	if len(vectors) < idx.nlist {
		return fmt.Errorf("%w: %d vectores para nlist=%d", ErrNotEnoughData, len(vectors), idx.nlist)
	}

	for i, vec := range vectors {
		if len(vec) != idx.dim {
			return fmt.Errorf("%w: vector %d tiene %d dimensiones, se esperaban %d",
				ErrInvalidDimension, i, len(vec), idx.dim)
		}
		if err := validateComponents(vec); err != nil {
			return fmt.Errorf("%w: vector %d", err, i)
		}
	}

	// Para Euclidiana kmeans solo lee los vectores, así que no hace falta
	// copiarlos. Para coseno sí necesitamos una copia normalizada porque
	// kmeans esférico normaliza los datos de entrada.
	data := vectors
	if idx.metric == MetricCosine {
		data = make([][]float32, len(vectors))
		for i, vec := range vectors {
			data[i] = append([]float32(nil), vec...)
			normalize32(data[i])
		}
	}

	centroids := kmeans(data, idx.nlist, idx.metric)
	flat := make([]float32, 0, idx.nlist*idx.dim)
	for _, centroid := range centroids {
		flat = append(flat, centroid...)
	}
	return idx.setCentroidsLocked(flat)
}

// kmeans agrupa data en k centroides. Es determinista (semilla fija) para que
// los tests y una reconstrucción del índice den el mismo resultado.
func kmeans(data [][]float32, k int, m MetricType) [][]float32 {
	rng := rand.New(rand.NewSource(1))
	dim := len(data[0])

	centroids := make([][]float32, 0, k)
	centroids = append(centroids, cloneVector(data[rng.Intn(len(data))]))

	// Inicialización k-means++
	dists := make([]float64, len(data))
	for i := range dists {
		dists[i] = math.Inf(1)
	}

	// Generando k centroides
	for len(centroids) < k {
		last := centroids[len(centroids)-1]
		total := 0.0
		for i, vec := range data {
			d := metricpkg.RankingDistance(vec, last, m)
			if d < dists[i] {
				dists[i] = d
			}
			total += dists[i]
		}

		pick := rng.Intn(len(data))
		if total > 0 {
			target := rng.Float64() * total
			for i, d := range dists {
				target -= d
				if target <= 0 {
					pick = i
					break
				}
			}
		}
		centroids = append(centroids, cloneVector(data[pick]))
	}

	labels := make([]int, len(data))
	for i := range labels {
		labels[i] = -1
	}

	// Entrenando data a los k centroides
	trainingData(k, dim, labels, data, centroids, TrainingData{maxIters: 100, tol: 1e-4}, m)

	return centroids
}

func trainingData(k int, dim int, labels []int, data [][]float32, centroids [][]float32, trData TrainingData, m MetricType) {
	for iter := 0; iter < trData.maxIters; iter++ {
		changed := 0
		for i, vec := range data {
			best := 0
			bestDist := metricpkg.RankingDistance(vec, centroids[0], m)
			for c := 1; c < k; c++ {
				d := metricpkg.RankingDistance(vec, centroids[c], m)
				if d < bestDist {
					bestDist = d
					best = c
				}
			}
			if labels[i] != best {
				labels[i] = best
				changed++
			}
		}

		sums := make([][]float64, k)
		counts := make([]int, k)
		for c := range sums {
			sums[c] = make([]float64, dim)
		}
		for i, vec := range data {
			c := labels[i]
			counts[c]++
			for j, value := range vec {
				sums[c][j] += float64(value)
			}
		}

		// Un cluster vacío se re-siembra con el punto más lejano a su centroide.
		// Cada punto se usa como máximo una vez por iteración y se descuenta
		// del cluster anterior para no desviar su media.
		reseeded := make([]bool, len(data))
		for c := 0; c < k; c++ {
			if counts[c] > 0 {
				continue
			}
			far := -1
			farDist := -1.0
			for i, vec := range data {
				if reseeded[i] {
					continue
				}
				d := metricpkg.RankingDistance(vec, centroids[labels[i]], m)
				if d > farDist {
					farDist = d
					far = i
				}
			}
			if far < 0 {
				continue
			}

			old := labels[far]
			labels[far] = c
			reseeded[far] = true
			counts[c] = 1
			for j := range sums[c] {
				sums[c][j] = float64(data[far][j])
			}
			if counts[old] > 0 {
				counts[old]--
				for j := range sums[old] {
					sums[old][j] -= float64(data[far][j])
				}
			}
		}

		maxShift := 0.0
		for c := 0; c < k; c++ {
			if counts[c] == 0 {
				continue
			}
			for j := 0; j < dim; j++ {
				next := float32(sums[c][j] / float64(counts[c]))
				shift := math.Abs(float64(next - centroids[c][j]))
				if shift > maxShift {
					maxShift = shift
				}
				centroids[c][j] = next
			}
			if m == MetricCosine {
				normalize32(centroids[c])
			}
		}

		if changed == 0 && maxShift < trData.tol {
			break
		}
	}
}

func cloneVector(vec []float32) []float32 {
	return append([]float32(nil), vec...)
}

func normalize32(vec []float32) {
	var norm float64
	for _, value := range vec {
		norm += float64(value) * float64(value)
	}
	if norm == 0 {
		return
	}

	// Calcular v / |v| de manera eficiente
	scale := float32(1 / math.Sqrt(norm))
	for i := range vec {
		vec[i] *= scale
	}
}

func (idx *IVFFlatIndex) Dim() int { return idx.dim }

func (idx *IVFFlatIndex) NList() int { return idx.nlist }

func (idx *IVFFlatIndex) Metric() MetricType { return idx.metric }

func (idx *IVFFlatIndex) SetCentroids(centroids []float32) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.setCentroidsLocked(centroids)
}

// setCentroidsLocked aplica los centroides. El llamador debe tener idx.mu.
func (idx *IVFFlatIndex) setCentroidsLocked(centroids []float32) error {
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

func validateMetric(m MetricType) error {
	if err := metricpkg.Validate(m); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidMetric, err)
	}
	return nil
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

func distance64(a, b []float32, m MetricType) float64 {
	return metricpkg.Distance(a, b, m)
}

// rankingDistance64 usa distancia al cuadrado para Euclidiana: es monótona y
// evita un sqrt por cada vector candidato.
func rankingDistance64(a, b []float32, m MetricType) float64 {
	return metricpkg.RankingDistance(a, b, m)
}

func finalDistance64(dist float64, m MetricType) float64 {
	return metricpkg.FinalDistance(dist, m)
}

// computeDistance se conserva como utilidad para pruebas y usos internos que
// necesiten la distancia final en float32.
func computeDistance(a, b []float32, m MetricType) float32 {
	return float32(distance64(a, b, m))
}
