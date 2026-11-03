package spimi

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/dbms-go/v2/dbms/lib/index/text/textindex"
)

const (
	blockMagic = "SPB1"
	dictMagic  = "SPD1"
	docsMagic  = "SPC1"

	blockHeaderSize = 8
	docRecordSize   = 12 // largo uint32 + norma float64

	maxTermLen  = 1 << 16
	maxPrealloc = 1 << 16 // tope de prealocación al leer, por si un archivo está corrupto
)

var (
	ErrCorruptBlock = errors.New("spimi: bloque corrupto")
	ErrCorruptIndex = errors.New("spimi: índice corrupto")
	ErrDocOrder     = errors.New("spimi: los docID de los bloques se solapan o no crecen")
	ErrClosed       = errors.New("spimi: el builder ya terminó o se cerró")
	ErrEmptyTerm    = errors.New("spimi: término vacío")
	ErrTermTooLong  = errors.New("spimi: término demasiado largo")
	ErrTermWeight   = errors.New("spimi: la función TermWeight devolvió un valor inválido")

	errBadPosting = errors.New("posteo inválido")
)

// dictEntry ubica la lista de posteos de un término en postings.dat.
type dictEntry struct {
	term   string
	df     uint32
	off    int64
	length uint32
}

// appendPostings codifica una lista (estrictamente creciente por DocID) con
// docID en diferencias y tf, ambos en uvarint.
func appendPostings(dst []byte, ps []textindex.Posting) []byte {
	var prev uint32
	for i, p := range ps {
		delta := p.DocID
		if i > 0 {
			delta = p.DocID - prev
		}
		dst = binary.AppendUvarint(dst, uint64(delta))
		dst = binary.AppendUvarint(dst, uint64(p.TF))
		prev = p.DocID
	}
	return dst
}

// deltaDecoder reconstruye docID absolutos a partir de las diferencias y
// valida la lista: docID estrictamente crecientes, tf >= 1, todo en uint32.
type deltaDecoder struct {
	prev uint32
	n    int
}

func (d *deltaDecoder) next(delta, tf uint64) (textindex.Posting, error) {
	var id uint64
	if d.n == 0 {
		id = delta
	} else {
		if delta == 0 {
			return textindex.Posting{}, errBadPosting
		}
		id = uint64(d.prev) + delta
	}
	if id > math.MaxUint32 || tf == 0 || tf > math.MaxUint32 {
		return textindex.Posting{}, errBadPosting
	}
	d.prev = uint32(id)
	d.n++
	return textindex.Posting{DocID: uint32(id), TF: uint32(tf)}, nil
}

// decodePostings decodifica exactamente n posteos de b (sin bytes sobrantes).
func decodePostings(b []byte, n int) ([]textindex.Posting, error) {
	ps := make([]textindex.Posting, 0, min(n, maxPrealloc))
	var d deltaDecoder
	for i := 0; i < n; i++ {
		delta, k := binary.Uvarint(b)
		if k <= 0 {
			return nil, errBadPosting
		}
		b = b[k:]
		tf, k := binary.Uvarint(b)
		if k <= 0 {
			return nil, errBadPosting
		}
		b = b[k:]
		p, err := d.next(delta, tf)
		if err != nil {
			return nil, err
		}
		ps = append(ps, p)
	}
	if len(b) != 0 {
		return nil, errBadPosting
	}
	return ps, nil
}

// readUvarint lee un uvarint; un EOF a mitad de camino es truncamiento.
func readUvarint(r *bufio.Reader) (uint64, error) {
	v, err := binary.ReadUvarint(r)
	if err == io.EOF {
		return 0, io.ErrUnexpectedEOF
	}
	return v, err
}

// readPostings lee n posteos de un bloque.
func readPostings(r *bufio.Reader, n uint64) ([]textindex.Posting, error) {
	ps := make([]textindex.Posting, 0, min(n, uint64(maxPrealloc)))
	var d deltaDecoder
	for i := uint64(0); i < n; i++ {
		delta, err := readUvarint(r)
		if err != nil {
			return nil, err
		}
		tf, err := readUvarint(r)
		if err != nil {
			return nil, err
		}
		p, err := d.next(delta, tf)
		if err != nil {
			return nil, err
		}
		ps = append(ps, p)
	}
	return ps, nil
}

func corruptBlock(path string, err error) error {
	return fmt.Errorf("%w: %s: %v", ErrCorruptBlock, path, err)
}
