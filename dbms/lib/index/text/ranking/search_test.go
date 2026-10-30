package ranking_test

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"

	"github.com/dbms-go/v2/dbms/lib/index/text/ranking"
	"github.com/dbms-go/v2/dbms/lib/index/text/textindex"
)

func TestUnifiedSearchMatchesDirectRankers(t *testing.T) {
	// Construimos un índice SPIMI REAL; los métodos existentes ya cuentan
	// con pruebas independientes de sus fórmulas matemáticas.
	index := buildRealSPIMI(t, [][]string{
		{"gat", "gat", "perr"},
		{"gat"},
		{"perr"},
		{},
	})
	terms := []string{"gat", "perr", "gat"}
	opts := ranking.DefaultSearchOptions(2)

	gotTF, err := ranking.Search(index, ranking.MethodTFIDF, terms, opts)
	if err != nil {
		t.Fatal(err)
	}
	directTF, err := ranking.SearchTFIDF(index, terms, 2)
	if err != nil {
		t.Fatal(err)
	}
	wantTF := make([]ranking.SearchResult, len(directTF))
	for i, h := range directTF {
		wantTF[i] = ranking.SearchResult{DocID: h.DocID, Score: h.Score}
	}
	if !reflect.DeepEqual(gotTF, wantTF) {
		t.Fatalf("TF-IDF via Search: got %v, want %v", gotTF, wantTF)
	}

	gotBM, err := ranking.Search(index, ranking.MethodBM25, terms, opts)
	if err != nil {
		t.Fatal(err)
	}
	directBM, err := ranking.SearchBM25TopK(index, terms, 2, opts.BM25K1, opts.BM25B)
	if err != nil {
		t.Fatal(err)
	}
	wantBM := make([]ranking.SearchResult, len(directBM))
	for i, h := range directBM {
		wantBM[i] = ranking.SearchResult{DocID: h.DocID, Score: h.Score}
	}
	if !reflect.DeepEqual(gotBM, wantBM) {
		t.Fatalf("BM25 via Search: got %v, want %v", gotBM, wantBM)
	}
	if len(gotTF) > opts.TopK || len(gotBM) > opts.TopK {
		t.Fatalf("Top-K no respetado: tfidf=%v bm25=%v", gotTF, gotBM)
	}
}

func TestUnifiedSearchValidationsAndDefaults(t *testing.T) {
	index := buildRealSPIMI(t, [][]string{{"gat"}, {"gat", "perr"}, {"perr"}, {}})
	opts := ranking.DefaultSearchOptions(10)
	if opts.BM25K1 != ranking.DefaultBM25K1 || opts.BM25B != ranking.DefaultBM25B {
		t.Fatal("valores por defecto BM25 inesperados")
	}
	if _, err := ranking.Search(index, ranking.Method("no-existe"), []string{"gat"}, opts); err == nil {
		t.Fatal("debe rechazar metodos desconocidos")
	}
	if _, err := ranking.Search(nil, ranking.MethodTFIDF, []string{"gat"}, opts); err == nil {
		t.Fatal("debe rechazar indice nil")
	}
	badK := opts
	badK.TopK = -1
	if _, err := ranking.Search(index, ranking.MethodBM25, []string{"gat"}, badK); err == nil {
		t.Fatal("debe rechazar TopK negativo")
	}
	badBM := opts
	badBM.BM25B = math.NaN()
	if _, err := ranking.Search(index, ranking.MethodBM25, []string{"gat"}, badBM); err == nil {
		t.Fatal("debe delegar la validacion de parametros BM25")
	}
	if got, err := ranking.Search(index, ranking.MethodTFIDF, nil, opts); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("TF-IDF consulta vacia debe devolver slice vacio: %v %v", got, err)
	}
	if got, err := ranking.Search(index, ranking.MethodBM25, []string{"noexiste"}, opts); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("BM25 sin candidatos debe devolver slice vacio: %v %v", got, err)
	}
	got, err := ranking.Search(index, ranking.MethodTFIDF, []string{"noexiste"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(got)
	if err != nil || string(encoded) != "[]" {
		t.Fatalf("JSON vacio incorrecto: %s %v", encoded, err)
	}
}

func TestUnifiedSearchBM25WorksWithMemIndexAndTFIDFRequiresNorm(t *testing.T) {
	mem := textindex.NewMemIndex()
	mem.AddDocument([]string{"gat", "gat"})
	mem.AddDocument([]string{"perr"})
	opts := ranking.DefaultSearchOptions(2)
	hits, err := ranking.Search(mem, ranking.MethodBM25, []string{"gat"}, opts)
	if err != nil || len(hits) != 1 || hits[0].DocID != 0 {
		t.Fatalf("BM25 con MemIndex: %v, %v", hits, err)
	}
	if _, err := ranking.Search(mem, ranking.MethodTFIDF, []string{"gat"}, opts); err == nil {
		t.Fatal("TF-IDF debe exigir norma documental")
	}
}

func TestUnifiedSearchBM25AndTFIDFScalesDiffer(t *testing.T) {
	// BM25 asigna IDF positivo a un término presente en todo el corpus;
	// la variante TF-IDF elegida asigna IDF=0.
	index := buildRealSPIMI(t, [][]string{{"comun"}, {"comun", "otro"}, {"comun"}})
	opts := ranking.DefaultSearchOptions(10)
	tf, err := ranking.Search(index, ranking.MethodTFIDF, []string{"comun"}, opts)
	if err != nil || len(tf) != 0 {
		t.Fatalf("TF-IDF comun debe dar []: %v (%v)", tf, err)
	}
	bm, err := ranking.Search(index, ranking.MethodBM25, []string{"comun"}, opts)
	if err != nil || len(bm) != 3 {
		t.Fatalf("BM25 comun debe dar 3: %v (%v)", bm, err)
	}
}
