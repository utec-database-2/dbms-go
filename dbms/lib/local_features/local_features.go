package local_features

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dbms-go/v2/dbms/lib/local_features/mfcc"
	"github.com/dbms-go/v2/dbms/lib/local_features/sift"
)

func ExtractFeatures(filenameOBJ string) ([][]float64, error) {
	// identificar si el archivo es de audio o imagen
	if isAudioFile(filenameOBJ) {
		// extraer características MFCC del archivo de audio
		features, _, _, err := mfcc.MFCCFromWav(filenameOBJ)
		if err != nil {
			return nil, fmt.Errorf("error extracting MFCC features: %v", err)
		}
		return features, nil
	} else if isImageFile(filenameOBJ) {
		// extraer características SIFT del archivo de imagen
		img, err := sift.ReadImage(filenameOBJ)
		if err != nil {
			return nil, fmt.Errorf("error reading image file %q: %w", filenameOBJ, err)
		}
		defer img.Close()

		_, descriptors, err := sift.SIFTKeypointsAndDescriptors(img)
		if err != nil {
			return nil, fmt.Errorf("error extracting SIFT features: %v", err)
		}
		defer descriptors.Close()

		// convertir los descriptores a [][]float64
		features := make([][]float64, descriptors.Rows())
		for i := 0; i < descriptors.Rows(); i++ {
			row := make([]float64, descriptors.Cols())
			for j := 0; j < descriptors.Cols(); j++ {
				row[j] = float64(descriptors.GetFloatAt(i, j))
			}
			features[i] = row
		}
		return features, nil
	} else {
		return nil, fmt.Errorf("unsupported file type: %s", filenameOBJ)
	}
}

func isAudioFile(filename string) bool {
	// verificar la extensión del archivo para determinar si es un archivo de audio
	// por ejemplo, .wav, .mp3, .flac, etc.
	// implementar la lógica según sea necesario
	return hasAudioExtension(filename)
}

func isImageFile(filename string) bool {
	// verificar la extensión del archivo para determinar si es un archivo de imagen
	// por ejemplo, .jpg, .png, .gif, etc.
	// implementar la lógica según sea necesario
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
