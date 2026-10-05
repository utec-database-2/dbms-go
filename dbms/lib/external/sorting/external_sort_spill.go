package sorting

import (
	"bufio"
	"container/heap"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/dbms-go/v2/dbms/lib/storage"
)

// External sorting con volcado a disco.
//
// La versión en memoria (ExternalSort) ordena cada run y lo conserva en un
// slice, así que el pico de memoria es el de todos los runs juntos. Aquí cada run
// se escribe a un archivo temporal en cuanto se llena el buffer y el merge los
// vuelve a leer por franjas: la memoria queda acotada por el tamaño del buffer,
// que es justo lo que hace un external sort de verdad.
//
// Formato de un run: una secuencia de entradas
//
//	[u32 keyLen][clave][u32 rowLen][fila]
//
// La clave y la fila se serializan con el códec de storage, que etiqueta cada
// valor con su tipo. Así el merge compara claves sin conocer el esquema y la
// fila vuelve a ser exactamente el valor que se le pasó.

// SpillOptions configura el external sorting con volcado a disco.
type SpillOptions struct {
	// BufferSlots es cuántas filas caben en memoria antes de volcar un run. Con 0
	// se genera un único run con todas las filas.
	BufferSlots int
	// Desc ordena en descendente.
	Desc bool
	// Dir es el directorio donde se crean los runs temporales. Vacío = actual.
	Dir string
	// Prefix da nombre a los archivos temporales (por defecto "sortrun").
	Prefix string
	// TempSuffix es el sufijo de los temporales (por defecto ".tmp").
	TempSuffix string
	// NoSpill mantiene los runs en memoria aunque haya varios. Es el modo en
	// memoria de ExternalSort, útil para comparar o para directorios de solo
	// lectura.
	NoSpill bool
}

// SpillResult es el resultado del external sorting con volcado a disco.
type SpillResult[T any] struct {
	// Rows son las filas ordenadas.
	Rows []T
	// Runs es cuántos runs se formaron (1 = todo en un solo run).
	Runs int
	// Spilled indica que al menos un run llegó a escribirse en disco.
	Spilled bool
	// TempFiles es cuántos archivos temporales se crearon (y se borraron).
	TempFiles int
	// BytesWritten y BytesRead son las E/S del volcado y del merge.
	BytesWritten int64
	BytesRead    int64
}

// SpillEncode serializa una fila para escribirla en un run temporal.
type SpillEncode[T any] func(T) ([]byte, error)

// SpillDecode lee una fila de un run temporal.
type SpillDecode[T any] func([]byte) (T, error)

// cursor es un run positioned para el merge: entrega sus filas en orden.
type cursor[T any] interface {
	// next avanza a la siguiente entrada. Devuelve io.EOF al agotarse.
	next() error
	// key y row son los valores de la posición actual.
	key() any
	row() T
	// order es la posición del run, que desempata claves iguales.
	order() int
	// bytesRead lleva la cuenta de lo leído de disco.
	bytesRead() int64
	// close libera el archivo del run, si lo tiene.
	close() error
}

// ---------------------------------------------------------------------------
// Run en memoria
// ---------------------------------------------------------------------------

type memCursor[T any] struct {
	rows  []keyed[T]
	pos   int
	index int

	key_ any
	row_ T
}

func (m *memCursor[T]) next() error {
	if m.pos >= len(m.rows) {
		return io.EOF
	}
	e := m.rows[m.pos]
	m.pos++
	m.key_ = e.key
	m.row_ = e.row
	return nil
}

func (m *memCursor[T]) key() any         { return m.key_ }
func (m *memCursor[T]) row() T           { return m.row_ }
func (m *memCursor[T]) order() int       { return m.index }
func (m *memCursor[T]) bytesRead() int64 { return 0 }
func (m *memCursor[T]) close() error     { return nil }

// ---------------------------------------------------------------------------
// Run en disco
// ---------------------------------------------------------------------------

type fileCursor[T any] struct {
	index  int
	path   string
	f      *os.File
	r      *bufio.Reader
	decode SpillDecode[T]
	read   int64

	key_ any
	row_ T
}

// openRun abre (o reintenta abrir) un run temporal para leerlo.
func openRun[T any](path string, index int, decode SpillDecode[T]) (*fileCursor[T], error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("sorting: no se pudo abrir el run temporal %s: %w", path, err)
	}
	return &fileCursor[T]{
		index:  index,
		path:   path,
		f:      f,
		r:      bufio.NewReaderSize(f, 64*1024),
		decode: decode,
	}, nil
}

