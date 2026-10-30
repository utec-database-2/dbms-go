package spimi

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/dbms-go/v2/dbms/lib/index/text/textindex"
)

const (
	// DefaultMaxBlockPostings es cuántos posteos (pares término-documento)
	// caben en memoria antes de volcar un bloque. Con 100 000 documentos
	// de texto real da varios bloques, de modo que el merge se ejercita
	// también en el benchmark más grande.
	DefaultMaxBlockPostings = 1 << 19
	// DefaultMaxFanIn es cuántos bloques se abren a la vez en un merge.
	DefaultMaxFanIn = 64

	postingsFile = "postings.dat"
	dictFile     = "dict.dat"
	docsFile     = "docs.dat"
)

// DefaultTermWeight es el peso TF-IDF con el que se calcula la norma de cada
// documento: (1 + ln tf) * ln(N/df).
//
// Esta formula se comparte con el ranking mediante textindex.TFIDFWeight.
// Alternativa anterior de Python, NO activa: (1+log10(tf))*log10(N/df).
// Si se usa Options.TermWeight personalizada, el coseno debe usar normas compatibles.
func DefaultTermWeight(tf uint32, df, numDocs int) float64 {
	if tf == 0 || df == 0 || numDocs == 0 {
		return 0
	}
	return textindex.TFIDFWeight(tf, df, numDocs)
}

type Options struct {
	// Dir es el directorio del índice. Es exclusivo: se borran los
	// spimi-*.tmp que haya.
	Dir string
	// MaxBlockPostings: <= 0 usa DefaultMaxBlockPostings.
	MaxBlockPostings int // MaxBlockPostings es un limite aproximado: el builder puede volcar un bloque con más posteos si un término grande lo requiere.
	// MaxFanIn: <= 0 usa DefaultMaxFanIn; 1 se trata como 2.
	MaxFanIn int
	// TermWeight pesa un término en un documento para la norma. nil usa
	// DefaultTermWeight.
	TermWeight func(tf uint32, df, numDocs int) float64
}

// BuildStats es lo que consume el benchmark de construcción.
type BuildStats struct {
	NumDocs     int
	NumTerms    int
	NumPostings int64
	Blocks      int           // bloques volcados desde memoria
	MergeRounds int           // rondas intermedias antes del merge final
	BlockBytes  int64         // bytes escritos en bloques temporales (iniciales e intermedios)
	IndexBytes  int64         // tamaño final del índice en disco
	Elapsed     time.Duration // tiempo dentro del builder (AddDocument + Finish)
}

// Builder construye el índice. No es seguro para uso concurrente.
type Builder struct {
	opts          Options
	dict          map[string][]textindex.Posting
	pending       int
	blocks        []string
	docLens       []uint32
	seq           int
	stats         BuildStats
	busy          time.Duration
	done          bool
	generationDir string // directorio privado de construcción y publicación
	published     bool   // Close no elimina una generación publicada
}

func NewBuilder(opts Options) (*Builder, error) {
	if opts.Dir == "" {
		return nil, fmt.Errorf("spimi: falta Options.Dir")
	}
	if opts.MaxBlockPostings <= 0 {
		opts.MaxBlockPostings = DefaultMaxBlockPostings
	}
	switch {
	case opts.MaxFanIn <= 0:
		opts.MaxFanIn = DefaultMaxFanIn
	case opts.MaxFanIn == 1:
		opts.MaxFanIn = 2
	}
	if opts.TermWeight == nil {
		opts.TermWeight = DefaultTermWeight
	}
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return nil, err
	}
	if err := removeTemps(opts.Dir); err != nil {
		return nil, err
	}
	genDir, err := os.MkdirTemp(opts.Dir, generationPrefix)
	if err != nil {
		return nil, err
	}
	return &Builder{opts: opts, dict: make(map[string][]textindex.Posting), generationDir: genDir}, nil
}

