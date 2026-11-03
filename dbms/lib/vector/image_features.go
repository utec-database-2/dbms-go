//go:build opencv

// Este archivo requiere OpenCV 4 y gocv. Compila con:
//
//	go build -tags opencv ./...
//
// Sin el tag se usa image_features_stub.go y el resto del proyecto compila
// sin OpenCV.
package vector

import (
	"fmt"
	"math"

	"gocv.io/x/gocv"
)

type ImageCVFeature struct {
	K        int
	Codebook [][]float64
	Distance func([]float64, []float64) float64
}

var _ Extractor = (*ImageCVFeature)(nil)

// Dim devuelve la dimensión del histograma BoW (K).
func (e *ImageCVFeature) Dim() int {
	return e.K
}

// defaultDistance usa distancia euclidiana si no se ha configurado.
func defaultDistance(a, b []float64) float64 {
	var sum float64
	for i := range a {
		if i >= len(b) {
			break
		}
		d := a[i] - b[i]
		sum += d * d
	}
	return math.Sqrt(sum)
}

// getDistance devuelve la función de distancia a usar.
func (e *ImageCVFeature) getDistance() func([]float64, []float64) float64 {
	if e.Distance != nil {
		return e.Distance
	}
	return defaultDistance
}

// Extract decodifica la imagen y devuelve los descriptores SIFT. El llamador
// debe cerrar el Mat devuelto con Close().
func (e *ImageCVFeature) Extract(bytes []byte) (gocv.Mat, error) {
	var object gocv.Mat
	err := gocv.IMDecodeIntoMat(bytes, gocv.IMReadColor, &object)
	if err != nil {
		return gocv.Mat{}, err
	}
	defer object.Close()

	if object.Empty() {
		return gocv.Mat{}, fmt.Errorf("imagen vacía o no válida")
	}

	sift := gocv.NewSIFT()
	defer sift.Close()

	mask := gocv.NewMat()
	defer mask.Close()

	_, descriptors := sift.DetectAndCompute(object, mask)
	defer descriptors.Close()
	if descriptors.Empty() {
		return gocv.Mat{}, fmt.Errorf("no se encontraron descriptores SIFT")
	}

	// Devolvemos una copia para que el llamador controle su ciclo de vida.
	return descriptors.Clone(), nil
}

// QueryHist devuelve el histograma BoW en float64 (K dimensiones), L2-normalizado.
func (e *ImageCVFeature) QueryHist(query []byte) ([]float64, error) {
	if e.K <= 0 {
		return nil, fmt.Errorf("K inválido")
	}
	if len(e.Codebook) == 0 {
		return nil, fmt.Errorf("el Codebook no ha sido entrenado")
	}

	mat, err := e.Extract(query)
	if err != nil {
		return nil, err
	}
	defer mat.Close()

	hist, err := e.Quantizate(mat)
	if err != nil {
		return nil, err
	}
	return hist, nil
}

// Query devuelve el histograma BoW convertido a []float32, listo para usar con IVF.
func (e *ImageCVFeature) Query(img []byte) ([]float32, error) {
	hist, err := e.QueryHist(img)
	if err != nil {
		return nil, err
	}

	vec := make([]float32, len(hist))
	for i, v := range hist {
		vec[i] = float32(v)
	}
	return vec, nil
}

// Vector es un alias de Query para encajar con la interfaz Extractor.
func (e *ImageCVFeature) Vector(img []byte) ([]float32, error) {
	return e.Query(img)
}

func (e *ImageCVFeature) Train(dataset [][]byte) ([][]float64, error) {
	if e.K <= 0 {
		return nil, fmt.Errorf("K inválido")
	}
	if len(dataset) == 0 {
		return nil, fmt.Errorf("dataset vacío")
	}

	var stackedMat gocv.Mat
	hasStacked := false
	defer func() {
		if hasStacked {
			stackedMat.Close()
		}
	}()

	used := 0
	for _, imgBytes := range dataset {
		desc, err := e.Extract(imgBytes)
		if err != nil || desc.Empty() {
			if !desc.Empty() {
				desc.Close()
			}
			continue
		}

		if used == 0 {
			stackedMat = desc.Clone()
			desc.Close()
			hasStacked = true
		} else {
			var next gocv.Mat
			if err := gocv.Vconcat(stackedMat, desc, &next); err != nil {
				desc.Close()
				return nil, fmt.Errorf("concatenando descriptores: %w", err)
			}
			stackedMat.Close()
			desc.Close()
			stackedMat = next
		}
		used++
	}

	if used == 0 || !hasStacked || stackedMat.Empty() {
		return nil, fmt.Errorf("no se pudieron extraer descriptores del dataset")
	}

	codebook, _, err := e.Partition(stackedMat)
	if err != nil {
		return nil, err
	}

	e.Codebook = codebook
	return e.Codebook, nil
}

