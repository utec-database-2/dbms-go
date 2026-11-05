package vector

import (
	metricpkg "github.com/dbms-go/v2/dbms/lib/vector/metric"
)

// Extractor es la interfaz común para cualquier extractor de features.
// El vector resultante debe tener dimensión fija para poder indexarse con IVF.
type Extractor interface {
	Dim() int
	Vector(raw []byte) ([]float32, error)
}

// MetricProvider permite que un extractor declare la métrica recomendada para
// los vectores que produce. Si no la implementa, se asume Euclidiana.
type MetricProvider interface {
	VectorMetric() metricpkg.Metric
}

// MetricOf devuelve la métrica recomendada por el extractor.
func MetricOf(ext Extractor) metricpkg.Metric {
	if provider, ok := ext.(MetricProvider); ok {
		return provider.VectorMetric()
	}
	return metricpkg.Euclidean
}

// Float64ToFloat32 convierte un vector float64 a float32.
func Float64ToFloat32(vec []float64) []float32 {
	if len(vec) == 0 {
		return []float32{}
	}
	out := make([]float32, len(vec))
	for i, v := range vec {
		out[i] = float32(v)
	}
	return out
}

// Float32ToFloat64 convierte un vector float32 a float64.
func Float32ToFloat64(vec []float32) []float64 {
	if len(vec) == 0 {
		return []float64{}
	}
	out := make([]float64, len(vec))
	for i, v := range vec {
		out[i] = float64(v)
	}
	return out
}