// AddDocument agrega un documento ya tokenizado y devuelve su docID
// (0, 1, 2, ... en orden de llegada). Los tokens repetidos suman tf.
func (b *Builder) AddDocument(tokens []string) (uint32, error) {
	if b.done {
		return 0, ErrClosed
	}
	t0 := time.Now()
	defer func() { b.busy += time.Since(t0) }()

	// Se valida todo antes de tocar el estado: un documento inválido no
	// deja posteos a medias.
	for _, t := range tokens {
		if t == "" {
			return 0, ErrEmptyTerm
		}
		if len(t) > maxTermLen {
			return 0, ErrTermTooLong
		}
	}
	if len(b.docLens) >= math.MaxUint32 {
		return 0, fmt.Errorf("spimi: demasiados documentos")
	}

	id := uint32(len(b.docLens))
	counts := make(map[string]uint32, len(tokens))
	for _, t := range tokens {
		counts[t]++
	}
	for t, tf := range counts {
		b.dict[t] = append(b.dict[t], textindex.Posting{DocID: id, TF: tf})
	}
	b.docLens = append(b.docLens, uint32(len(tokens)))
	b.pending += len(counts)

	// Se vuelca recién con el documento completo: así los bloques cubren
	// rangos de docID disjuntos.
	if b.pending >= b.opts.MaxBlockPostings {
		if err := b.flushBlock(); err != nil {
			return 0, err
		}
	}
	return id, nil
}

func (b *Builder) tempPath(prefix string) string {
	b.seq++
	return filepath.Join(b.generationDir, fmt.Sprintf("%s-%06d.tmp", prefix, b.seq))
}

func (b *Builder) flushBlock() error {
	if len(b.dict) == 0 {
		return nil
	}
	path := b.tempPath("spimi-block")
	n, err := writeBlock(path, b.dict)
	if err != nil {
		return err
	}
	b.blocks = append(b.blocks, path)
	b.stats.Blocks++
	b.stats.BlockBytes += n
	b.dict = make(map[string][]textindex.Posting)
	b.pending = 0
	return nil
}

// Finish mezcla los bloques, publica el índice (postings.dat, docs.dat y,
// al final, dict.dat) y borra los temporales. Después el Builder queda
// cerrado.
func (b *Builder) Finish() (stats BuildStats, err error) {
	if b.done {
		return BuildStats{}, ErrClosed
	}
	b.done = true
	t0 := time.Now()
	defer func() {
		b.busy += time.Since(t0)
		b.dict = nil
		if err != nil {
			_ = os.RemoveAll(b.generationDir) // nunca se publica si falla
			return
		}
		b.stats.Elapsed = b.busy
		stats = b.stats
	}()

	if err = b.flushBlock(); err != nil {
		return
	}
	blocks, rounds, written, err := reduceBlocks(b.blocks, b.opts.MaxFanIn, func() string {
		return b.tempPath("spimi-merge")
	})
	b.stats.MergeRounds = rounds
	b.stats.BlockBytes += written
	if err != nil {
		return
	}
	if err = b.writeFinal(blocks); err != nil {
		return
	}
	if err = removeTemps(b.generationDir); err != nil {
		return
	}
	if err = publishGeneration(b.opts.Dir, filepath.Base(b.generationDir)); err != nil {
		return
	}
	b.published = true
	return
}

// Close descarta lo que quede a medias. Es seguro llamarlo después de Finish.
func (b *Builder) Close() error {
	b.done = true
	b.dict = nil
	if b.published {
		return nil // el índice publicado debe sobrevivir al Builder
	}
	return os.RemoveAll(b.generationDir)
}

