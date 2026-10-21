package spimi

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/dbms-go/v2/dbms/lib/index/text/textindex"
)

// genCorpus genera documentos tokenizados con vocabulario sesgado (unos pocos
// términos muy frecuentes y una cola larga) y algunos documentos vacíos.
func genCorpus(seed int64, docs, vocab, maxLen int) [][]string {
	rng := rand.New(rand.NewSource(seed))
	out := make([][]string, docs)
	for i := range out {
		toks := make([]string, rng.Intn(maxLen+1))
		for j := range toks {
			u := rng.Float64()
			toks[j] = fmt.Sprintf("t%03d", int(u*u*float64(vocab)))
		}
		out[i] = toks
	}
	return out
}

func build(t *testing.T, dir string, corpus [][]string, opts Options) (BuildStats, *textindex.MemIndex) {
	t.Helper()
	opts.Dir = dir
	b, err := NewBuilder(opts)
	if err != nil {
		t.Fatal(err)
	}
	mem := textindex.NewMemIndex()
	for i, doc := range corpus {
		id, err := b.AddDocument(doc)
		if err != nil {
			t.Fatal(err)
		}
		if want := mem.AddDocument(doc); id != want || int(id) != i {
			t.Fatalf("docID %d, esperaba %d", id, i)
		}
	}
	stats, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return stats, mem
}

// assertSameIndex compara el índice leído de disco con el oráculo en memoria.
func assertSameIndex(t *testing.T, got *Reader, want *textindex.MemIndex) {
	t.Helper()
	if got.NumDocs() != want.NumDocs() {
		t.Fatalf("NumDocs %d, esperaba %d", got.NumDocs(), want.NumDocs())
	}
	if math.Abs(got.AvgDocLen()-want.AvgDocLen()) > 1e-9 {
		t.Fatalf("AvgDocLen %v, esperaba %v", got.AvgDocLen(), want.AvgDocLen())
	}
	wantTerms := want.Terms()
	gotTerms := got.Terms()
	if !reflect.DeepEqual(gotTerms, append([]string(nil), wantTerms...)) && len(wantTerms) != 0 {
		t.Fatalf("el vocabulario no coincide (%d vs %d términos)", got.NumTerms(), len(wantTerms))
	}
	for d := 0; d < want.NumDocs(); d++ {
		if got.DocLen(uint32(d)) != want.DocLen(uint32(d)) {
			t.Fatalf("DocLen(%d) = %d, esperaba %d", d, got.DocLen(uint32(d)), want.DocLen(uint32(d)))
		}
	}
	for _, term := range want.Terms() {
		gp, err := got.Postings(term)
		if err != nil {
			t.Fatalf("Postings(%q): %v", term, err)
		}
		wp, _ := want.Postings(term)
		// el oráculo agrega en orden de iteración de mapa por documento, pero
		// los docID ya van crecientes
		if !reflect.DeepEqual(gp, wp) {
			t.Fatalf("Postings(%q) = %v, esperaba %v", term, gp, wp)
		}
		if got.DF(term) != want.DF(term) {
			t.Fatalf("DF(%q) = %d, esperaba %d", term, got.DF(term), want.DF(term))
		}
	}
	if ps, err := got.Postings("no-existe"); ps != nil || err != nil {
		t.Fatalf("término desconocido: %v, %v", ps, err)
	}
	if got.DF("no-existe") != 0 || got.DocLen(uint32(want.NumDocs()+5)) != 0 {
		t.Fatal("un término o docID desconocido debe dar 0")
	}
}

