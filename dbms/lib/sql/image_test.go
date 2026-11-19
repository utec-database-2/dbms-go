package sql

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	metricpkg "github.com/dbms-go/v2/dbms/lib/vector/metric"
)

// fakeImageExtractor sustituye a OpenCV: el "descriptor" de una imagen es el
// histograma de sus bytes módulo K. Imágenes con bytes parecidos dan
// histogramas parecidos, que es lo que necesita el pipeline del motor.
type fakeImageExtractor struct {
	k       int
	trained int
}

func (f *fakeImageExtractor) Dim() int { return f.k }

func (f *fakeImageExtractor) Train(dataset [][]byte) ([][]float64, error) {
	f.trained++
	out := make([][]float64, f.k)
	for i := range out {
		out[i] = []float64{float64(i)}
	}
	return out, nil
}

func (f *fakeImageExtractor) Query(img []byte) ([]float32, error) {
	if len(img) == 0 {
		return nil, fmt.Errorf("imagen vacía")
	}
	vec := make([]float32, f.k)
	for _, b := range img {
		vec[int(b)%f.k]++
	}
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	for i := range vec {
		vec[i] = float32(float64(vec[i]) / norm)
	}
	return vec, nil
}

// useFakeExtractor instala el extractor falso y devuelve el contador de
// entrenamientos.
func useFakeExtractor(t *testing.T) *fakeImageExtractor {
	t.Helper()
	fake := &fakeImageExtractor{}
	prev := newImageExtractor
	newImageExtractor = func(k int, _ metricpkg.Metric, _ [][]float64) ImageExtractor {
		fake.k = k
		return fake
	}
	t.Cleanup(func() { newImageExtractor = prev })
	return fake
}

