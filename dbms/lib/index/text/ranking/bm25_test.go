package ranking_test

import (
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/dbms-go/v2/dbms/lib/index/text/ranking"
	"github.com/dbms-go/v2/dbms/lib/index/text/spimi"
	"github.com/dbms-go/v2/dbms/lib/index/text/textindex"
)

// buildBM25RealSPIMI construye un índice real sin depender de helpers TF-IDF.
func buildBM25RealSPIMI(t *testing.T, docs [][]string) *spimi.Reader {
	t.Helper()
	dir := t.TempDir()
	builder, err := spimi.NewBuilder(spimi.Options{Dir: dir, MaxBlockPostings: 2, MaxFanIn: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = builder.Close() })
	for i, doc := range docs {
		id, err := builder.AddDocument(doc)
		if err != nil || id != uint32(i) {
			t.Fatalf("documento %d: id=%d, err=%v", i, id, err)
		}
	}
	if _, err := builder.Finish(); err != nil {
		t.Fatal(err)
	}
	reader, err := spimi.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	return reader
}

func TestBM25ManualWithRealSPIMI(t *testing.T) {
	// Mismo corpus de las pruebas TF-IDF, pero una fórmula de ranking diferente.
	r := buildBM25RealSPIMI(t, [][]string{
		{"gat", "gat", "perr"}, // doc 0, dl=3
		{"gat"},                // doc 1, dl=1
		{"perr"},               // doc 2, dl=1
		{},                     // doc 3, dl=0
	})
	hits, err := ranking.SearchBM25TopK(r, []string{"gat"}, 10, ranking.DefaultBM25K1, ranking.DefaultBM25B)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].DocID != 1 || hits[1].DocID != 0 {
		t.Fatalf("Top-K BM25 incorrecto: %v", hits)
	}
	idf := math.Log(1 + (4.0-2.0+0.5)/(2.0+0.5))
	avgdl := 5.0 / 4.0
	want0 := idf * (2 * (1.2 + 1)) / (2 + 1.2*(1-0.75+0.75*3/avgdl))
	want1 := idf * (1 * (1.2 + 1)) / (1 + 1.2*(1-0.75+0.75*1/avgdl))
	if math.Abs(hits[0].Score-want1) > 1e-12 || math.Abs(hits[1].Score-want0) > 1e-12 {
		t.Fatalf("BM25 no coincide con fórmula manual: got=%v, esperado=[%v %v]", hits, want1, want0)
	}
	all, err := ranking.SearchBM25(r, []string{"gat"}, ranking.DefaultBM25K1, ranking.DefaultBM25B)
	if err != nil || !reflect.DeepEqual(hits, all) {
		t.Fatalf("API heredada difiere de Top-K: %v vs %v (%v)", hits, all, err)
	}
}

func TestBM25TopKRepeatedQueryAndTie(t *testing.T) {
	r := buildBM25RealSPIMI(t, [][]string{{"gat"}, {"gat"}, {"perr"}, {}})
	hits, err := ranking.SearchBM25TopK(r, []string{"gat", "gat"}, 1, 1.2, 0.75)
	if err != nil || len(hits) != 1 || hits[0].DocID != 0 {
		t.Fatalf("Top-K y desempate: %v (%v)", hits, err)
	}
	once, err := ranking.SearchBM25TopK(r, []string{"gat"}, 3, 1.2, 0.75)
	if err != nil || len(once) != 2 || math.Abs(hits[0].Score-once[0].Score) > 1e-14 {
		t.Fatalf("términos repetidos modificaron el BM25: %v, %v (%v)", hits, once, err)
	}
	if len(once) > 1 && once[1].DocID != 1 {
		t.Fatalf("empate debe preferir DocID menor: %v", once)
	}
}