func TestBuilderMatchesOracle(t *testing.T) {
	corpus := genCorpus(1, 300, 150, 25)
	configs := []struct {
		name   string
		budget int
		fanIn  int
	}{
		{"un solo bloque", 1 << 30, 64},
		{"varios bloques", 50, 64},
		{"varios bloques y rondas", 50, 2},
		{"un bloque por documento", 1, 3},
	}
	for _, c := range configs {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			build(t, dir, corpus, Options{MaxBlockPostings: c.budget, MaxFanIn: c.fanIn})
			// reconstruir el oráculo (build ya lo llenó, pero acá lo pedimos de nuevo)
			mem := textindex.NewMemIndex()
			for _, d := range corpus {
				mem.AddDocument(d)
			}
			r, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			assertSameIndex(t, r, mem)
		})
	}
}

// El tamaño de bloque y el fan-in son detalles de construcción: los archivos
// finales tienen que ser idénticos byte a byte.
func TestBlockSizeDoesNotChangeFinalFiles(t *testing.T) {
	corpus := genCorpus(2, 250, 120, 30)
	dirA, dirB := t.TempDir(), t.TempDir()
	build(t, dirA, corpus, Options{MaxBlockPostings: 1 << 30})
	build(t, dirB, corpus, Options{MaxBlockPostings: 40, MaxFanIn: 2})
	for _, name := range []string{postingsFile, dictFile, docsFile} {
		a, _ := os.ReadFile(filepath.Join(dirA, name))
		b, _ := os.ReadFile(filepath.Join(dirB, name))
		if len(a) == 0 || !bytes.Equal(a, b) {
			t.Fatalf("%s difiere entre configuraciones (%d vs %d bytes)", name, len(a), len(b))
		}
	}
}

func TestBuilderStatsReflectConfiguration(t *testing.T) {
	corpus := genCorpus(3, 300, 150, 25)

	one, _ := build(t, t.TempDir(), corpus, Options{MaxBlockPostings: 1 << 30})
	if one.Blocks != 1 || one.MergeRounds != 0 {
		t.Fatalf("un solo bloque: Blocks=%d MergeRounds=%d", one.Blocks, one.MergeRounds)
	}

	many, _ := build(t, t.TempDir(), corpus, Options{MaxBlockPostings: 50, MaxFanIn: 2})
	if many.Blocks < 10 || many.MergeRounds == 0 {
		t.Fatalf("varios bloques: Blocks=%d MergeRounds=%d", many.Blocks, many.MergeRounds)
	}
	if many.BlockBytes <= one.BlockBytes {
		t.Fatalf("con rondas se escribe más a disco: %d vs %d", many.BlockBytes, one.BlockBytes)
	}
	if many.NumDocs != 300 || many.NumTerms == 0 || many.NumPostings == 0 || many.IndexBytes == 0 || many.Elapsed <= 0 {
		t.Fatalf("estadísticas incompletas: %+v", many)
	}
	if many.NumTerms != one.NumTerms || many.NumPostings != one.NumPostings || many.IndexBytes != one.IndexBytes {
		t.Fatalf("el índice final no debería depender de los bloques: %+v vs %+v", many, one)
	}
}

func TestBuilderLeavesOnlyFinalFiles(t *testing.T) {
	dir := t.TempDir()
	build(t, dir, genCorpus(4, 200, 100, 20), Options{MaxBlockPostings: 30, MaxFanIn: 2})
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if want := []string{dictFile, docsFile, postingsFile}; !reflect.DeepEqual(names, want) {
		t.Fatalf("archivos %v, esperaba %v", names, want)
	}
}

