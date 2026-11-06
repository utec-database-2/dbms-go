package spimi

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"

	"github.com/dbms-go/v2/dbms/lib/index/text/textindex"
)

// Reader abre un índice publicado por Builder.Finish. Carga en memoria el
// diccionario y la tabla de documentos; los posteos se leen del disco al
// pedirlos. Es seguro para uso concurrente.
type Reader struct {
	dict     map[string]dictEntry
	docLens  []uint32
	norms    []float64
	totalLen uint64
	pf       *os.File
}

func Open(dir string) (*Reader, error) {
	indexDir, err := activeIndexDir(dir)
	if err != nil {
		return nil, err
	}
	dir = indexDir
	dictB, err := os.ReadFile(filepath.Join(dir, dictFile))
	if err != nil {
		return nil, err
	}
	docsB, err := os.ReadFile(filepath.Join(dir, docsFile))
	if err != nil {
		return nil, err
	}
	pf, err := os.Open(filepath.Join(dir, postingsFile))
	if err != nil {
		return nil, err
	}
	st, err := pf.Stat()
	if err != nil {
		pf.Close()
		return nil, err
	}

	r := &Reader{pf: pf}
	if r.dict, err = parseDict(dictB, st.Size()); err != nil {
		pf.Close()
		return nil, err
	}
	if err = r.parseDocs(docsB); err != nil {
		pf.Close()
		return nil, err
	}
	return r, nil
}

func corruptIndex(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCorruptIndex, fmt.Sprintf(format, args...))
}

func parseDict(b []byte, postingsSize int64) (map[string]dictEntry, error) {
	if len(b) < 8 || string(b[:4]) != dictMagic {
		return nil, corruptIndex("cabecera de %s", dictFile)
	}
	count := binary.LittleEndian.Uint32(b[4:8])
	pos := 8
	m := make(map[string]dictEntry, min(int(count), maxPrealloc))

	uvarint := func() (uint64, bool) {
		v, k := binary.Uvarint(b[pos:])
		if k <= 0 {
			return 0, false
		}
		pos += k
		return v, true
	}

	var last string
	for i := uint32(0); i < count; i++ {
		tl, ok := uvarint()
		if !ok || tl == 0 || tl > maxTermLen || tl > uint64(len(b)-pos) {
			return nil, corruptIndex("término %d de %s", i, dictFile)
		}
		term := string(b[pos : pos+int(tl)])
		pos += int(tl)
		if i > 0 && term <= last {
			return nil, corruptIndex("término %q fuera de orden", term)
		}
		last = term

		df, ok1 := uvarint()
		off, ok2 := uvarint()
		length, ok3 := uvarint()
		if !ok1 || !ok2 || !ok3 || df == 0 || df > math.MaxUint32 || length > math.MaxUint32 {
			return nil, corruptIndex("entrada del término %q", term)
		}
		if off > uint64(postingsSize) || length > uint64(postingsSize)-off {
			return nil, corruptIndex("el término %q apunta fuera de %s", term, postingsFile)
		}
		m[term] = dictEntry{term: term, df: uint32(df), off: int64(off), length: uint32(length)}
	}
	if pos != len(b) {
		return nil, corruptIndex("bytes sobrantes en %s", dictFile)
	}
	return m, nil
}

func (r *Reader) parseDocs(b []byte) error {
	if len(b) < 8 || string(b[:4]) != docsMagic {
		return corruptIndex("cabecera de %s", docsFile)
	}
	n := int(binary.LittleEndian.Uint32(b[4:8]))
	if len(b) != 8+n*docRecordSize {
		return corruptIndex("tamaño de %s", docsFile)
	}
	r.docLens = make([]uint32, n)
	r.norms = make([]float64, n)
	for i := 0; i < n; i++ {
		rec := b[8+i*docRecordSize:]
		r.docLens[i] = binary.LittleEndian.Uint32(rec[:4])
		r.norms[i] = math.Float64frombits(binary.LittleEndian.Uint64(rec[4:12]))
		r.totalLen += uint64(r.docLens[i])
	}
	return nil
}

func (r *Reader) Postings(term string) ([]textindex.Posting, error) {
	e, ok := r.dict[term]
	if !ok {
		return nil, nil
	}
	buf := make([]byte, e.length)
	if n, err := r.pf.ReadAt(buf, e.off); err != nil && !(err == io.EOF && n == len(buf)) {
		return nil, fmt.Errorf("%w: %s: %v", ErrCorruptIndex, postingsFile, err)
	}
	ps, err := decodePostings(buf, int(e.df))
	if err != nil {
		return nil, fmt.Errorf("%w: términos %q: %v", ErrCorruptIndex, term, err)
	}
	return ps, nil
}

func (r *Reader) DF(term string) int { return int(r.dict[term].df) }

func (r *Reader) DocLen(doc uint32) int {
	if int(doc) >= len(r.docLens) {
		return 0
	}
	return int(r.docLens[doc])
}

// DocNorm es la norma del vector TF-IDF del documento, para el coseno
// (0 si el docID no existe o el documento no tiene términos con peso).
func (r *Reader) DocNorm(doc uint32) float64 {
	if int(doc) >= len(r.norms) {
		return 0
	}
	return r.norms[doc]
}

func (r *Reader) NumDocs() int { return len(r.docLens) }

func (r *Reader) AvgDocLen() float64 {
	if len(r.docLens) == 0 {
		return 0
	}
	return float64(r.totalLen) / float64(len(r.docLens))
}

func (r *Reader) NumTerms() int { return len(r.dict) }

// Terms devuelve el vocabulario ordenado.
func (r *Reader) Terms() []string {
	out := make([]string, 0, len(r.dict))
	for t := range r.dict {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func (r *Reader) Close() error { return r.pf.Close() }

var _ textindex.InvertedIndex = (*Reader)(nil)