func (c *fileCursor[T]) next() error {
	keyBuf, err := readBlob(c.r, &c.read)
	if err != nil {
		return err
	}
	rowBuf, err := readBlob(c.r, &c.read)
	if err != nil {
		return err
	}
	key, used, err := storage.DecodeValue(keyBuf)
	if err != nil {
		return fmt.Errorf("sorting: clave ilegible en %s: %w", c.path, err)
	}
	if used != len(keyBuf) {
		return fmt.Errorf("sorting: clave con %d bytes de sobra en %s", len(keyBuf)-used, c.path)
	}
	row, err := c.decode(rowBuf)
	if err != nil {
		return fmt.Errorf("sorting: fila ilegible en %s: %w", c.path, err)
	}
	c.key_ = key
	c.row_ = row
	return nil
}

func (c *fileCursor[T]) key() any         { return c.key_ }
func (c *fileCursor[T]) row() T           { return c.row_ }
func (c *fileCursor[T]) order() int       { return c.index }
func (c *fileCursor[T]) bytesRead() int64 { return c.read }

func (c *fileCursor[T]) close() error {
	if c.f == nil {
		return nil
	}
	err := c.f.Close()
	c.f = nil
	return err
}

// readBlob lee un bloque [u32 len][bytes] y lo devuelve tal cual.
func readBlob(r io.Reader, read *int64) ([]byte, error) {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		if err == io.ErrUnexpectedEOF {
			return nil, fmt.Errorf("sorting: run temporal truncado en la cabecera de un bloque")
		}
		return nil, err
	}
	n := int(binary.LittleEndian.Uint32(head[:]))
	*read += 4
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("sorting: run temporal truncado: %w", err)
	}
	*read += int64(n)
	return buf, nil
}

// ---------------------------------------------------------------------------
// El algoritmo
// ---------------------------------------------------------------------------

// ExternalSortSpilling ordena rows según keyOf volcando a disco cada run que no
// cabe en el buffer y volviendo a unir los runs con un k-way merge. encode y
// decode son los códecs de la fila: solo se usan si algún run llega a disco.
func ExternalSortSpilling[T any](
	rows []T,
	keyOf func(T) (any, error),
	encode SpillEncode[T],
	decode SpillDecode[T],
	opt SpillOptions,
) (SpillResult[T], error) {
	res := SpillResult[T]{Rows: []T{}}

	bufferSlots := opt.BufferSlots
	if bufferSlots < 1 {
		bufferSlots = len(rows)
	}
	if bufferSlots < 1 {
		bufferSlots = 1
	}
	prefix := opt.Prefix
	if prefix == "" {
		prefix = "sortrun"
	}
	suffix := opt.TempSuffix
	if suffix == "" {
		suffix = ".tmp"
	}

	// Las claves se extraen una sola vez: el merge compara claves, no filas.
	keys := make([]any, len(rows))
	for i, r := range rows {
		k, err := keyOf(r)
		if err != nil {
			return res, fmt.Errorf("sorting: no se pudo extraer la clave de la fila %d: %w", i, err)
		}
		keys[i] = k
	}

	var cursors []cursor[T]
	var tempFiles []string
	cleanup := func() {
		for _, c := range cursors {
			_ = c.close()
		}
		for _, p := range tempFiles {
			_ = os.Remove(p)
		}
	}
	// Los runs temporales nunca sobreviven a la llamada, ni siquiera si el merge
	// falla a mitad: un temporal huérfano solo occupationa espacio.
	defer cleanup()

	buf := make([]keyed[T], 0, bufferSlots)
	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		sortKeyed(buf, opt.Desc)
		index := len(cursors)
		if opt.NoSpill {
			cursors = append(cursors, &memCursor[T]{rows: buf, index: index})
			buf = nil
			return nil
		}
		path, written, err := writeRun(dirOf(opt.Dir),
			fmt.Sprintf("%s-%03d%s", prefix, index, suffix), buf, encode)
		if err != nil {
			return err
		}
		res.BytesWritten += written
		res.TempFiles++
		tempFiles = append(tempFiles, path)

		fc, err := openRun[T](path, index, decode)
		if err != nil {
			return err
		}
		cursors = append(cursors, fc)
		buf = nil
		return nil
	}

	// El último buffer no se vuelga por adelantado: si al final resulta ser el
	// único run, se queda en memoria y no se ha escrito nada. Solo cuando ya hay
	// runs en disco el resto pasa a ser temporal, que es cuando el spilling
	// compensa de verdad.
	for i, r := range rows {
		buf = append(buf, keyed[T]{key: keys[i], row: r, ord: i})
		if len(buf) == bufferSlots && i < len(rows)-1 {
			if err := flush(); err != nil {
				return res, err
			}
		}
	}
	if len(cursors) == 0 {
		sortKeyed(buf, opt.Desc)
		if len(buf) > 0 {
			cursors = append(cursors, &memCursor[T]{rows: buf})
		}
		buf = nil
	} else if err := flush(); err != nil {
		return res, err
	}

	res.Runs = len(cursors)
	res.Spilled = res.TempFiles > 0
	if res.Runs == 0 {
		return res, nil
	}

	// Un único run ya está ordenado: no hay nada que fusionar, y no hace falta
	// ni tocar el disco.
	if res.Runs == 1 {
		m := cursors[0]
		if err := m.next(); err == nil {
			out := make([]T, 0, len(rows))
			for {
				out = append(out, m.row())
				if err := m.next(); err != nil {
					break
				}
			}
			res.Rows = out
			return res, nil
		}
	}

	h := &mergeHeap[T]{desc: opt.Desc}
	for _, c := range cursors {
		if err := c.next(); err == nil {
			h.cursors = append(h.cursors, c)
		} else if err != io.EOF {
			return res, err
		}
	}
	heap.Init(h)

	out := make([]T, 0, len(rows))
	for h.Len() > 0 {
		top := h.cursors[0]
		out = append(out, top.row())
		if err := top.next(); err == nil {
			heap.Fix(h, 0)
		} else if err == io.EOF {
			heap.Pop(h)
		} else {
			return res, err
		}
	}
	res.Rows = out
	for _, c := range cursors {
		res.BytesRead += c.bytesRead()
	}
	return res, nil
}