func TestCloseWithoutFinishRemovesTemps(t *testing.T) {
	dir := t.TempDir()
	b, err := NewBuilder(Options{Dir: dir, MaxBlockPostings: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range genCorpus(5, 100, 50, 15) {
		if _, err := b.AddDocument(d); err != nil {
			t.Fatal(err)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "spimi-*.tmp")); len(m) == 0 {
		t.Fatal("la prueba necesita bloques volcados")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("quedaron archivos: %v", entries)
	}
	if _, err := b.AddDocument([]string{"a"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("AddDocument tras Close: %v", err)
	}
}

func TestNewBuilderCleansStaleTemps(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "spimi-block-000001.tmp")
	other := filepath.Join(dir, "notas.txt")

	if err := os.WriteFile(stale, []byte("basura"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(other, []byte("no tocar"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := NewBuilder(Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("no limpió el temporal viejo")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("borró un archivo que no era suyo")
	}
}

func TestEmptyIndexAndEmptyDocuments(t *testing.T) {
	t.Run("sin documentos", func(t *testing.T) {
		dir := t.TempDir()
		stats, _ := build(t, dir, nil, Options{})
		if stats.NumDocs != 0 || stats.NumTerms != 0 || stats.Blocks != 0 {
			t.Fatalf("%+v", stats)
		}
		r, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		if r.NumDocs() != 0 || r.AvgDocLen() != 0 || r.NumTerms() != 0 {
			t.Fatal("índice vacío mal leído")
		}
	})
	t.Run("documentos sin tokens", func(t *testing.T) {
		dir := t.TempDir()
		corpus := [][]string{nil, {"a", "a", "b"}, {}, {"b"}, nil}
		_, mem := build(t, dir, corpus, Options{MaxBlockPostings: 2})
		r, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		assertSameIndex(t, r, mem)
		if r.DocLen(0) != 0 || r.DocLen(1) != 3 || r.DocNorm(0) != 0 {
			t.Fatal("un documento vacío conserva su docID con largo y norma 0")
		}
	})
}

func TestBuilderValidation(t *testing.T) {
	b, err := NewBuilder(Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.AddDocument([]string{"ok", ""}); !errors.Is(err, ErrEmptyTerm) {
		t.Fatalf("término vacío: %v", err)
	}
	if _, err := b.AddDocument([]string{strings.Repeat("x", maxTermLen+1)}); !errors.Is(err, ErrTermTooLong) {
		t.Fatalf("término largo: %v", err)
	}
	// un documento inválido no consume docID ni deja posteos
	if id, err := b.AddDocument([]string{"ok"}); err != nil || id != 0 {
		t.Fatalf("docID %d, err %v; esperaba 0", id, err)
	}
	if _, err := b.Finish(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Finish(); !errors.Is(err, ErrClosed) {
		t.Fatalf("segundo Finish: %v", err)
	}
	if _, err := b.AddDocument([]string{"a"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("AddDocument tras Finish: %v", err)
	}
	if _, err := NewBuilder(Options{}); err == nil {
		t.Fatal("aceptó Options sin Dir")
	}
}

func TestDocNormsMatchTermWeight(t *testing.T) {
	corpus := genCorpus(6, 200, 80, 20)
	weights := map[string]func(uint32, int, int) float64{
		"por defecto": nil,
		"tf crudo":    func(tf uint32, df, n int) float64 { return float64(tf) },
	}
	for name, w := range weights {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			_, mem := build(t, dir, corpus, Options{MaxBlockPostings: 60, MaxFanIn: 2, TermWeight: w})
			if w == nil {
				w = DefaultTermWeight
			}
			want := make([]float64, mem.NumDocs())
			for _, term := range mem.Terms() {
				ps, _ := mem.Postings(term)
				for _, p := range ps {
					x := w(p.TF, len(ps), mem.NumDocs())
					want[p.DocID] += x * x
				}
			}
			r, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			for d, w2 := range want {
				if got := r.DocNorm(uint32(d)); math.Abs(got-math.Sqrt(w2)) > 1e-9 {
					t.Fatalf("DocNorm(%d) = %v, esperaba %v", d, got, math.Sqrt(w2))
				}
			}
		})
	}
}

func TestReopenGivesSameResults(t *testing.T) {
	dir := t.TempDir()
	corpus := genCorpus(7, 150, 90, 20)
	_, mem := build(t, dir, corpus, Options{MaxBlockPostings: 40})
	for i := 0; i < 2; i++ {
		r, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		assertSameIndex(t, r, mem)
		r.Close()
	}
}

func TestReaderConcurrentPostings(t *testing.T) {
	dir := t.TempDir()
	_, mem := build(t, dir, genCorpus(8, 200, 100, 20), Options{MaxBlockPostings: 50})
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, term := range mem.Terms() {
				got, err := r.Postings(term)
				want, _ := mem.Postings(term)
				if err != nil || !reflect.DeepEqual(got, want) {
					errs <- fmt.Errorf("Postings(%q) falló: %v", term, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestOpenDetectsCorruptIndex(t *testing.T) {
	newIndex := func(t *testing.T) string {
		dir := t.TempDir()
		build(t, dir, genCorpus(9, 60, 40, 10), Options{MaxBlockPostings: 30})
		return dir
	}
	t.Run("falta un archivo", func(t *testing.T) {
		dir := newIndex(t)
		os.Remove(filepath.Join(dir, dictFile))
		if _, err := Open(dir); err == nil {
			t.Fatal("abrió un índice sin dict.dat")
		}
	})
	t.Run("magic del diccionario", func(t *testing.T) {
		dir := newIndex(t)
		os.WriteFile(filepath.Join(dir, dictFile), []byte("XXXXXXXXXXXX"), 0o644)
		if _, err := Open(dir); !errors.Is(err, ErrCorruptIndex) {
			t.Fatalf("error %v, esperaba ErrCorruptIndex", err)
		}
	})
	t.Run("postings truncado", func(t *testing.T) {
		dir := newIndex(t)
		p := filepath.Join(dir, postingsFile)
		st, _ := os.Stat(p)
		os.Truncate(p, st.Size()/2)
		if _, err := Open(dir); !errors.Is(err, ErrCorruptIndex) {
			t.Fatalf("error %v, esperaba ErrCorruptIndex", err)
		}
	})
	t.Run("tabla de documentos truncada", func(t *testing.T) {
		dir := newIndex(t)
		p := filepath.Join(dir, docsFile)
		st, _ := os.Stat(p)
		os.Truncate(p, st.Size()-3)
		if _, err := Open(dir); !errors.Is(err, ErrCorruptIndex) {
			t.Fatalf("error %v, esperaba ErrCorruptIndex", err)
		}
	})
}

func TestReaderKeepsPreviousGeneration(t *testing.T) {
	dir := t.TempDir()
	build(t, dir, genCorpus(10, 100, 50, 15), Options{MaxBlockPostings: 20})
	// simular que otro proceso está leyendo el índice mientras se publica uno nuevo
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	// publicar un nuevo índice
	build(t, dir, genCorpus(11, 120, 60, 15), Options{MaxBlockPostings: 20})
	// el reader viejo sigue viendo el índice viejo
	if r.NumDocs() != 100 {
		t.Fatalf("NumDocs tras publicar un nuevo índice = %d, esperaba 100", r.NumDocs())
	}
}

func TestRebuildOnDirectoryWithExistingIndex(t *testing.T) {
	dir := t.TempDir()
	first := genCorpus(12, 80, 40, 10)
	second := genCorpus(13, 90, 50, 10)

	build(t, dir, first, Options{MaxBlockPostings: 20})
	_, want := build(t, dir, second, Options{MaxBlockPostings: 20})

	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	assertSameIndex(t, r, want)
}

func TestTermWeightValidation(t *testing.T) {
	cases := []struct {
		name   string
		weight float64
	}{
		{"negativo", -1},
		{"nan", math.NaN()},
		{"infinito", math.Inf(1)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := NewBuilder(Options{
				Dir: t.TempDir(),
				TermWeight: func(uint32, int, int) float64 {
					return tc.weight
				},
			})
			if err != nil {
				t.Fatal(err)
			}

			if _, err := b.AddDocument([]string{"term"}); err != nil {
				t.Fatal(err)
			}

			if _, err := b.Finish(); err == nil {
				t.Fatal("aceptó un peso inválido")
			}
		})
	}
}
