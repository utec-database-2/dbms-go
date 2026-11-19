package vector

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"  // registra el decodificador GIF
	_ "image/jpeg" // registra el decodificador JPEG
	_ "image/png"  // registra el decodificador PNG
	"math"

	metricpkg "github.com/dbms-go/v2/dbms/lib/vector/metric"
)

// ColorHistogram es un extractor global y liviano: cuenta los píxeles en una
// grilla RGB de Bins×Bins×Bins celdas y normaliza el histograma (norma L2).
// No reemplaza a SIFT (que necesita OpenCV): sirve para la demo del
// frontend y como respaldo cuando el binario se compila sin el tag opencv,
// porque solo usa la biblioteca estándar.
type ColorHistogram struct {
	// Bins por canal; <= 0 usa 4 (64 dimensiones).
	Bins int
}

var (
	_ Extractor      = ColorHistogram{}
	_ MetricProvider = ColorHistogram{}
)

func (c ColorHistogram) bins() int {
	if c.Bins <= 0 {
		return 4
	}
	return c.Bins
}

// Dim devuelve Bins³.
func (c ColorHistogram) Dim() int {
	b := c.bins()
	return b * b * b
}

// VectorMetric recomienda coseno: el histograma queda normalizado.
func (c ColorHistogram) VectorMetric() metricpkg.Metric { return metricpkg.Cosine }

// Vector decodifica la imagen (PNG, JPEG o GIF) y devuelve su histograma.
func (c ColorHistogram) Vector(raw []byte) ([]float32, error) {
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("vector: no se pudo decodificar la imagen: %w", err)
	}
	return c.FromImage(img), nil
}

// FromImage calcula el histograma de una imagen ya decodificada.
func (c ColorHistogram) FromImage(img image.Image) []float32 {
	b := c.bins()
	hist := make([]float64, b*b*b)
	bounds := img.Bounds()
	// Se muestrea como máximo una grilla de ~256×256 píxeles: suficiente
	// para un histograma global y rápido con fotos grandes.
	step := 1
	if w := bounds.Dx(); w > 256 {
		step = w / 256
	}
	if h := bounds.Dy(); h/step > 256 {
		step = h / 256
	}
	bucket := func(v uint32) int {
		// v viene en 16 bits (0..65535).
		i := int(v) * b / 65536
		if i >= b {
			i = b - 1
		}
		return i
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y += step {
		for x := bounds.Min.X; x < bounds.Max.X; x += step {
			r, g, bl, a := img.At(x, y).RGBA()
			if a == 0 {
				continue
			}
			hist[(bucket(r)*b+bucket(g))*b+bucket(bl)]++
		}
	}
	var norm float64
	for _, v := range hist {
		norm += v * v
	}
	norm = math.Sqrt(norm)
	out := make([]float32, len(hist))
	if norm == 0 {
		return out
	}
	for i, v := range hist {
		out[i] = float32(v / norm)
	}
	return out
}
