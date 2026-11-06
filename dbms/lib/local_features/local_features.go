package local_features

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"github.com/dbms-go/v2/dbms/lib/local_features/mfcc"
	"github.com/dbms-go/v2/dbms/lib/local_features/sift"
)

const DefaultVocabularySize = 32

// Vocabulary contains the centroids and inverse document frequencies used to
// turn local descriptors into a fixed-size TF-IDF vector.
type Vocabulary struct {
	centroids [][]float64
	idf       []float64
	size      int
}

// NewVocabulary creates an empty vocabulary with k visual/audio words.
func NewVocabulary(k int) (*Vocabulary, error) {
	if k <= 0 {
		return nil, fmt.Errorf("vocabulary size must be positive: %d", k)
	}
	return &Vocabulary{size: k}, nil
}

// Fit learns k centroids and the IDF weights from a descriptor set per
// document. Every descriptor set must contain vectors of the same dimension.
func (v *Vocabulary) Fit(documents [][][]float64) error {
	if v == nil || v.size <= 0 {
		return fmt.Errorf("vocabulary size must be positive")
	}
	if len(documents) == 0 {
		return fmt.Errorf("cannot fit vocabulary without documents")
	}

	descriptors, dimension, err := validateDocuments(documents)
	if err != nil {
		return err
	}
	v.centroids = kmeans(descriptors, dimension, v.size)
	v.idf = make([]float64, v.size)

	documentFrequency := make([]int, v.size)
	for _, document := range documents {
		seen := make([]bool, v.size)
		for _, descriptor := range document {
			seen[nearestCentroid(descriptor, v.centroids)] = true
		}
		for word, present := range seen {
			if present {
				documentFrequency[word]++
			}
		}
	}
	for word, frequency := range documentFrequency {
		v.idf[word] = math.Log((float64(len(documents)+1) / float64(frequency+1))) + 1
	}
	return nil
}

// Transform quantizes descriptors, builds their normalized TF-IDF histogram,
// and always returns a vector with one component per vocabulary word.
func (v *Vocabulary) Transform(descriptors [][]float64) ([]float64, error) {
	if len(v.centroids) == 0 {
		return nil, fmt.Errorf("vocabulary has not been fitted")
	}
	if len(descriptors) == 0 {
		return make([]float64, len(v.centroids)), nil
	}
	dimension := len(v.centroids[0])
	histogram := make([]float64, len(v.centroids))
	for i, descriptor := range descriptors {
		if len(descriptor) != dimension {
			return nil, fmt.Errorf("descriptor %d has dimension %d, expected %d", i, len(descriptor), dimension)
		}
		histogram[nearestCentroid(descriptor, v.centroids)]++
	}
	scale := float64(len(descriptors))
	for word := range histogram {
		histogram[word] = histogram[word] / scale * v.idfValue(word)
	}
	return histogram, nil
}

// ExtractFeatures extracts local descriptors and returns a fixed-size TF-IDF
// vector. For corpus-aware IDF, fit a Vocabulary and use
// ExtractFeaturesWithVocabulary.
func ExtractFeatures(filename string) ([]float64, error) {
	vocabulary, err := NewVocabulary(DefaultVocabularySize)
	if err != nil {
		return nil, err
	}
	descriptors, err := extractDescriptors(filename)
	if err != nil {
		return nil, err
	}
	if err := vocabulary.Fit([][][]float64{descriptors}); err != nil {
		return nil, fmt.Errorf("fit feature vocabulary: %w", err)
	}
	return vocabulary.Transform(descriptors)
}

// ExtractFeaturesWithVocabulary extracts and quantizes an object using a
// vocabulary previously fitted on the search corpus.
func ExtractFeaturesWithVocabulary(filename string, vocabulary *Vocabulary) ([]float64, error) {
	if vocabulary == nil {
		return nil, fmt.Errorf("feature vocabulary cannot be nil")
	}
	descriptors, err := extractDescriptors(filename)
	if err != nil {
		return nil, err
	}
	return vocabulary.Transform(descriptors)
}