func (im *ImageCVFeature) Partition(samples gocv.Mat) ([][]float64, float64, error) {
	if im.K <= 0 {
		return nil, 0, fmt.Errorf("K inválido")
	}
	if samples.Empty() || samples.Rows() < im.K {
		return nil, 0, fmt.Errorf("insuficientes descriptores (%d) para K=%d clusters", samples.Rows(), im.K)
	}

	input := samples
	var converted gocv.Mat
	if samples.Type() != gocv.MatTypeCV32F {
		converted = gocv.NewMat()
		defer converted.Close()
		samples.ConvertTo(&converted, gocv.MatTypeCV32F)
		input = converted
	}

	bestLabels := gocv.NewMat()
	defer bestLabels.Close()

	centers := gocv.NewMat()
	defer centers.Close()

	criteria := gocv.NewTermCriteria(gocv.Count+gocv.EPS, 100, 0.2)
	attempts := 10

	compactness := gocv.KMeans(
		input,
		im.K,
		&bestLabels,
		criteria,
		attempts,
		gocv.KMeansPPCenters,
		&centers,
	)

	sz0 := centers.Rows()
	sz1 := centers.Cols()
	ff := make([][]float64, sz0)

	for i := 0; i < sz0; i++ {
		f := make([]float64, sz1)
		for j := 0; j < sz1; j++ {
			f[j] = float64(centers.GetFloatAt(i, j))
		}
		ff[i] = f
	}

	return ff, compactness, nil
}

func (e *ImageCVFeature) Quantizate(desc gocv.Mat) ([]float64, error) {
	if desc.Empty() {
		return nil, fmt.Errorf("matriz de descriptores vacía")
	}
	if len(e.Codebook) == 0 {
		return nil, fmt.Errorf("el Codebook no ha sido entrenado")
	}
	if len(e.Codebook) != e.K {
		return nil, fmt.Errorf("codebook con %d centroides, se esperaban %d", len(e.Codebook), e.K)
	}

	input := desc
	var converted gocv.Mat
	if desc.Type() != gocv.MatTypeCV32F {
		converted = gocv.NewMat()
		defer converted.Close()
		desc.ConvertTo(&converted, gocv.MatTypeCV32F)
		input = converted
	}

	histogram := make([]float64, e.K)
	rows := input.Rows()
	cols := input.Cols()
	if len(e.Codebook[0]) != cols {
		return nil, fmt.Errorf("codebook de %d dimensiones, descriptores de %d", len(e.Codebook[0]), cols)
	}

	// Asignar cada descriptor al centroide más cercano del Codebook
	for i := 0; i < rows; i++ {
		descriptor := make([]float64, cols)
		for j := 0; j < cols; j++ {
			descriptor[j] = float64(input.GetFloatAt(i, j))
		}

		nearestIdx := e.FindNearestIdx(descriptor)
		if nearestIdx < 0 {
			continue
		}
		histogram[nearestIdx]++
	}

	// Normalizar L2 para evitar sesgos por tamaño de imagen
	var norm float64
	for _, val := range histogram {
		norm += val * val
	}
	norm = math.Sqrt(norm)

	if norm > 0 {
		for k := 0; k < e.K; k++ {
			histogram[k] /= norm
		}
	}

	return histogram, nil
}

func (im *ImageCVFeature) FindNearestIdx(descriptor []float64) int {
	if len(im.Codebook) == 0 {
		return -1
	}

	distance := im.getDistance()
	minDist := math.MaxFloat64
	bestIdx := -1

	for k, centroid := range im.Codebook {
		dist := distance(centroid, descriptor)
		if dist < minDist {
			minDist = dist
			bestIdx = k
		}
	}

	return bestIdx
}
