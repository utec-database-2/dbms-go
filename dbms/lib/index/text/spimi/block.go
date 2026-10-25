package spimi

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/dbms-go/v2/dbms/lib/index/text/textindex"
)

// blockWriter escribe un bloque en streaming: como no conoce de antemano
// cuántos términos va a recibir (el merge intermedio tampoco), deja el
// contador en 0 y lo corrige al cerrar.
type blockWriter struct {
	path     string
	f        *os.File
	w        *bufio.Writer
	terms    uint32
	size     int64
	last     string
	haveLast bool
	buf      []byte
}

func createBlock(path string) (*blockWriter, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	bw := &blockWriter{path: path, f: f, w: bufio.NewWriterSize(f, 1<<16)}
	var hdr [blockHeaderSize]byte
	copy(hdr[:4], blockMagic)
	if _, err := bw.w.Write(hdr[:]); err != nil {
		bw.abort()
		return nil, err
	}
	bw.size = blockHeaderSize
	return bw, nil
}

// add agrega una entrada. Los términos deben llegar en orden estrictamente
// creciente y los posteos en orden estrictamente creciente de docID.
func (bw *blockWriter) add(term string, ps []textindex.Posting) error {
	switch {
	case term == "":
		return ErrEmptyTerm
	case len(term) > maxTermLen:
		return ErrTermTooLong
	case len(ps) == 0:
		return fmt.Errorf("spimi: el término %q no tiene posteos", term)
	case bw.haveLast && term <= bw.last:
		return fmt.Errorf("spimi: término %q fuera de orden (anterior %q)", term, bw.last)
	}
	buf := bw.buf[:0]
	buf = binary.AppendUvarint(buf, uint64(len(term)))
	buf = append(buf, term...)
	buf = binary.AppendUvarint(buf, uint64(len(ps)))
	buf = appendPostings(buf, ps)
	bw.buf = buf

	n, err := bw.w.Write(buf)
	bw.size += int64(n)
	if err != nil {
		return err
	}
	bw.terms++
	bw.last, bw.haveLast = term, true
	return nil
}

// close termina el bloque y devuelve su tamaño en bytes.
func (bw *blockWriter) close() (int64, error) {
	if err := bw.w.Flush(); err != nil {
		bw.abort()
		return 0, err
	}
	var cnt [4]byte
	binary.LittleEndian.PutUint32(cnt[:], bw.terms)
	if _, err := bw.f.WriteAt(cnt[:], 4); err != nil {
		bw.abort()
		return 0, err
	}
	if err := bw.f.Close(); err != nil {
		os.Remove(bw.path)
		return 0, err
	}
	return bw.size, nil
}

// abort descarta el bloque a medio escribir.
func (bw *blockWriter) abort() {
	bw.f.Close()
	os.Remove(bw.path)
}

// writeBlock vuelca un diccionario en memoria como un bloque ordenado.
func writeBlock(path string, dict map[string][]textindex.Posting) (int64, error) {
	terms := make([]string, 0, len(dict))
	for t := range dict {
		terms = append(terms, t)
	}
	sort.Strings(terms)

	bw, err := createBlock(path)
	if err != nil {
		return 0, err
	}
	for _, t := range terms {
		if err := bw.add(t, dict[t]); err != nil {
			bw.abort()
			return 0, err
		}
	}
	return bw.close()
}

// blockCursor recorre un bloque entrada por entrada, con una sola entrada
// en memoria a la vez.
type blockCursor struct {
	path      string
	f         *os.File
	r         *bufio.Reader
	remaining uint32
	last      string
	haveLast  bool
}

func openBlock(path string) (*blockCursor, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	r := bufio.NewReaderSize(f, 1<<16)
	var hdr [blockHeaderSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		f.Close()
		return nil, corruptBlock(path, fmt.Errorf("cabecera: %w", err))
	}
	if string(hdr[:4]) != blockMagic {
		f.Close()
		return nil, corruptBlock(path, errors.New("número mágico incorrecto"))
	}
	return &blockCursor{
		path:      path,
		f:         f,
		r:         r,
		remaining: binary.LittleEndian.Uint32(hdr[4:]),
	}, nil
}

// next devuelve la siguiente entrada; ok es false al agotar el bloque.
// Al llegar al final verifica que no sobren bytes.
func (c *blockCursor) next() (term string, ps []textindex.Posting, ok bool, err error) {
	if c.remaining == 0 {
		if _, err := c.r.ReadByte(); err == nil {
			return "", nil, false, corruptBlock(c.path, errors.New("bytes sobrantes al final"))
		} else if err != io.EOF {
			return "", nil, false, err
		}
		return "", nil, false, nil
	}

	termLen, err := readUvarint(c.r)
	if err != nil {
		return "", nil, false, corruptBlock(c.path, err)
	}
	if termLen == 0 || termLen > maxTermLen {
		return "", nil, false, corruptBlock(c.path, fmt.Errorf("largo de término %d inválido", termLen))
	}
	tb := make([]byte, termLen)
	if _, err := io.ReadFull(c.r, tb); err != nil {
		return "", nil, false, corruptBlock(c.path, err)
	}
	term = string(tb)
	if c.haveLast && term <= c.last {
		return "", nil, false, corruptBlock(c.path, fmt.Errorf("término %q fuera de orden", term))
	}

	n, err := readUvarint(c.r)
	if err != nil {
		return "", nil, false, corruptBlock(c.path, err)
	}
	if n == 0 {
		return "", nil, false, corruptBlock(c.path, fmt.Errorf("término %q sin posteos", term))
	}
	ps, err = readPostings(c.r, n)
	if err != nil {
		return "", nil, false, corruptBlock(c.path, err)
	}

	c.remaining--
	c.last, c.haveLast = term, true
	return term, ps, true, nil
}

func (c *blockCursor) close() error { return c.f.Close() }

func openCursors(paths []string) ([]*blockCursor, error) {
	cursors := make([]*blockCursor, 0, len(paths))
	for _, p := range paths {
		c, err := openBlock(p)
		if err != nil {
			closeCursors(cursors)
			return nil, err
		}
		cursors = append(cursors, c)
	}
	return cursors, nil
}

func closeCursors(cursors []*blockCursor) {
	for _, c := range cursors {
		c.close()
	}
}
