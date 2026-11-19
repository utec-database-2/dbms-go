// Comando imgcsv: recorre una carpeta de imágenes, extrae el vector de cada
// una y escribe un CSV listo para importar en una tabla con columna VECTOR:
//
//	id,nombre,categoria,archivo,v
//
// "archivo" es la ruta relativa al directorio --media del servidor, así el
// frontend puede mostrar la imagen. Por defecto usa el histograma de color
// (vector.ColorHistogram), el mismo que aplica el servidor a las imágenes que
// sube el usuario en la búsqueda multimedia.
//
//	go run ./dbms/cmd/imgcsv -dir datos_demo/imagenes -out datos_demo/imagenes.csv
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/dbms-go/v2/dbms/lib/vector"
)

func main() {
	dir := flag.String("dir", "datos_demo/imagenes", "carpeta con las imágenes (PNG, JPEG, GIF)")
	media := flag.String("media", "", "directorio --media del servidor (por defecto, el padre de -dir)")
	out := flag.String("out", "datos_demo/imagenes.csv", "CSV de salida")
	bins := flag.Int("bins", 4, "bins por canal del histograma (dimensión = bins³)")
	flag.Parse()

	if *media == "" {
		*media = filepath.Dir(filepath.Clean(*dir))
	}
	var files []string
	err := filepath.WalkDir(*dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".png", ".jpg", ".jpeg", ".gif":
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
	sort.Strings(files)
	if len(files) == 0 {
		log.Fatalf("no hay imágenes en %s", *dir)
	}

	f, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	_ = w.Write([]string{"id", "nombre", "categoria", "archivo", "v"})

	ext := vector.ColorHistogram{Bins: *bins}
	for i, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			log.Fatal(err)
		}
		vec, err := ext.Vector(raw)
		if err != nil {
			log.Fatalf("%s: %v", path, err)
		}
		rel, err := filepath.Rel(*media, path)
		if err != nil {
			rel = path
		}
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		category := filepath.Base(filepath.Dir(path))
		if before, _, ok := strings.Cut(name, "_"); ok {
			category = before
		}
		parts := make([]string, len(vec))
		for j, v := range vec {
			parts[j] = strconv.FormatFloat(float64(v), 'f', 5, 32)
		}
		_ = w.Write([]string{
			strconv.Itoa(i + 1), strings.ReplaceAll(name, "_", " "), category,
			filepath.ToSlash(rel), "[" + strings.Join(parts, " ") + "]",
		})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%d imágenes → %s (vectores de %d dimensiones)\n", len(files), *out, ext.Dim())
}
