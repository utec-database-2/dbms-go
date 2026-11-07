//go:build opencv

package sql

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Prueba de punta a punta con OpenCV real (SIFT + k-means):
//
//	go test -tags opencv ./dbms/lib/sql/ -run TestImageIndexOpenCV -v
//
// Genera dos familias de imágenes sintéticas con PNG de la librería estándar:
// tableros de ajedrez (esquinas por todas partes) y bandas diagonales. Una
// consulta tipo tablero debe traer tableros primero.

func checkerPNG(t *testing.T, path string, cell, offset int) {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 256, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			v := uint8(235)
			if ((x+offset)/cell+(y+offset)/cell)%2 == 0 {
				v = 20
			}
			img.SetGray(x, y, color.Gray{Y: v})
		}
	}
	savePNG(t, path, img)
}

func stripesPNG(t *testing.T, path string, width, shift int) {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 256, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			v := uint8(235)
			if ((x+y+shift)/width)%2 == 0 {
				v = 20
			}
			img.SetGray(x, y, color.Gray{Y: v})
		}
	}
	savePNG(t, path, img)
}

func savePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func TestImageIndexOpenCV(t *testing.T) {
	imgs := t.TempDir()
	e := newTestEngine(t, Options{})
	run(t, e, "CREATE TABLE fotos (id INT PRIMARY KEY, ruta VARCHAR(255))")

	// ids 1-4: tableros de distinto tamaño; ids 5-8: bandas diagonales.
	for i, cell := range []int{24, 28, 32, 36} {
		p := filepath.Join(imgs, fmt.Sprintf("tablero%d.png", i))
		checkerPNG(t, p, cell, i*3)
		run(t, e, fmt.Sprintf("INSERT INTO fotos (id, ruta) VALUES (%d, '%s')", i+1, p))
	}
	for i, w := range []int{10, 14, 18, 22} {
		p := filepath.Join(imgs, fmt.Sprintf("bandas%d.png", i))
		stripesPNG(t, p, w, i*5)
		run(t, e, fmt.Sprintf("INSERT INTO fotos (id, ruta) VALUES (%d, '%s')", i+5, p))
	}

	run(t, e, "CREATE INDEX idx_fotos ON fotos USING IMAGE (ruta) WITH (k = 8, nlist = 2, nprobe = 2)")

	q := filepath.Join(imgs, "consulta.png")
	checkerPNG(t, q, 30, 7)
	res := run(t, e, fmt.Sprintf(
		"SELECT id, ruta FROM fotos ORDER BY vector_distance(ruta, IMAGE_VECTOR('%s')) LIMIT 4", q))
	t.Logf("vecinos: %v\n%s", ids(res), planText(res))
	if !strings.Contains(planText(res), "IMAGE") {
		t.Fatalf("no se usó el índice IMAGE:\n%s", planText(res))
	}
	first := ids(res)
	if len(first) == 0 || first[0] > "4" {
		t.Fatalf("el vecino más cercano de un tablero debía ser un tablero (ids 1-4): %v", first)
	}
}
