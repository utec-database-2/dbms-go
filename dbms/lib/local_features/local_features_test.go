package local_features

import (
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