// writeImage crea un "archivo de imagen" con n0 bytes 0 y n1 bytes 1 (o 2 y 3
// si high), para que haya dos familias bien separadas.
func writeImage(t *testing.T, dir, name string, a, b byte, na, nb int) string {
	t.Helper()
	raw := append(make([]byte, 0, na+nb), []byte(strings.Repeat(string([]byte{a}), na))...)
	raw = append(raw, []byte(strings.Repeat(string([]byte{b}), nb))...)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestImageIndexKNN(t *testing.T) {
	fake := useFakeExtractor(t)
	imgs := t.TempDir()
	dbDir := t.TempDir()

	a1 := writeImage(t, imgs, "a1.img", 0, 1, 90, 10)
	a2 := writeImage(t, imgs, "a2.img", 0, 1, 80, 20)
	b1 := writeImage(t, imgs, "b1.img", 2, 3, 90, 10)
	b2 := writeImage(t, imgs, "b2.img", 2, 3, 80, 20)
	q := writeImage(t, imgs, "q.img", 0, 1, 85, 15)

	e, err := Open("app", Options{Dir: dbDir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if e != nil {
			e.Close()
		}
	}()

	run(t, e, "CREATE TABLE fotos (id INT PRIMARY KEY, ruta VARCHAR(255))")

	// Sin filas no hay con qué entrenar el codebook.
	if _, err := e.Exec("CREATE INDEX idx_img ON fotos USING IMAGE (ruta) WITH (k = 4)"); err == nil {
		t.Fatal("CREATE INDEX IMAGE sobre una tabla vacía debería fallar")
	}

	for i, p := range []string{a1, a2, b1, b2} {
		run(t, e, fmt.Sprintf("INSERT INTO fotos (id, ruta) VALUES (%d, '%s')", i+1, p))
	}

	res := run(t, e, "CREATE INDEX idx_img ON fotos USING IMAGE (ruta) WITH (k = 4, nlist = 2, nprobe = 2)")
	if !strings.Contains(res.Message, "image") {
		t.Fatalf("CREATE INDEX no creó un índice IMAGE: %s", res.Message)
	}
	if fake.trained != 1 {
		t.Fatalf("el codebook debía entrenarse una vez, se entrenó %d", fake.trained)
	}
	codebook := imageCodebookPath(dbDir, "fotos", "idx_img")
	if _, err := os.Stat(codebook); err != nil {
		t.Fatalf("falta el archivo del codebook: %v", err)
	}

	query := fmt.Sprintf("SELECT id FROM fotos ORDER BY vector_distance(ruta, IMAGE_VECTOR('%s')) LIMIT 2", q)
	res = run(t, e, query)
	if got := ids(res); !equalStrings(got, []string{"1", "2"}) {
		t.Fatalf("kNN de imagen: %v", got)
	}
	if !hasKind(res, StepIndexScan) || !strings.Contains(planText(res), "IMAGE") {
		t.Fatalf("el kNN no usó el índice IMAGE:\n%s", planText(res))
	}

	// Una imagen insertada después del índice entra con el codebook existente.
	c1 := writeImage(t, imgs, "c1.img", 0, 1, 88, 12)
	run(t, e, fmt.Sprintf("INSERT INTO fotos (id, ruta) VALUES (5, '%s')", c1))
	res = run(t, e, fmt.Sprintf("SELECT id FROM fotos ORDER BY vector_distance(ruta, IMAGE_VECTOR('%s')) LIMIT 1", q))
	if got := ids(res); !equalStrings(got, []string{"5"}) {
		t.Fatalf("la imagen nueva debía ser la más cercana: %v", got)
	}

	// Una ruta inexistente se rechaza.
	if _, err := e.Exec("INSERT INTO fotos (id, ruta) VALUES (6, '/no/existe.img')"); err == nil {
		t.Fatal("INSERT con una imagen ilegible debería fallar")
	}

	// vector_distance fuera del atajo de índice (con WHERE) también entiende rutas.
	res = run(t, e, fmt.Sprintf(
		"SELECT id FROM fotos WHERE id <= 4 ORDER BY vector_distance(ruta, IMAGE_VECTOR('%s')) LIMIT 2", q))
	if got := ids(res); !equalStrings(got, []string{"1", "2"}) {
		t.Fatalf("kNN con WHERE: %v", got)
	}

	// Reabrir reutiliza el codebook guardado: no se vuelve a entrenar.
	e.Close()
	e = nil
	e, err = Open("app", Options{Dir: dbDir})
	if err != nil {
		t.Fatal(err)
	}
	if fake.trained != 1 {
		t.Fatalf("al reabrir no debía reentrenarse (entrenamientos: %d)", fake.trained)
	}
	res = run(t, e, query)
	if got := ids(res); len(got) != 2 || !(got[0] == "5" || got[0] == "1" || got[0] == "2") {
		t.Fatalf("kNN tras reabrir: %v", got)
	}

	run(t, e, "DROP INDEX idx_img ON fotos")
	if _, err := os.Stat(codebook); !os.IsNotExist(err) {
		t.Fatalf("el codebook debía borrarse con el índice (err=%v)", err)
	}
}

func TestImageVectorNeedsIndex(t *testing.T) {
	useFakeExtractor(t)
	e := newTestEngine(t, Options{})
	run(t, e, "CREATE TABLE fotos (id INT PRIMARY KEY, ruta VARCHAR(255))")
	run(t, e, "INSERT INTO fotos (id, ruta) VALUES (1, 'x')")
	if _, err := e.Exec("SELECT IMAGE_VECTOR('x') FROM fotos"); err == nil ||
		!strings.Contains(err.Error(), "IMAGE") {
		t.Fatalf("IMAGE_VECTOR sin índice debía explicar qué falta: %v", err)
	}
}

func TestImageIndexRequiresPathColumn(t *testing.T) {
	useFakeExtractor(t)
	e := newTestEngine(t, Options{})
	run(t, e, "CREATE TABLE t (id INT PRIMARY KEY, n INT)")
	run(t, e, "INSERT INTO t (id, n) VALUES (1, 1)")
	if _, err := e.Exec("CREATE INDEX i ON t USING IMAGE (n)"); err == nil {
		t.Fatal("IMAGE sobre una columna INT debería fallar")
	}
}

func TestIVFRejectsImageOption(t *testing.T) {
	e := newTestEngine(t, Options{})
	run(t, e, "CREATE TABLE v (id INT PRIMARY KEY, e VECTOR(2))")
	if _, err := e.Exec("CREATE INDEX i ON v USING IVF (e) WITH (k = 4)"); err == nil {
		t.Fatal("la opción k es solo de IMAGE")
	}
}