func extractDescriptors(filename string) ([][]float64, error) {
	switch {
	case isAudioFile(filename):
		features, _, _, err := mfcc.MFCCFromAudio(filename)
		if err != nil {
			return nil, fmt.Errorf("error extracting MFCC features: %w", err)
		}
		return features, nil
	case isImageFile(filename):
		img, err := sift.ReadImage(filename)
		if err != nil {
			return nil, fmt.Errorf("error reading image file %q: %w", filename, err)
		}
		defer img.Close()

		_, descriptors, err := sift.SIFTKeypointsAndDescriptors(img)
		if err != nil {
			return nil, fmt.Errorf("error extracting SIFT features: %w", err)
		}
		defer descriptors.Close()

		features := make([][]float64, descriptors.Rows())
		for i := range features {
			features[i] = make([]float64, descriptors.Cols())
			for j := range features[i] {
				features[i][j] = float64(descriptors.GetFloatAt(i, j))
			}
		}
		return features, nil
	default:
		return nil, fmt.Errorf("unsupported file type: %s", filename)
	}
}

func (v *Vocabulary) idfValue(word int) float64 {
	if len(v.idf) == 0 {
		return 1
	}
	return v.idf[word]
}

func validateDocuments(documents [][][]float64) ([]([]float64), int, error) {
	dimension := 0
	var all [][]float64
	for documentIndex, document := range documents {
		if len(document) == 0 {
			return nil, 0, fmt.Errorf("document %d has no descriptors", documentIndex)
		}
		if dimension == 0 {
			dimension = len(document[0])
			if dimension == 0 {
				return nil, 0, fmt.Errorf("descriptors cannot be empty")
			}
		}
		for descriptorIndex, descriptor := range document {
			if len(descriptor) != dimension {
				return nil, 0, fmt.Errorf("document %d descriptor %d has dimension %d, expected %d",
					documentIndex, descriptorIndex, len(descriptor), dimension)
			}
			all = append(all, descriptor)
		}
	}
	return all, dimension, nil
}

func kmeans(descriptors [][]float64, dimension, k int) [][]float64 {
	centroids := make([][]float64, k)
	for i := range centroids {
		centroids[i] = append([]float64(nil), descriptors[i%len(descriptors)]...)
	}

	for iteration := 0; iteration < 50; iteration++ {
		sums := make([][]float64, k)
		counts := make([]int, k)
		for i := range sums {
			sums[i] = make([]float64, dimension)
		}
		for _, descriptor := range descriptors {
			word := nearestCentroid(descriptor, centroids)
			counts[word]++
			for component, value := range descriptor {
				sums[word][component] += value
			}
		}

		changed := false
		for word := range centroids {
			if counts[word] == 0 {
				continue
			}
			for component := range centroids[word] {
				next := sums[word][component] / float64(counts[word])
				if math.Abs(next-centroids[word][component]) > 1e-9 {
					changed = true
				}
				centroids[word][component] = next
			}
		}
		if !changed {
			break
		}
	}
	return centroids
}

func nearestCentroid(descriptor []float64, centroids [][]float64) int {
	best := 0
	bestDistance := math.Inf(1)
	for index, centroid := range centroids {
		distance := 0.0
		for component, value := range descriptor {
			delta := value - centroid[component]
			distance += delta * delta
		}
		if distance < bestDistance {
			bestDistance = distance
			best = index
		}
	}
	return best
}

func isAudioFile(filename string) bool {
	return hasAudioExtension(filename)
}

func isImageFile(filename string) bool {
	return hasImageExtension(filename)
}

func hasAudioExtension(filename string) bool {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".wav", ".mp3", ".flac", ".ogg", ".oga", ".m4a", ".aac":
		return true
	default:
		return false
	}
}

func hasImageExtension(filename string) bool {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".bmp", ".tif", ".tiff", ".webp":
		return true
	default:
		return false
	}
}
