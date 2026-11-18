package vector

import (
	"fmt"
	"math"
	"math/rand"
	"os"

	"github.com/dbms-go/v2/dbms/lib/local_features"
	"github.com/dbms-go/v2/dbms/lib/local_features/mfcc"
	metricpkg "github.com/dbms-go/v2/dbms/lib/vector/metric"
)

// AudioBoWFeature implementa Bag of Audio Words (MFCC + K-Means TF-IDF)
// compatible con las interfaces Extractor y MetricProvider del motor.
type AudioBoWFeature struct {
	K      int
	Metric metricpkg.Metric
	Vocab  *local_features.Vocabulary
}

var (
	_ Extractor      = (*AudioBoWFeature)(nil)
	_ MetricProvider = (*AudioBoWFeature)(nil)
)

func (e *AudioBoWFeature) Dim() int { return e.K }

func (e *AudioBoWFeature) VectorMetric() metricpkg.Metric {
	if metricpkg.IsValid(e.Metric) {
		return e.Metric
	}
	return metricpkg.Cosine
}

// QueryFromPath extrae el vector BoAW directamente desde una ruta de audio
// (evita archivos temporales si ya es .wav).
func (e *AudioBoWFeature) QueryFromPath(path string) ([]float32, error) {
	if e.K <= 0 {
		return nil, fmt.Errorf("audio: K inválido")
	}
	if e.Vocab == nil {
		return nil, fmt.Errorf("audio: el vocabulario no ha sido entrenado")
	}
	hist64, err := local_features.ExtractFeaturesWithVocabulary(path, e.Vocab)
	if err != nil {
		return nil, err
	}
	return Float64ToFloat32(hist64), nil
}

// Vector cumple la interfaz Extractor para bytes crudos (BLOB).
func (e *AudioBoWFeature) Vector(raw []byte) ([]float32, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("audio: bytes vacíos")
	}
	pattern := "dbms-audio-*.audio"
	if len(raw) >= 12 && string(raw[0:4]) == "RIFF" && string(raw[8:12]) == "WAVE" {
		pattern = "dbms-audio-*.wav"
	}
	tmp, err := os.CreateTemp("", pattern)
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	return e.QueryFromPath(tmpPath)
}

// TrainFromPaths entrena K-Means + IDF con una muestra submuestreada de rutas
// para no saturar la RAM ni tardar minutos al ejecutar CREATE INDEX.
func (e *AudioBoWFeature) TrainFromPaths(paths []string) ([]byte, error) {
	if e.K <= 0 {
		return nil, fmt.Errorf("audio: K inválido")
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("audio: dataset vacío")
	}

	shuffled := append([]string(nil), paths...)
	rand.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})

	const maxSamples = 250
	const stride = 6

	var docs [][][]float64
	for _, p := range shuffled {
		if len(docs) >= maxSamples {
			break
		}
		frames, _, _, err := mfcc.MFCCFromAudio(p)
		if err != nil || !validMFCC(frames) {
			continue
		}
		sampled := make([][]float64, 0, len(frames)/stride+1)
		for i := 0; i < len(frames); i += stride {
			sampled = append(sampled, frames[i])
		}
		if len(sampled) > 0 {
			docs = append(docs, sampled)
		}
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("audio: no se pudieron extraer frames MFCC válidos")
	}

	vocab, err := local_features.NewVocabulary(e.K)
	if err != nil {
		return nil, err
	}
	if err := vocab.Fit(docs); err != nil {
		return nil, err
	}
	e.Vocab = vocab
	return vocab.GobEncode()
}

func (e *AudioBoWFeature) LoadVocabJSON(data []byte) error {
	vocab := &local_features.Vocabulary{}
	if err := vocab.GobDecode(data); err != nil {
		return err
	}
	e.Vocab = vocab
	return nil
}

func validMFCC(frames [][]float64) bool {
	if len(frames) == 0 {
		return false
	}
	for _, f := range frames {
		for _, v := range f {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return false
			}
		}
	}
	return true
}
