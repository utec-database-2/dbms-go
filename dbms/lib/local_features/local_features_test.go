package local_features

import (
	"math"
	"strings"
	"testing"
)

func TestFileTypeByExtension(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		isAudio  bool
		isImage  bool
	}{
		{"wav", "samples/audio.WAV", true, false},
		{"mp3", "samples/audio.mp3", true, false},
		{"flac", "samples/audio.flac", true, false},
		{"jpeg", "samples/photo.JpEg", false, true},
		{"png", "samples/photo.png", false, true},
		{"unknown", "samples/document.pdf", false, false},
		{"missing extension", "samples/document", false, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isAudioFile(test.filename); got != test.isAudio {
				t.Fatalf("isAudioFile(%q) = %t, expected %t", test.filename, got, test.isAudio)
			}
			if got := isImageFile(test.filename); got != test.isImage {
				t.Fatalf("isImageFile(%q) = %t, expected %t", test.filename, got, test.isImage)
			}
		})
	}
}

func TestExtractFeaturesRejectsUnsupportedFileType(t *testing.T) {
	features, err := ExtractFeatures("input.pdf")
	if err == nil {
		t.Fatal("ExtractFeatures expected an error for an unsupported file type")
	}
	if features != nil {
		t.Fatalf("features = %v, expected nil", features)
	}
	if !strings.Contains(err.Error(), "unsupported file type") {
		t.Fatalf("error = %q, expected unsupported file type", err)
	}
}

func TestExtractFeaturesReportsImageReadError(t *testing.T) {
	features, err := ExtractFeatures("missing.png")
	if err == nil {
		t.Fatal("ExtractFeatures expected an error for a missing image")
	}
	if features != nil {
		t.Fatalf("features = %v, expected nil", features)
	}
	if !strings.Contains(err.Error(), "error reading image file") {
		t.Fatalf("error = %q, expected image read error", err)
	}
}

func TestExtractFeaturesReportsAudioReadError(t *testing.T) {
	features, err := ExtractFeatures("missing.wav")
	if err == nil {
		t.Fatal("ExtractFeatures expected an error for a missing audio file")
	}
	if features != nil {
		t.Fatalf("features = %v, expected nil", features)
	}
	if !strings.Contains(err.Error(), "error extracting MFCC features") {
		t.Fatalf("error = %q, expected MFCC extraction error", err)
	}
}

func TestVocabularyProducesKDimensionalTFIDFVector(t *testing.T) {
	vocabulary, err := NewVocabulary(3)
	if err != nil {
		t.Fatalf("NewVocabulary returned error: %v", err)
	}

	documents := [][][]float64{
		{{0, 0}, {0.1, 0}},
		{{10, 10}, {9.9, 10}},
		{{0, 0.2}, {10, 9.8}},
	}
	if err := vocabulary.Fit(documents); err != nil {
		t.Fatalf("Fit returned error: %v", err)
	}

	vector, err := vocabulary.Transform(documents[0])
	if err != nil {
		t.Fatalf("Transform returned error: %v", err)
	}
	if len(vector) != 3 {
		t.Fatalf("vector length = %d, expected 3", len(vector))
	}
	for index, value := range vector {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			t.Fatalf("vector[%d] is not finite: %v", index, value)
		}
	}
}

func TestVocabularyRejectsInvalidDescriptors(t *testing.T) {
	vocabulary, err := NewVocabulary(2)
	if err != nil {
		t.Fatalf("NewVocabulary returned error: %v", err)
	}
	if err := vocabulary.Fit([][][]float64{{{1, 2}, {3}}}); err == nil {
		t.Fatal("Fit expected an error for inconsistent descriptor dimensions")
	}

	if _, err := vocabulary.Transform([][]float64{{1, 2}}); err == nil {
		t.Fatal("Transform expected an error for an unfitted vocabulary")
	}
}

func TestVocabularyEmptyDocumentReturnsZeroVector(t *testing.T) {
	vocabulary, err := NewVocabulary(2)
	if err != nil {
		t.Fatalf("NewVocabulary returned error: %v", err)
	}
	if err := vocabulary.Fit([][][]float64{
		{{0, 0}},
		{{1, 1}},
	}); err != nil {
		t.Fatalf("Fit returned error: %v", err)
	}

	vector, err := vocabulary.Transform(nil)
	if err != nil {
		t.Fatalf("Transform returned error: %v", err)
	}
	if len(vector) != 2 || vector[0] != 0 || vector[1] != 0 {
		t.Fatalf("empty transform = %v, expected [0 0]", vector)
	}
}
