package spimi

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/dbms-go/v2/dbms/lib/index/text/textindex"
)

type P = textindex.Posting

func TestBlockRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.tmp")
	dict := map[string][]P{
		"zeta": {{DocID: 3, TF: 1}},
		"alfa": {{DocID: 0, TF: 2}, {DocID: 5, TF: 1}, {DocID: 1000000, TF: 7}},
		"beta": {{DocID: 2, TF: 3}},
	}
	size, err := writeBlock(path, dict)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	if st.Size() != size {
		t.Fatalf("tamaño reportado %d, real %d", size, st.Size())
	}

	c, err := openBlock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()

	var terms []string
	for {
		term, ps, ok, err := c.next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		terms = append(terms, term)
		if !reflect.DeepEqual(ps, dict[term]) {
			t.Fatalf("%q: posteos %v, esperaba %v", term, ps, dict[term])
		}
	}
	if want := []string{"alfa", "beta", "zeta"}; !reflect.DeepEqual(terms, want) {
		t.Fatalf("términos %v, esperaba %v (ordenados)", terms, want)
	}
}

func TestBlockWriterRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	bw, err := createBlock(filepath.Join(dir, "x.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	defer bw.abort()
	if err := bw.add("b", []P{{DocID: 0, TF: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := bw.add("a", []P{{DocID: 0, TF: 1}}); err == nil {
		t.Fatal("aceptó un término fuera de orden")
	}
	if err := bw.add("b", []P{{DocID: 1, TF: 1}}); err == nil {
		t.Fatal("aceptó un término repetido")
	}
	if err := bw.add("", []P{{DocID: 0, TF: 1}}); !errors.Is(err, ErrEmptyTerm) {
		t.Fatalf("término vacío: %v", err)
	}
	if err := bw.add("c", nil); err == nil {
		t.Fatal("aceptó un término sin posteos")
	}
}

func drain(t *testing.T, path string) error {
	t.Helper()
	c, err := openBlock(path)
	if err != nil {
		return err
	}
	defer c.close()
	for {
		_, _, ok, err := c.next()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}
}

func TestBlockCursorDetectsCorruption(t *testing.T) {
	dict := map[string][]P{"zeta": {{DocID: 3, TF: 1}}, "alfa": {{DocID: 0, TF: 2}}}

	t.Run("número mágico", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "b.tmp")
		os.WriteFile(path, []byte("XXXX\x00\x00\x00\x00"), 0o644)
		if err := drain(t, path); !errors.Is(err, ErrCorruptBlock) {
			t.Fatalf("error %v, esperaba ErrCorruptBlock", err)
		}
	})
	t.Run("cabecera truncada", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "b.tmp")
		os.WriteFile(path, []byte("SPB"), 0o644)
		if err := drain(t, path); !errors.Is(err, ErrCorruptBlock) {
			t.Fatalf("error %v, esperaba ErrCorruptBlock", err)
		}
	})
	t.Run("entrada truncada", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "b.tmp")
		size, _ := writeBlock(path, dict)
		os.Truncate(path, size-1)
		if err := drain(t, path); !errors.Is(err, ErrCorruptBlock) {
			t.Fatalf("error %v, esperaba ErrCorruptBlock", err)
		}
	})
	t.Run("bytes sobrantes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "b.tmp")
		writeBlock(path, dict)
		f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		f.Write([]byte{0x01})
		f.Close()
		if err := drain(t, path); !errors.Is(err, ErrCorruptBlock) {
			t.Fatalf("error %v, esperaba ErrCorruptBlock", err)
		}
	})
	t.Run("contador mayor al real", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "b.tmp")
		writeBlock(path, dict)
		f, _ := os.OpenFile(path, os.O_WRONLY, 0)
		f.WriteAt([]byte{9}, 4) // dice 9 términos, hay 2
		f.Close()
		if err := drain(t, path); !errors.Is(err, ErrCorruptBlock) {
			t.Fatalf("error %v, esperaba ErrCorruptBlock", err)
		}
	})
}

func TestPostingsCodecRoundTrip(t *testing.T) {
	ps := []P{{DocID: 0, TF: 1}, {DocID: 1, TF: 300}, {DocID: 70000, TF: 2}, {DocID: 4294967295, TF: 4294967295}}
	got, err := decodePostings(appendPostings(nil, ps), len(ps))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, ps) {
		t.Fatalf("got %v, want %v", got, ps)
	}
	if _, err := decodePostings(appendPostings(nil, ps), len(ps)-1); err == nil {
		t.Fatal("no detectó bytes sobrantes")
	}
	if _, err := decodePostings(appendPostings(nil, ps), len(ps)+1); err == nil {
		t.Fatal("no detectó truncamiento")
	}
}

func sortedKeys(m map[string][]P) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// decodePostings rechaza listas inválidas: docIDs repetidos o no crecientes, TF=0.
func TestPostingsDecoderRejectsInvalidLists(t *testing.T) {
	cases := map[string][]P{
		"docID repetido":      {{DocID: 4, TF: 1}, {DocID: 4, TF: 1}},
		"docID decreciente":   {{DocID: 9, TF: 1}, {DocID: 3, TF: 1}},
		"tf cero":             {{DocID: 1, TF: 0}},
		"tf cero en el medio": {{DocID: 1, TF: 1}, {DocID: 2, TF: 0}, {DocID: 3, TF: 1}},
	}
	for name, ps := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodePostings(appendPostings(nil, ps), len(ps)); err == nil {
				t.Fatal("aceptó una lista inválida")
			}
		})
	}
	t.Run("docID fuera de uint32", func(t *testing.T) {
		b := binary.AppendUvarint(nil, math.MaxUint32+1)
		b = binary.AppendUvarint(b, 1)
		if _, err := decodePostings(b, 1); err == nil {
			t.Fatal("aceptó un docID mayor que uint32")
		}
	})
}

// El lector de bloques también rechaza listas inválidas: un bloque con
// posteos que no cumplen el formato se detecta al leerlo, no después.
func TestBlockCursorRejectsInvalidPostings(t *testing.T) {
	for name, ps := range map[string][]P{
		"docID repetido": {{DocID: 4, TF: 1}, {DocID: 4, TF: 1}},
		"tf cero":        {{DocID: 1, TF: 0}},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "b.tmp")
			if _, err := writeBlock(path, map[string][]P{"a": ps}); err != nil {
				t.Fatal(err)
			}
			if err := drain(t, path); !errors.Is(err, ErrCorruptBlock) {
				t.Fatalf("error %v, esperaba ErrCorruptBlock", err)
			}
		})
	}
}
