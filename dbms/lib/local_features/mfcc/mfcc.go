package mfcc

import (
	"fmt"
	"os/exec"

	external "github.com/zrma/go-mfcc/mfcc"
)

// Config is the configuration accepted by the external MFCC extractor.
type Config = external.Config

// MFCC extrae un vector MFCC por cada frame de muestras PCM.
func MFCC(samples []float64, sampleRate int) ([][]float64, error) {
	features, _, err := external.ComputeMFCC(samples, sampleRate)
	return features, err
}

// MFCCWithConfig extrae vectores MFCC usando la configuración proporcionada.
func MFCCWithConfig(samples []float64, sampleRate int, config *Config) ([][]float64, error) {
	cfg := external.DefaultConfig()
	if config != nil {
		cfg = *config
	}
	features, _, err := external.ComputeMFCCWithConfig(samples, sampleRate, cfg)
	return features, err
}

// MFCCFeatures devuelve todos los vectores de frame concatenados en orden de frame.
func MFCCFeatures(samples []float64, sampleRate int) ([]float64, error) {
	return flatten(MFCC(samples, sampleRate))
}

// MFCCFeaturesWithConfig es el equivalente aplanado de MFCCWithConfig.
func MFCCFeaturesWithConfig(samples []float64, sampleRate int, config *Config) ([]float64, error) {
	return flatten(MFCCWithConfig(samples, sampleRate, config))
}

// ReadWavMono lee un archivo WAV y devuelve muestras PCM mono normalizadas.
func ReadWavMono(path string) ([]float64, int, error) {
	return external.ReadWavMono(path)
}

// MFCCFromWav extrae vectores MFCC directamente de un archivo WAV mono o estéreo.
func MFCCFromWav(path string) ([][]float64, int, int, error) {
	return external.ComputeMFCCFromWav(path)
}

func flatten(frames [][]float64, err error) ([]float64, error) {
	if err != nil {
		return nil, err
	}
	if len(frames) == 0 {
		return []float64{}, nil
	}

	features := make([]float64, 0, len(frames)*len(frames[0]))
	for _, frame := range frames {
		features = append(features, frame...)
	}
	return features, nil
}

func convertToWav(inputFile, outputFileName string) error {
	// Comando: ffmpeg -i input.mp3 -c:a pcm_s16le output.wav
	outputFile := outputFileName + ".wav"
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("ffmpeg no está instalado o no se encuentra en el PATH: %w", err)
	}
	// chequear si los nombres de archivo de entrada y salida son válidos
	if inputFile == "" || outputFile == "" {
		return fmt.Errorf("los nombres de archivo de entrada y salida no pueden estar vacíos")
	}
	// chequear si el inputFile ya es un archivo WAV
	if len(inputFile) >= 4 && inputFile[len(inputFile)-4:] == ".wav" {
		return fmt.Errorf("el archivo de entrada ya es un archivo WAV")
	}
	// chequear si el archivo de entrada y salida son iguales
	if inputFile == outputFile {
		return fmt.Errorf("los nombres de archivo de entrada y salida no pueden ser iguales")
	}
	// chequear si el archivo de entreada existe
	if _, err := exec.Command("test", "-f", inputFile).Output(); err != nil {
		return fmt.Errorf("el archivo de entrada no existe: %w", err)
	}
	// chequear si el archivo de salida ya existe
	if _, err := exec.Command("test", "-f", outputFile).Output(); err == nil {
		return fmt.Errorf("el archivo de salida ya existe")
	}
	// Chequear si el archivo de entrada es un archivo de audio válido (mp3, wav, etc.)
	if _, err := exec.Command("file", "--mime-type", inputFile).Output(); err != nil {
		return fmt.Errorf("no se pudo determinar el tipo de archivo de entrada: %w", err)
	}
	cmd := exec.Command("ffmpeg", "-i", inputFile, "-c:a", "pcm_s16le", outputFile)
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("error al convertir el archivo: %w", err)
	}
	return nil
}
