//go:build !opencv

package vector

import (
	"errors"

	metricpkg "github.com/dbms-go/v2/dbms/lib/vector/metric"
)

// ErrOpenCVDisabled indica que las features de imagen no están disponibles
// porque el binario se compiló sin el tag "opencv".
var ErrOpenCVDisabled = errors.New("image features: compilado sin OpenCV (use -tags opencv)")

// ImageCVFeature es el fallback cuando el proyecto se compila sin OpenCV.
// Mantiene la misma configuración pública que la implementación real para
// que el resto del código compile y pueda arrancar.
type ImageCVFeature struct {
	K        int
	Codebook [][]float64
	Metric   metricpkg.Metric
}

var (
	_ Extractor      = (*ImageCVFeature)(nil)
	_ MetricProvider = (*ImageCVFeature)(nil)
)

// Dim devuelve la dimensión configurada del histograma.
func (e *ImageCVFeature) Dim() int { return e.K }

// VectorMetric indica que los histogramas BoW se comparan con coseno.
func (e *ImageCVFeature) VectorMetric() metricpkg.Metric { return metricpkg.Cosine }

// Train no está disponible sin OpenCV.
func (e *ImageCVFeature) Train(dataset [][]byte) ([][]float64, error) {
	return nil, ErrOpenCVDisabled
}

// QueryHist no está disponible sin OpenCV.
func (e *ImageCVFeature) QueryHist(query []byte) ([]float64, error) {
	return nil, ErrOpenCVDisabled
}

// Query no está disponible sin OpenCV.
func (e *ImageCVFeature) Query(img []byte) ([]float32, error) {
	return nil, ErrOpenCVDisabled
}

// Vector no está disponible sin OpenCV.
func (e *ImageCVFeature) Vector(img []byte) ([]float32, error) {
	return nil, ErrOpenCVDisabled
}
