package mfcc

import (
	"math"
	"os"
	"os/exec"
	"testing"
	"time"
)

func sineWave(samples, sampleRate int, frequency float64) []float64 {
	result := make([]float64, samples)
	for i := range result {
		result[i] = math.Sin(2 * math.Pi * frequency * float64(i) / float64(sampleRate))
	}
	return result
}

func TestMFCCShapeAndFiniteValues(t *testing.T) {
	samples := sineWave(16000, 16000, 440)

	features, err := MFCC(samples, 16000)
	if err != nil {
		t.Fatalf("MFCC returned error: %v", err)
	}
	if len(features) != 98 {
		t.Fatalf("frame count = %d, expected 98", len(features))
	}
	for i, frame := range features {
		if len(frame) != 13 {
			t.Fatalf("frame %d has %d coefficients, expected 13", i, len(frame))
		}
		for j, value := range frame {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				t.Fatalf("feature [%d][%d] is not finite: %v", i, j, value)
			}
		}
	}
}

func TestMFCCFeaturesFlattensFrames(t *testing.T) {
	samples := sineWave(800, 8000, 250)

	frames, err := MFCC(samples, 8000)
	if err != nil {
		t.Fatalf("MFCC returned error: %v", err)
	}
	flattened, err := MFCCFeatures(samples, 8000)
	if err != nil {
		t.Fatalf("MFCCFeatures returned error: %v", err)
	}
	if len(flattened) != len(frames)*13 {
		t.Fatalf("flattened length = %d, expected %d", len(flattened), len(frames)*13)
	}
	for i, value := range flattened {
		if value != frames[i/13][i%13] {
			t.Fatalf("flattened value %d differs from frame representation", i)
		}
	}
}

func TestMFCCWithConfig(t *testing.T) {
	samples := sineWave(1000, 10000, 1000)
	features, err := MFCCWithConfig(samples, 10000, &Config{
		WindowDuration:  20 * time.Millisecond,
		HopDuration:     10 * time.Millisecond,
		NumCoefficients: 5,
		NumFilters:      10,
		PreEmphasis:     0.95,
	})
	if err != nil {
		t.Fatalf("MFCCWithConfig returned error: %v", err)
	}
	if len(features) != 9 || len(features[0]) != 5 {
		t.Fatalf("unexpected shape: %dx%d", len(features), len(features[0]))
	}
}

func TestMFCCRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name       string
		samples    []float64
		sampleRate int
	}{
		{"empty samples", nil, 16000},
		{"invalid sample rate", []float64{0}, 0},
		{"nan sample", []float64{math.NaN()}, 16000},
		{"infinite sample", []float64{math.Inf(1)}, 16000},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := MFCC(test.samples, test.sampleRate); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestConvertToWav(t *testing.T) {
	// crear un archivo de prueba WAV a partir de un archivo MP3 de prueba

	// Nota: Este test requiere que ffmpeg esté instalado y disponible en el PATH.
	// buscar alternativas si ffmpeg no está disponible en el entorno de prueba.
	inputFile := "testdata/test.mp3"
	outputFileName := "testdata/test"

	if _, err := os.Stat(inputFile); os.IsNotExist(err) {
		t.Skipf("test fixture does not exist: %s", inputFile)
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}

	err := convertToWav(inputFile, outputFileName)
	if err != nil {
		t.Fatalf("convertToWav returned error: %v", err)
	}
	// verify that the output file exists and is a valid WAV file.
	outputFile := outputFileName + ".wav"
	if _, err := os.Stat(outputFile); os.IsNotExist(err) {
		t.Fatalf("output WAV file does not exist: %s", outputFile)
	}
	outputFileInfo, err := os.Stat(outputFile)
	if err != nil {
		t.Fatalf("error stating output WAV file: %v", err)
	}
	if outputFileInfo.Size() == 0 {
		t.Fatalf("output WAV file is empty: %s", outputFile)
	}

	// Clean up the output file after the test.
	defer os.Remove(outputFile)
}
