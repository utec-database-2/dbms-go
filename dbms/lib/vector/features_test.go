package vector

import (
	"testing"

	metricpkg "github.com/dbms-go/v2/dbms/lib/vector/metric"
)

type fakeExtractor struct {
	dim int
}

func (f fakeExtractor) Dim() int { return f.dim }

func (f fakeExtractor) Vector([]byte) ([]float32, error) {
	return make([]float32, f.dim), nil
}

func TestMetricOfDefault(t *testing.T) {
	if got := MetricOf(fakeExtractor{dim: 4}); got != metricpkg.Euclidean {
		t.Fatalf("MetricOf sin MetricProvider = %v, se esperaba Euclidean", got)
	}
}

func TestMetricOfImage(t *testing.T) {
	if got := MetricOf(&ImageCVFeature{K: 4}); got != metricpkg.Cosine {
		t.Fatalf("MetricOf(ImageCVFeature) = %v, se esperaba Cosine", got)
	}
}

func TestImageFeatureDim(t *testing.T) {
	feat := &ImageCVFeature{K: 7}
	if got := feat.Dim(); got != 7 {
		t.Fatalf("Dim() = %d, se esperaba 7", got)
	}
}

func TestConvertVectors(t *testing.T) {
	f32 := Float64ToFloat32([]float64{1.5, -2.25})
	if len(f32) != 2 || f32[0] != 1.5 || f32[1] != -2.25 {
		t.Fatalf("Float64ToFloat32 = %v", f32)
	}
	f64 := Float32ToFloat64(f32)
	if len(f64) != 2 || f64[0] != 1.5 || f64[1] != -2.25 {
		t.Fatalf("Float32ToFloat64 = %v", f64)
	}
	if got := Float64ToFloat32(nil); len(got) != 0 {
		t.Fatalf("Float64ToFloat32(nil) = %v", got)
	}
	if got := Float32ToFloat64(nil); len(got) != 0 {
		t.Fatalf("Float32ToFloat64(nil) = %v", got)
	}
}

func TestImageFeatureWithoutOpenCV(t *testing.T) {
	feat := &ImageCVFeature{K: 4}
	if _, err := feat.Train([][]byte{{1, 2, 3}}); err == nil {
		t.Fatal("Train sin OpenCV debería fallar")
	}
	if _, err := feat.Vector([]byte{1, 2, 3}); err == nil {
		t.Fatal("Vector sin OpenCV debería fallar")
	}
}
