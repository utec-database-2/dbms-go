package main

// Este programa genera un conjunto de características de audio (MFCC + Bag of Audio Words)
// a partir de un conjunto de archivos WAV. Se utiliza para crear un vocabulario global
// y extraer vectores de características para cada archivo de audio.

// Origen del dataset de audio:
// - FMA: Free Music Archive
// - subconjunto de 8,000 pistas de audio de 30 segundos cada una.
// - Descarga oficial: https://github.com/mdeff/fma (archivo fma_small.zip).
// - Preparación: los archivos originales vienen en .mp3 y fueron convertidos a .wav

// Requisitos:
// - Coloca los archivos WAV en la carpeta "data/audio/fma_small".
// - Ajusta la constante maxTrainSamples para limitar el número de archivos usados para entrenar el vocabulario.
// - Ajusta vocabSize para cambiar la dimensión del vector BoAW (por ejemplo, 32, 64, 128).
// - Ejecuta este programa y generará "vocabulary.gob" y "audio_features.gob" en "data/audio".

// Ejecución (desde la raiz del repositorio dbms-go):
// go run datos_demo/audio/audio_features_gen.go

// Salidas generadas en data/audio:
// - vocabulary.gob: centroides y pesos IDF del vocabulario entrenado.
// - audio_features.gob: un slice de AudioRecord con TrackID, Path y Vector ([]float64) por pista.

import (
	"encoding/gob"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"math/rand"

	"github.com/dbms-go/v2/dbms/lib/local_features"
	"github.com/dbms-go/v2/dbms/lib/local_features/mfcc"
)

const (
	folder          = "data/audio"
	maxTrainSamples = 600 // Suficiente para entrenar K-Means rápido y sin saturar RAM
	vocabSize       = 128 // Puedes usar local_features.DefaultVocabularySize (32) o 64/128
)

type AudioRecord struct {
	TrackID string
	Path    string
	Vector  []float64
}

func GenerateAudioFeatures() ([]AudioRecord, error) {
	trainFolder := filepath.Join(folder, "fma_small")

	// 1. Listar todos los archivos .wav de las subcarpetas
	var wavPaths []string
	err := filepath.WalkDir(trainFolder, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(d.Name()), ".wav") {
			wavPaths = append(wavPaths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("error recorriendo %s: %w", trainFolder, err)
	}
	fmt.Printf("Archivos WAV encontrados: %d\n", len(wavPaths))

	rand.Shuffle(len(wavPaths), func(i, j int) {
		wavPaths[i], wavPaths[j] = wavPaths[j], wavPaths[i]
	})

	// 2. Extraer MFCC de una muestra para entrenar el vocabulario
	var trainDocs [][][]float64
	for _, path := range wavPaths {
		if len(trainDocs) >= maxTrainSamples {
			break
		}
		frames, _, _, err := mfcc.MFCCFromWav(path)
		if err != nil || len(frames) == 0 {
			continue // Evita que Fit falle si hay un audio vacío o corrupto
		}
		trainDocs = append(trainDocs, frames)
	}

	// 3. Ajustar (Fit) y guardar el vocabulario global
	vocabulary, err := local_features.NewVocabulary(vocabSize)
	if err != nil {
		return nil, fmt.Errorf("error creando vocabulario: %w", err)
	}
	if err := vocabulary.Fit(trainDocs); err != nil {
		return nil, fmt.Errorf("error ajustando vocabulario: %w", err)
	}
	trainDocs = nil // Liberar memoria de los frames crudos

	vocabPath := filepath.Join(folder, "vocabulary.gob")
	if err := saveVocabulary(vocabPath, vocabulary); err != nil {
		return nil, fmt.Errorf("error guardando vocabulario: %w", err)
	}

	// 4. Vectorizar los 7,997 audios a Bag of Audio Words ([]float64 de dimensión K)
	records := make([]AudioRecord, 0, len(wavPaths))
	for _, path := range wavPaths {
		frames, _, _, err := mfcc.MFCCFromWav(path)
		if err != nil || len(frames) == 0 {
			continue
		}
		vec, err := vocabulary.Transform(frames)
		if err != nil {
			continue
		}
		trackID := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		records = append(records, AudioRecord{
			TrackID: trackID,
			Path:    path,
			Vector:  vec,
		})
	}

	return records, nil
}

func saveVocabulary(path string, v *local_features.Vocabulary) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return gob.NewEncoder(f).Encode(v)
}

func LoadVocabulary(path string) (*local_features.Vocabulary, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var v local_features.Vocabulary
	if err := gob.NewDecoder(f).Decode(&v); err != nil {
		return nil, err
	}
	return &v, nil
}

func saveGob(path string, data any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return gob.NewEncoder(f).Encode(data)
}

func loadAudioRecords(path string) ([]AudioRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var records []AudioRecord
	if err := gob.NewDecoder(f).Decode(&records); err != nil {
		return nil, err
	}
	return records, nil
}

func main() {
	records, err := GenerateAudioFeatures()
	if err != nil {
		fmt.Printf("Error generando características de audio: %v\n", err)
		return
	}

	recordsPath := filepath.Join(folder, "audio_features.gob")
	if err := saveGob(recordsPath, records); err != nil {
		fmt.Printf("Error guardando características de audio: %v\n", err)
		return
	}

	fmt.Printf("Se generaron características para %d archivos de audio.\n", len(records))
}
