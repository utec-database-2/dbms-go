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
	"github.com/dbms-go/v2/dbms/lib/text"
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
	if !reflect.DeepEqual(gotTerms, wantTerms) {
		t.Fatalf("el vocabulario no coincide: got %v vs want %v (%d vs %d términos)", gotTerms, wantTerms, got.NumTerms(), len(wantTerms))
	}
	for d := 0; d < want.NumDocs(); d++ {
		if got.DocLen(uint32(d)) != want.DocLen(uint32(d)) {
			t.Fatalf("DocLen(%d) = %d, esperaba %d", d, got.DocLen(uint32(d)), want.DocLen(uint32(d)))
		}
	}
	for _, term := range wantTerms {
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
		a, err := os.ReadFile(filepath.Join(dirA, name))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(dirB, name))
		if err != nil {
			t.Fatal(err)
		}
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
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
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
	if entries, err := os.ReadDir(dir); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
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
		if err := os.Remove(filepath.Join(dir, dictFile)); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir); err == nil {
			t.Fatal("abrió un índice sin dict.dat")
		}
	})
	t.Run("magic del diccionario", func(t *testing.T) {
		dir := newIndex(t)
		if err := os.WriteFile(filepath.Join(dir, dictFile), []byte("XXXXXXXXXXXX"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir); !errors.Is(err, ErrCorruptIndex) {
			t.Fatalf("error %v, esperaba ErrCorruptIndex", err)
		}
	})
	t.Run("postings truncado", func(t *testing.T) {
		dir := newIndex(t)
		p := filepath.Join(dir, postingsFile)
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(p, st.Size()/2); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir); !errors.Is(err, ErrCorruptIndex) {
			t.Fatalf("error %v, esperaba ErrCorruptIndex", err)
		}
	})
	t.Run("tabla de documentos truncada", func(t *testing.T) {
		dir := newIndex(t)
		p := filepath.Join(dir, docsFile)
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(p, st.Size()-3); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir); !errors.Is(err, ErrCorruptIndex) {
			t.Fatalf("error %v, esperaba ErrCorruptIndex", err)
		}
	})
}

func TestReaderKeepsPreviousGeneration(t *testing.T) {
	dir := t.TempDir()
	first := genCorpus(10, 100, 50, 15)
	second := genCorpus(11, 120, 60, 15)

	_, want := build(t, dir, first, Options{MaxBlockPostings: 20})

	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	// simular que otro proceso está leyendo el índice mientras se publica uno nuevo
	build(t, dir, second, Options{MaxBlockPostings: 20})

	assertSameIndex(t, r, want)
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
		{"infinito positivo", math.Inf(1)},
		{"infinito negativo", math.Inf(-1)},
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

			if _, err := b.Finish(); !errors.Is(err, ErrTermWeight) {
				t.Fatalf("error = %v, esperaba ErrTermWeight", err)
			}
		})
	}
}

func TestPreprocessTextAndBuilder(t *testing.T) {
	inputs := []string{
		"Hola, la programación.",
		"Bienvenido al mundo de la programación.",
		"El rápido zorro marrón salta sobre el perro perezoso.",
		"El zorro rápido salta y salta.",
	}
	expected := [][]string{
		{"hol", "program"},
		{"bienven", "mund", "program"},
		{"rap", "zorr", "marron", "salt", "perr", "perez"},
		{"zorr", "rap", "salt", "salt"},
	}

	dir := t.TempDir()
	b, err := NewBuilder(Options{Dir: dir, MaxBlockPostings: 2, MaxFanIn: 2})
	if err != nil {
		t.Fatal(err)
	}
	want := textindex.NewMemIndex()
	for i, input := range inputs {
		tokens, err := text.PreprocessText(input)
		if err != nil {
			t.Fatalf("PreprocessText(%q): %v", input, err)
		}
		if !reflect.DeepEqual(tokens, expected[i]) {
			t.Fatalf("PreprocessText(%q) = %q, esperaba %q", input, tokens, expected[i])
		}
		id, err := b.AddDocument(tokens)
		if err != nil {
			t.Fatalf("AddDocument(%q): %v", input, err)
		}
		if id != want.AddDocument(tokens) {
			t.Fatalf("docID %d inesperado para el documento %d", id, i)
		}
	}

	stats, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if stats.NumDocs != len(inputs) || stats.NumTerms != len(want.Terms()) {
		t.Fatalf("estadísticas incompletas: %+v", stats)
	}
	if stats.Blocks < 2 || stats.MergeRounds == 0 {
		t.Fatalf("el flujo no ejercitó SPIMI y merge: %+v", stats)
	}

	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	assertSameIndex(t, r, want)
}

