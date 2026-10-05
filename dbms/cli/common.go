package main

import (
	"encoding/csv"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dbms-go/v2/dbms/lib/storage"
)

// ---------------------------------------------------------------------------
// Esquema y datos
// ---------------------------------------------------------------------------

// benchTable es el esquema de las tablas de los benchmarks de la parte 1:
// id (clave primaria), k (columna secundaria indexable) y nombre.
var benchTable = storage.Table{
	Name:    "bench",
	Columns: []string{"id", "k", "nombre"},
	Types:   []storage.Type{storage.TypeInt32, storage.TypeInt32, storage.TypeString},
	MaxLen:  []int{0, 0, 16},
	KeyCols: []int{0},
}

// makeRows genera n filas con identificadores 0..n-1 y una columna k dispersa.
func makeRows(n int, seed int64) []storage.Tuple {
	rng := rand.New(rand.NewSource(seed))
	rows := make([]storage.Tuple, n)
	for i := 0; i < n; i++ {
		rows[i] = storage.Tuple{int32(i), int32(rng.Intn(n)), fmt.Sprintf("fila-%07d", i)}
	}
	return rows
}

// randomSegundos genera valores de la columna k, que es la que indexan el B+ no
// agrupado y el hash. Se necesitan claves propias: buscar por la clave primaria
// en un índice secundario no tiene sentido.
func randomSegundos(n, q int, seed int64) []int32 {
	if q > n {
		q = n
	}
	rng := rand.New(rand.NewSource(seed))
	perm := rng.Perm(n)
	out := make([]int32, 0, q)
	for _, p := range perm[:q] {
		out = append(out, int32(p))
	}
	return out
}

// shuffleRows devuelve las filas en orden de clave aleatorio, que es el caso
// duro para un archivo ordenado: provoca overflow, tombstones y reconstrucción.
func shuffleRows(rows []storage.Tuple, seed int64) []storage.Tuple {
	rng := rand.New(rand.NewSource(seed + 1))
	out := append([]storage.Tuple(nil), rows...)
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// ---------------------------------------------------------------------------
// Utilidades de medición
// ---------------------------------------------------------------------------

// cronómetro de alta resolución para las mediciones.
func now() time.Time { return time.Now() }

func ms(d time.Duration) float64 { return float64(d.Nanoseconds()) / 1e6 }

func us(d time.Duration) float64 { return float64(d.Nanoseconds()) / 1e3 }

// medirMem ejecuta f y devuelve los bytes de heap que quedan vivos después. Se
// fuerza un GC antes y después para que la medida sea estable y no dependa del
// momento en que el recolector decida actuar.
func medirMem(f func() error) (usados uint64, err error) {
	runtime.GC()
	antes := memLive()
	if err := f(); err != nil {
		return 0, err
	}
	runtime.GC()
	return memLive() - antes, nil
}

// memLive es la memoria viva del proceso: la que el recolector no considers
// liberable, no la que el proceso ha pedido al sistema.
func memLive() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// ---------------------------------------------------------------------------
// Archivos: tamaño en disco
// ---------------------------------------------------------------------------

// fileSize suma el tamaño de todos los archivos cuyo nombre empieza por base.
func fileSize(base string, suffixes ...string) (int64, error) {
	var total int64
	paths := make([]string, 0, len(suffixes))
	for _, s := range suffixes {
		paths = append(paths, base+s)
	}
	if len(paths) == 0 {
		paths = []string{base}
	}
	for _, p := range paths {
		info, err := os.Stat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return 0, err
		}
		total += info.Size()
	}
	return total, nil
}

func mustDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("no se pudo crear %s: %w", dir, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// CSV de resultados
// ---------------------------------------------------------------------------

// csvWriter acumula filas y las guarda con cabecera.
type csvWriter struct {
	path   string
	header []string
	rows   [][]string
}

func newCSV(out, name string, header ...string) (*csvWriter, error) {
	if err := mustDir(out); err != nil {
		return nil, err
	}
	return &csvWriter{path: filepath.Join(out, name), header: header}, nil
}

func (c *csvWriter) add(cells ...string) {
	c.rows = append(c.rows, cells)
}

func (c *csvWriter) save() error {
	f, err := os.Create(c.path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	if err := w.Write(c.header); err != nil {
		return err
	}
	for _, r := range c.rows {
		if err := w.Write(r); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

// readCSV lee un CSV de resultados. Si no existe devuelve nil.
func readCSV(path string) ([][]string, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	return r.ReadAll()
}

func f2s(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func i2s(v int) string { return strconv.Itoa(v) }

func i64s(v int64) string { return strconv.FormatInt(v, 10) }

// ---------------------------------------------------------------------------
// Presentación en terminal
// ---------------------------------------------------------------------------

// tabla imprime una tabla con las columnas alineadas.
func tabla(titulo string, cabeceras []string, filas [][]string) {
	fmt.Printf("\n%s\n", titulo)
	anchos := make([]int, len(cabeceras))
	for i, h := range cabeceras {
		anchos[i] = len(h)
	}
	for _, f := range filas {
		for i, c := range f {
			if i < len(anchos) && len(c) > anchos[i] {
				anchos[i] = len(c)
			}
		}
	}
	var sep strings.Builder
	for _, a := range anchos {
		sep.WriteString(strings.Repeat("-", a+2))
		sep.WriteByte('-')
	}
	fmt.Println(sep.String())

	linea := func(cells []string) {
		var b strings.Builder
		for i, a := range anchos {
			c := ""
			if i < len(cells) {
				c = cells[i]
			}
			if i == len(anchos)-1 {
				fmt.Fprintf(&b, " %-*s ", a, c)
				break
			}
			fmt.Fprintf(&b, " %-*s |", a, c)
		}
		fmt.Println(b.String())
	}
	linea(cabeceras)
	for _, f := range filas {
		linea(f)
	}
	fmt.Println(sep.String())
}

// barras imprime un gráfico de barras horizontales en la terminal: cada
// técnica con su barra proporcional al mayor valor.
func barras(titulo string, etiquetas []string, valores []float64, unidad string, invertir bool) {
	fmt.Printf("\n%s\n", titulo)
	mayor := 0.0
	for _, v := range valores {
		if v > mayor {
			mayor = v
		}
	}
	if mayor == 0 {
		mayor = 1
	}
	const ancho = 44
	for i, v := range valores {
		// Un valor negativo significa que esa técnica ganó sin que se note: en
		// una barra no tiene representación, así que se deja vacía y se deja
		// el número en negativo, que sí es información.
		n := 0
		if v > 0 {
			n = int(math.Round(v / mayor * float64(ancho)))
			if n < 1 {
				n = 1
			}
		}
		filled, empty := n, ancho-n
		if invertir {
			filled, empty = ancho-n, n
		}
		fmt.Printf("  %-22s |%s%s| %10.3f %s\n",
			etiquetas[i], strings.Repeat("#", filled), strings.Repeat(".", empty), v, unidad)
	}
}

// humanBytes formatea bytes en unidades legibles.
func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// sortStrings ordena las etiquetas de forma estable (para las gráficas).
func sortStrings(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}