func dirOf(dir string) string {
	if dir == "" {
		return "."
	}
	return dir
}

// writeRun vuelca un run ordenado a un archivo temporal. Si algo falla borra el
// archivo para no dejar basura.
func writeRun[T any](dir, name string, rows []keyed[T], encode SpillEncode[T]) (string, int64, error) {
	if encode == nil {
		return "", 0, fmt.Errorf("sorting: hace falta un códec para volcar filas a disco")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, fmt.Errorf("sorting: no se pudo crear el directorio de runs: %w", err)
	}
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		return "", 0, fmt.Errorf("sorting: no se pudo crear el run temporal %s: %w", path, err)
	}
	fail := func(err error) (string, int64, error) {
		_ = f.Close()
		_ = os.Remove(path)
		return "", 0, err
	}

	w := bufio.NewWriter(f)
	var written int64
	for _, e := range rows {
		keyBuf, err := storage.AppendValue(nil, e.key)
		if err != nil {
			return fail(err)
		}
		rowBuf, err := encode(e.row)
		if err != nil {
			return fail(fmt.Errorf("sorting: no se pudo codificar una fila del run: %w", err))
		}
		n, err := writeEntry(w, keyBuf, rowBuf)
		written += n
		if err != nil {
			return fail(err)
		}
	}
	if err := w.Flush(); err != nil {
		return fail(fmt.Errorf("sorting: no se pudo escribir el run temporal: %w", err))
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", 0, fmt.Errorf("sorting: no se pudo cerrar el run temporal: %w", err)
	}
	return path, written, nil
}

// writeEntry escribe una entrada del run: [keyLen][clave][rowLen][fila]. Cada
// longitud precede a su bloque, que es lo que espera readBlob al releer.
func writeEntry(w io.Writer, key, row []byte) (int64, error) {
	var head [4]byte
	var total int64
	write := func(chunk []byte) error {
		n, err := w.Write(chunk)
		total += int64(n)
		if err != nil {
			return fmt.Errorf("sorting: error escribiendo el run: %w", err)
		}
		return nil
	}

	binary.LittleEndian.PutUint32(head[:], uint32(len(key)))
	if err := write(head[:]); err != nil {
		return total, err
	}
	if err := write(key); err != nil {
		return total, err
	}
	binary.LittleEndian.PutUint32(head[:], uint32(len(row)))
	if err := write(head[:]); err != nil {
		return total, err
	}
	return total, write(row)
}

// mergeHeap es la cola de prioridad del k-way merge sobre cualquier mezcla de
// runs (en memoria y en disco).
type mergeHeap[T any] struct {
	cursors []cursor[T]
	desc    bool
}

func (h *mergeHeap[T]) Len() int { return len(h.cursors) }

func (h *mergeHeap[T]) Less(i, j int) bool {
	c := compare(h.cursors[i].key(), h.cursors[j].key())
	if c != 0 {
		if h.desc {
			return c > 0
		}
		return c < 0
	}
	// Con claves iguales gana el run más antiguo, que es lo que hace estable el
	// merge respecto del orden de llegada.
	return h.cursors[i].order() < h.cursors[j].order()
}

func (h *mergeHeap[T]) Swap(i, j int) {
	h.cursors[i], h.cursors[j] = h.cursors[j], h.cursors[i]
}

func (h *mergeHeap[T]) Push(x any) { h.cursors = append(h.cursors, x.(cursor[T])) }
func (h *mergeHeap[T]) Pop() any {
	old := h.cursors
	n := len(old)
	c := old[n-1]
	h.cursors = old[:n-1]
	return c
}