func TestBuilderPreservesTermFrequency(t *testing.T) {
	// asume que estos ya estan procesados como terminos (generalmente deben estas palabras ser
	// procesadas pero esta es prueba de frecuencia de terminos)
	corpus := [][]string{
		{"gato", "gato", "corre"}, // Document 0: "gato" aparece dos veces, "corre" solo una vez
	}

	// Crear Builder, agregar tokens, finalizar y consultar postings.
	builder, err := NewBuilder(Options{Dir: t.TempDir(), MaxBlockPostings: 10})
	if err != nil {
		t.Fatal(err)
	}

	docID, err := builder.AddDocument(corpus[0])
	if err != nil {
		t.Fatal(err)
	}
	if docID != 0 {
		t.Fatalf("docID %d inesperado, esperaba 0", docID)
	}

	stats, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if stats.NumDocs != 1 || stats.NumTerms != 2 {
		t.Fatalf("estadísticas incompletas: %+v", stats)
	}

	reader, err := Open(builder.opts.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	postingsGato, err := reader.Postings("gato")
	if err != nil {
		t.Fatalf("Postings(\"término de gato\"): %v", err)
	}
	if len(postingsGato) != 1 || postingsGato[0].DocID != 0 || postingsGato[0].TF != 2 {
		t.Fatalf("Postings(\"término de gato\") = %v, esperaba DocID 0 con TF 2", postingsGato)
	}

	postingsCorre, err := reader.Postings("corre")
	if err != nil {
		t.Fatalf("Postings(\"término de corre\"): %v", err)
	}
	if len(postingsCorre) != 1 || postingsCorre[0].DocID != 0 || postingsCorre[0].TF != 1 {
		t.Fatalf("Postings(\"término de corre\") = %v, esperaba DocID 0 con TF 1", postingsCorre)
	}
}

func TestPreprocessedTermFrequency(t *testing.T) {
	documents := []string{
		"el gato y el perro corre corre", // "el" y "y" son stop words, "corre" aparece dos veces
	}
	corpus := make([][]string, len(documents))

	corpus[0], _ = text.PreprocessText(documents[0])

	builder, err := NewBuilder(Options{Dir: t.TempDir(), MaxBlockPostings: 10})
	if err != nil {
		t.Fatal(err)
	}

	docID, err := builder.AddDocument(corpus[0])
	if err != nil {
		t.Fatal(err)
	}
	if docID != 0 {
		t.Fatalf("docID %d inesperado, esperaba 0", docID)
	}

	stats, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if stats.NumDocs != 1 || stats.NumTerms != 3 { // Solo "gato", "perro" y "corre" deberían ser indexados
		t.Fatalf("estadísticas incompletas: %+v", stats)
	}

	reader, err := Open(builder.opts.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	term, err := text.PreprocessOneWord("corre")
	if err != nil {
		t.Fatalf("PreprocessOneWord(\"corre\"): %v", err)
	}

	postingsCorre, err := reader.Postings(term)
	if err != nil {
		t.Fatalf("Postings(\"término de corre\"): %v", err)
	}
	if len(postingsCorre) != 1 || postingsCorre[0].DocID != 0 || postingsCorre[0].TF != 2 {
		t.Fatalf("Postings(\"término de corre\") = %v, esperaba DocID 0 con TF 2", postingsCorre)
	}
}

func TestPreprocessedStopWordsAreNotIndexed(t *testing.T) {
	documents := []string{
		"el gato y el perro corre", // "el" y "y" son stop words
	}
	corpus := make([][]string, len(documents))

	corpus[0], _ = text.PreprocessText(documents[0])

	builder, err := NewBuilder(Options{Dir: t.TempDir(), MaxBlockPostings: 10})
	if err != nil {
		t.Fatal(err)
	}

	// como solo tenemos un caso solo usaremos el primer documento para verificar que las stop words no se indexaron
	docID, err := builder.AddDocument(corpus[0])
	if err != nil {
		t.Fatal(err)
	}
	if docID != 0 {
		t.Fatalf("docID %d inesperado, esperaba 0", docID)
	}

	stats, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if stats.NumDocs != 1 || stats.NumTerms != 3 { // Solo "gato", "perro" y "corre" deberían ser indexados
		t.Fatalf("estadísticas incompletas: %+v", stats)
	}

	reader, err := Open(builder.opts.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	if reader.DF("el") != 0 || reader.DF("y") != 0 {
		t.Fatal("\"el\" o \"y\" estan en el vocabulario")
	}

	termGato, err := text.PreprocessOneWord("gato")
	if err != nil {
		t.Fatalf("PreprocessOneWord(\"gato\"): %v", err)
	}
	postingsGato, err := reader.Postings(termGato)
	if err != nil {
		t.Fatalf("Postings(\"término de gato\"): %v", err)
	}
	if len(postingsGato) != 1 || postingsGato[0].DocID != 0 || postingsGato[0].TF != 1 {
		t.Fatalf("Postings(\"término de gato\") = %v, esperaba DocID 0 con TF 1", postingsGato)
	}

	termPerro, err := text.PreprocessOneWord("perro")
	if err != nil {
		t.Fatalf("PreprocessOneWord(\"perro\"): %v", err)
	}
	postingsPerro, err := reader.Postings(termPerro)
	if err != nil {
		t.Fatalf("Postings(\"término de perro\"): %v", err)
	}
	if len(postingsPerro) != 1 || postingsPerro[0].DocID != 0 || postingsPerro[0].TF != 1 {
		t.Fatalf("Postings(\"término de perro\") = %v, esperaba DocID 0 con TF 1", postingsPerro)
	}
}
