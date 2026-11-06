package spimi

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

type emission struct {
	term string
	ps   []P
}

func writeTestBlock(t *testing.T, dir, name string, dict map[string][]P) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if _, err := writeBlock(path, dict); err != nil {
		t.Fatal(err)
	}
	return path
}

func collect(t *testing.T, paths []string) ([]emission, error) {
	t.Helper()
	cursors, err := openCursors(paths)
	if err != nil {
		t.Fatal(err)
	}
	defer closeCursors(cursors)
	var out []emission
	err = mergeStream(cursors, func(term string, ps []P) error {
		out = append(out, emission{term, append([]P(nil), ps...)})
		return nil
	})
	return out, err
}

func TestMergeStreamConcatenatesInBlockOrder(t *testing.T) {
	dir := t.TempDir()
	paths := []string{
		writeTestBlock(t, dir, "0.tmp", map[string][]P{
			"a": {{DocID: 0, TF: 1}},
			"c": {{DocID: 1, TF: 2}},
		}),
		writeTestBlock(t, dir, "1.tmp", map[string][]P{
			"a": {{DocID: 2, TF: 1}},
			"b": {{DocID: 3, TF: 1}},
			"c": {{DocID: 4, TF: 1}},
		}),
		writeTestBlock(t, dir, "2.tmp", map[string][]P{
			"c": {{DocID: 5, TF: 3}, {DocID: 6, TF: 1}},
		}),
	}
	got, err := collect(t, paths)
	if err != nil {
		t.Fatal(err)
	}
	want := []emission{
		{"a", []P{{DocID: 0, TF: 1}, {DocID: 2, TF: 1}}},
		{"b", []P{{DocID: 3, TF: 1}}},
		{"c", []P{{DocID: 1, TF: 2}, {DocID: 4, TF: 1}, {DocID: 5, TF: 3}, {DocID: 6, TF: 1}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestMergeStreamDetectsOverlappingBlocks(t *testing.T) {
	dir := t.TempDir()
	paths := []string{
		writeTestBlock(t, dir, "0.tmp", map[string][]P{"a": {{DocID: 5, TF: 1}}}),
		writeTestBlock(t, dir, "1.tmp", map[string][]P{"a": {{DocID: 3, TF: 1}}}),
	}
	if _, err := collect(t, paths); !errors.Is(err, ErrDocOrder) {
		t.Fatalf("error %v, esperaba ErrDocOrder", err)
	}
}

func TestMergeStreamEdgeCases(t *testing.T) {
	dir := t.TempDir()
	if got, err := collect(t, nil); err != nil || len(got) != 0 {
		t.Fatalf("sin cursores: %v, %v", got, err)
	}
	one := writeTestBlock(t, dir, "0.tmp", map[string][]P{"x": {{DocID: 0, TF: 1}}})
	got, err := collect(t, []string{one})
	if err != nil || len(got) != 1 || got[0].term != "x" {
		t.Fatalf("un cursor: %v, %v", got, err)
	}
	empty := writeTestBlock(t, dir, "e.tmp", map[string][]P{})
	got, err = collect(t, []string{empty, one})
	if err != nil || len(got) != 1 {
		t.Fatalf("bloque vacío: %v, %v", got, err)
	}
}

// Dividir los mismos datos en más bloques o mezclarlos en rondas no cambia
// el resultado del merge.
func TestReduceBlocksPreservesContent(t *testing.T) {
	dir := t.TempDir()
	const nBlocks = 9
	var paths []string
	docID := uint32(0)
	for b := 0; b < nBlocks; b++ {
		dict := map[string][]P{}
		for d := 0; d < 4; d++ {
			for _, term := range []string{"comun", fmt.Sprintf("t%d", (b+d)%5), fmt.Sprintf("solo%d", b)} {
				dict[term] = append(dict[term], P{DocID: docID, TF: uint32(d + 1)})
			}
			docID++
		}
		paths = append(paths, writeTestBlock(t, dir, fmt.Sprintf("b%d.tmp", b), dict))
	}
	want, err := collect(t, paths)
	if err != nil {
		t.Fatal(err)
	}

	seq := 0
	reduced, rounds, written, err := reduceBlocks(paths, 3, func() string {
		seq++
		return filepath.Join(dir, fmt.Sprintf("m%d.tmp", seq))
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(reduced) > 3 || rounds == 0 || written == 0 {
		t.Fatalf("reducidos=%d rondas=%d bytes=%d", len(reduced), rounds, written)
	}
	got, err := collect(t, reduced)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("el resultado cambió tras reducir en rondas")
	}
}
