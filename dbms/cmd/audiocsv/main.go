package main

import (
	"encoding/csv"
	"encoding/gob"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/dbms-go/v2/dbms/lib/local_features"
	"github.com/dbms-go/v2/dbms/lib/vector"
)

func probeTags(path string) (title, genre string) {
	out, err := exec.Command(
		"ffprobe", "-v", "quiet",
		"-show_entries", "format_tags=genre,title",
		"-of", "default=noprint_wrappers=1",
		path,
	).Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
				key := strings.TrimPrefix(strings.ToLower(k), "tag:")
				switch key {
				case "title":
					title = sanitizeCSV(v)
				case "genre":
					genre = sanitizeCSV(v)
				}
			}
		}
	}
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if genre == "" {
		genre = "General"
	}
	return title, genre
}

func sanitizeCSV(s string) string {
	s = strings.ReplaceAll(s, "'", "")
	s = strings.ReplaceAll(s, "\"", "")
	s = strings.ReplaceAll(s, ",", " ")
	return strings.TrimSpace(s)
}

func loadGobVocab(path string) (*local_features.Vocabulary, error) {
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

func main() {
	srcDir := flag.String("src", "data/audio/fma_small", "carpeta origen con los WAV de FMA")
	vocabIn := flag.String("vocab", "data/audio/vocabulary.gob", "vocabulario entrenado (.gob)")
	media := flag.String("media", "datos_demo", "directorio --media del servidor")
	outCSV := flag.String("out", "datos_demo/audio.csv", "CSV de salida")
	perGenre := flag.Int("per-genre", 5, "cantidad de muestras por género")
	seconds := flag.Int("sec", 5, "duración en segundos de cada clip ligero")
	flag.Parse()

	vocab, err := loadGobVocab(*vocabIn)
	if err != nil {
		log.Fatalf("no se pudo leer %s: %v", *vocabIn, err)
	}

	// Guardar copia del vocabulario en datos_demo/audio/vocab.gob para que el servidor lo use al subir audios
	vocabOut := filepath.Join(*media, "audio", "vocab.gob")
	if err := os.MkdirAll(filepath.Dir(vocabOut), 0o755); err != nil {
		log.Fatal(err)
	}
	if rawGob, err := os.ReadFile(*vocabIn); err == nil {
		if err := os.WriteFile(vocabOut, rawGob, 0o644); err != nil {
			log.Fatal(err)
		}
	}

	var wavs []string
	err = filepath.WalkDir(*srcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.EqualFold(filepath.Ext(path), ".wav") {
			wavs = append(wavs, path)
		}
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
	sort.Strings(wavs)

	samplesDir := filepath.Join(*media, "audio", "muestras")
	if err := os.MkdirAll(samplesDir, 0o755); err != nil {
		log.Fatal(err)
	}

	f, err := os.Create(*outCSV)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	_ = w.Write([]string{"id", "titulo", "genero", "archivo", "v"})

	ext := &vector.AudioBoWFeature{K: vocab.Size(), Vocab: vocab}
	genreCounts := map[string]int{}
	id := 0

	for _, src := range wavs {
		if len(genreCounts) >= 8 {
			done := true
			for _, c := range genreCounts {
				if c < *perGenre {
					done = false
					break
				}
			}
			if done {
				break
			}
		}

		allowedGenres := map[string]bool{
			"Hip-Hop":       true,
			"Pop":           true,
			"Folk":          true,
			"Rock":          true,
			"Experimental":  true,
			"International": true,
			"Electronic":    true,
			"Instrumental":  true,
		}

		title, genre := probeTags(src)
		if !allowedGenres[genre] || genreCounts[genre] >= *perGenre {
			continue
		}

		base := filepath.Base(src)
		dst := filepath.Join(samplesDir, base)
		cmd := exec.Command(
			"ffmpeg", "-y", "-loglevel", "error",
			"-i", src,
			"-t", strconv.Itoa(*seconds),
			"-ar", "16000", "-ac", "1",
			dst,
		)
		if err := cmd.Run(); err != nil {
			continue
		}

		vec, err := ext.QueryFromPath(dst)
		if err != nil || len(vec) == 0 {
			_ = os.Remove(dst)
			continue
		}

		id++
		genreCounts[genre]++
		parts := make([]string, len(vec))
		for j, val := range vec {
			parts[j] = strconv.FormatFloat(float64(val), 'f', 5, 32)
		}
		_ = w.Write([]string{
			strconv.Itoa(id),
			title,
			genre,
			filepath.ToSlash(dst),
			"[" + strings.Join(parts, " ") + "]",
		})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%d pistas de audio (%d géneros) → %s (dim=%d)\n", id, len(genreCounts), *outCSV, vocab.Size())
}
