package metric

import (
	"errors"
	"fmt"
	"math"
)

type Metric uint8

const (
	// Euclidean ordena por distancia euclidiana: menor es más cercano.
	Euclidean Metric = iota
	// Cosine ordena por 1 - similitud coseno: menor es más cercano.
	Cosine
	// DotProduct ordena por -producto punto: menor es mayor similitud.
	DotProduct
)

// ErrInvalid indica una métrica desconocida.
var ErrInvalid = errors.New("metric: métrica inválida")

// Validate comprueba que la métrica sea una de las soportadas.
func Validate(m Metric) error {
	switch m {
	case Euclidean, Cosine, DotProduct:
		return nil
	default:
		return fmt.Errorf("%w: %d", ErrInvalid, m)
	}
}

// IsValid indica si la métrica es soportada.
func IsValid(m Metric) bool {
	return Validate(m) == nil
}

func (m Metric) String() string {
	switch m {
	case Euclidean:
		return "euclidean"
	case Cosine:
		return "cosine"
	case DotProduct:
		return "dot_product"
	default:
		return fmt.Sprintf("metric(%d)", uint8(m))
	}
}

// Float agrupa los tipos numéricos que usan los vectores del proyecto.
type Float interface {
	~float32 | ~float64
}

// Distance devuelve la distancia final según la métrica.
func Distance[T Float](a, b []T, m Metric) float64 {
	switch m {
	case Euclidean:
		return math.Sqrt(squaredEuclidean(a, b))
	case Cosine:
		var dot, normA, normB float64
		n := min(len(a), len(b))
		for i := 0; i < n; i++ {
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
	case DotProduct:
		var dot float64
		n := min(len(a), len(b))
		for i := 0; i < n; i++ {
			dot += float64(a[i]) * float64(b[i])
		}
		return -dot
	default:
		panic(fmt.Sprintf("metric: métrica inválida: %d", m))
	}
}

// RankingDistance devuelve la distancia usada para ordenar. Para Euclidiana
// usa la distancia al cuadrado: es monótona y evita un sqrt por candidato.
func RankingDistance[T Float](a, b []T, m Metric) float64 {
	if m == Euclidean {
		return squaredEuclidean(a, b)
	}
	return Distance(a, b, m)
}

// FinalDistance convierte una distancia de ranking a la distancia final.
func FinalDistance(dist float64, m Metric) float64 {
	if m == Euclidean {
		return math.Sqrt(dist)
	}
	return dist
}

func squaredEuclidean[T Float](a, b []T) float64 {
	var sum float64
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		diff := float64(a[i]) - float64(b[i])
		sum += diff * diff
	}
	return sum
}