// writeFinal hace el merge final y escribe los tres archivos del índice.
// Calcula en la misma pasada la norma de cada documento: en este punto cada
// término ya tiene su lista completa, así que df y N son los definitivos.
func (b *Builder) writeFinal(blocks []string) error {
	dir := b.generationDir
	n := len(b.docLens)
	norm2 := make([]float64, n)

	cursors, err := openCursors(blocks)
	if err != nil {
		return err
	}
	defer closeCursors(cursors)

	postingsTmp := filepath.Join(dir, "spimi-postings.tmp")
	pf, err := os.OpenFile(postingsTmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	pw := bufio.NewWriterSize(pf, 1<<16)

	var (
		entries []dictEntry
		off     int64
		buf     []byte
	)
	emit := func(term string, ps []textindex.Posting) error {
		df := len(ps)
		for _, p := range ps {
			if int(p.DocID) >= n {
				return fmt.Errorf("%w: docID %d fuera de rango (%d documentos)", ErrCorruptBlock, p.DocID, n)
			}
			w := b.opts.TermWeight(p.TF, df, n)
			if w < 0 || math.IsNaN(w) || math.IsInf(w, 0) {
				return fmt.Errorf(
					"%w: TermWeight devolvio un peso inválido para término %q en docID %d: %v", ErrTermWeight, term, p.DocID, w)
			}
			norm2[p.DocID] += w * w
		}
		buf = appendPostings(buf[:0], ps)
		if _, err := pw.Write(buf); err != nil {
			return err
		}
		entries = append(entries, dictEntry{term: term, df: uint32(df), off: off, length: uint32(len(buf))})
		off += int64(len(buf))
		b.stats.NumPostings += int64(df)
		return nil
	}
	if err := mergeStream(cursors, emit); err != nil {
		pf.Close()
		return err
	}
	if err := pw.Flush(); err != nil {
		pf.Close()
		return err
	}
	if err := pf.Sync(); err != nil {
		pf.Close()
		return err
	}
	if err := pf.Close(); err != nil {
		return err
	}

	// dict.dat
	db := make([]byte, 0, 8+len(entries)*16)
	db = append(db, dictMagic...)
	db = binary.LittleEndian.AppendUint32(db, uint32(len(entries)))
	for _, e := range entries {
		db = binary.AppendUvarint(db, uint64(len(e.term)))
		db = append(db, e.term...)
		db = binary.AppendUvarint(db, uint64(e.df))
		db = binary.AppendUvarint(db, uint64(e.off))
		db = binary.AppendUvarint(db, uint64(e.length))
	}
	dictTmp := filepath.Join(dir, "spimi-dict.tmp")
	if err := writeFileSync(dictTmp, db); err != nil {
		return err
	}

	// docs.dat
	ds := make([]byte, 0, 8+n*docRecordSize)
	ds = append(ds, docsMagic...)
	ds = binary.LittleEndian.AppendUint32(ds, uint32(n))
	for i := 0; i < n; i++ {
		ds = binary.LittleEndian.AppendUint32(ds, b.docLens[i])
		ds = binary.LittleEndian.AppendUint64(ds, math.Float64bits(math.Sqrt(norm2[i])))
	}
	docsTmp := filepath.Join(dir, "spimi-docs.tmp")
	if err := writeFileSync(docsTmp, ds); err != nil {
		return err
	}

	// dict.dat se publica al final: su presencia indica índice completo.
	if err := os.Rename(postingsTmp, filepath.Join(dir, postingsFile)); err != nil {
		return err
	}
	if err := os.Rename(docsTmp, filepath.Join(dir, docsFile)); err != nil {
		return err
	}
	if err := os.Rename(dictTmp, filepath.Join(dir, dictFile)); err != nil {
		return err
	}

	b.stats.NumDocs = n
	b.stats.NumTerms = len(entries)
	b.stats.IndexBytes = off + int64(len(db)) + int64(len(ds))
	return nil
}

func writeFileSync(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// removeTemps borra los spimi-*.tmp del directorio.
func removeTemps(dir string) error {
	matches, err := filepath.Glob(filepath.Join(dir, "spimi-*.tmp"))
	if err != nil {
		return err
	}
	for _, m := range matches {
		if err := os.Remove(m); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