func TestBM25ParametersAndNoPositiveMatch(t *testing.T) {
	r := buildBM25RealSPIMI(t, [][]string{{"gat", "gat", "perr"}, {"gat"}, {"perr"}, {}})
	if hits, err := ranking.SearchBM25TopK(r, []string{"desconocido"}, 5, 1.2, 0.75); err != nil || len(hits) != 0 {
		t.Fatalf("sin coincidencias: %v (%v)", hits, err)
	}
	if hits, err := ranking.SearchBM25TopK(r, []string{"gat"}, 0, 1.2, 0.75); err != nil || len(hits) != 0 {
		t.Fatalf("k cero: %v (%v)", hits, err)
	}
	for _, tc := range []struct {
		k     int
		k1, b float64
	}{
		{-1, 1.2, 0.75}, {1, -1, 0.75}, {1, 1.2, -0.1}, {1, 1.2, 1.1},
		{1, math.NaN(), 0.75}, {1, math.Inf(1), 0.75}, {1, 1.2, math.NaN()},
	} {
		if _, err := ranking.SearchBM25TopK(r, []string{"gat"}, tc.k, tc.k1, tc.b); err == nil {
			t.Fatalf("debió fallar para parámetros %+v", tc)
		}
	}
	if _, err := ranking.SearchBM25TopK(nil, []string{"gat"}, 1, 1.2, 0.75); err == nil {
		t.Fatal("índice nil debe rechazarse")
	}
	if _, err := ranking.SearchBM25TopK(r, []string{""}, 1, 1.2, 0.75); err == nil {
		t.Fatal("término vacío debe rechazarse")
	}
	empty := buildBM25RealSPIMI(t, [][]string{{}, {}})
	if got, err := ranking.SearchBM25TopK(empty, []string{"gat"}, 10, 1.2, 0.75); err != nil || len(got) != 0 {
		t.Fatalf("todos los documentos vacíos: %v, %v", got, err)
	}
}

type bm25TracingIndex struct {
	*spimi.Reader
	requested []string
	failure   bool
	wrongDF   bool
}

func (i *bm25TracingIndex) Postings(term string) ([]textindex.Posting, error) {
	i.requested = append(i.requested, term)
	if i.failure {
		return nil, errors.New("lectura fallida")
	}
	return i.Reader.Postings(term)
}
func (i *bm25TracingIndex) DF(term string) int {
	if i.wrongDF {
		return i.Reader.DF(term) + 1
	}
	return i.Reader.DF(term)
}

func TestBM25ReadsOnlyQueriedPostingsAndPropagatesErrors(t *testing.T) {
	r := buildBM25RealSPIMI(t, [][]string{{"gat", "gat", "perr"}, {"gat"}, {"perr"}, {}})
	i := &bm25TracingIndex{Reader: r}
	if _, err := ranking.SearchBM25TopK(i, []string{"gat", "gat"}, 2, 1.2, 0.75); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(i.requested, []string{"gat"}) {
		t.Fatalf("BM25 consultó publicaciones inesperadas: %v", i.requested)
	}
	i.failure = true
	if _, err := ranking.SearchBM25TopK(i, []string{"gat"}, 2, 1.2, 0.75); err == nil {
		t.Fatal("no propagó error de Postings")
	}
	i.failure = false
	i.wrongDF = true
	if _, err := ranking.SearchBM25TopK(i, []string{"gat"}, 2, 1.2, 0.75); err == nil {
		t.Fatal("no detectó DF inconsistente")
	}
}

func TestBM25CommonTermKeepsPositiveIDF(t *testing.T) {
	r := buildBM25RealSPIMI(t, [][]string{{"comun"}, {"comun", "otro"}, {"comun"}})
	hits, err := ranking.SearchBM25TopK(r, []string{"comun"}, 10, ranking.DefaultBM25K1, ranking.DefaultBM25B)
	if err != nil || len(hits) != 3 {
		t.Fatalf("BM25 debe mantener IDF positivo incluso cuando df=N: %v (%v)", hits, err)
	}
	for _, hit := range hits {
		if hit.Score <= 0 {
			t.Fatalf("BM25 tiene puntuación no positiva para el término común: %v", hits)
		}
	}
}
