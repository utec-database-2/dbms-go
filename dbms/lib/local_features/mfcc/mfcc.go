package mfcc

import (
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
