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

func buildRealSPIMI(t *testing.T, docs [][]string) *spimi.Reader {
	t.Helper()
	dir := t.TempDir()
	b, err := spimi.NewBuilder(spimi.Options{Dir: dir, MaxBlockPostings: 2, MaxFanIn: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	for i, tokens := range docs {
		id, err := b.AddDocument(tokens)
		if err != nil || id != uint32(i) {
			t.Fatalf("AddDocument(%d) id=%d, err=%v", i, id, err)
		}
	}
	if _, err := b.Finish(); err != nil {
		t.Fatal(err)
	}
	r, err := spimi.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func smallIndex(t *testing.T) *spimi.Reader {
	t.Helper()
	return buildRealSPIMI(t, [][]string{
		{"gat", "gat", "perr"}, // DocID 0
		{"gat"},                // DocID 1
		{"perr"},               // DocID 2
		{},                     // DocID 3 (stopwords/vacio)
	})
}

func TestTFIDFWeightCanonical(t *testing.T) {
	got := textindex.TFIDFWeight(2, 2, 4)
	want := (1 + math.Log(2)) * math.Log(2)
	if math.Abs(got-want) > 1e-14 {
		t.Fatalf("TFIDFWeight(2,2,4)=%v, esperado %v", got, want)
	}
	if spimi.DefaultTermWeight(2, 2, 4) != got {
		t.Fatal("SPIMI y TF-IDF deben usar identica formula")
	}
	if textindex.TFIDFWeight(0, 2, 4) != 0 || textindex.TFIDFWeight(1, 4, 4) != 0 {
		t.Fatal("el peso nulo debe ser cero")
	}
}

func TestTFIDFCosineManualWithRealSPIMI(t *testing.T) {
	r := smallIndex(t)
	hits, err := ranking.SearchTFIDF(r, []string{"gat"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("esperados 2 resultados: %v", hits)
	}
	if got := []uint32{hits[0].DocID, hits[1].DocID}; !reflect.DeepEqual(got, []uint32{1, 0}) {
		t.Fatalf("orden incorrecto: %v", hits)
	}
	if math.Abs(hits[0].Score-1) > 1e-12 {
		t.Fatalf("documento puro debe tener coseno 1: %v", hits)
	}
	want := (1 + math.Log(2)) / math.Hypot(1+math.Log(2), 1)
	if math.Abs(hits[1].Score-want) > 1e-12 {
		t.Fatalf("coseno doc0=%v, esperado=%v", hits[1].Score, want)
	}
	if r.DocNorm(3) != 0 {
		t.Fatal("documento vacio no debe tener norma")
	}
}

func TestTFIDFRepeatedQueryAndTopK(t *testing.T) {
	r := smallIndex(t)
	hits, err := ranking.SearchTFIDF(r, []string{"gat", "gat", "perr"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].DocID != 0 {
		t.Fatalf("consulta multitemino top1: %v", hits)
	}
	// La consulta y el doc 0 tienen exactamente el mismo vector TF-IDF.
	if math.Abs(hits[0].Score-1) > 1e-12 {
		t.Fatalf("similitud consigo mismo = %v", hits[0].Score)
	}
	hits, err = ranking.SearchTFIDF(r, []string{"gat", "desconocido"}, 10)
	if err != nil || len(hits) != 2 || hits[0].DocID != 1 {
		t.Fatalf("termino desconocido no debe cambiar el ranking: %v, %v", hits, err)
	}
}

func TestTFIDFEmptyUnknownAndZeroIDF(t *testing.T) {
	r := smallIndex(t)
	for _, terms := range [][]string{nil, {}, {"desconocido"}} {
		got, err := ranking.SearchTFIDF(r, terms, 5)
		if err != nil || len(got) != 0 {
			t.Fatalf("consulta %v: %v %v", terms, got, err)
		}
	}
	got, err := ranking.SearchTFIDF(r, []string{"gat"}, 0)
	if err != nil || len(got) != 0 {
		t.Fatalf("k=0: %v %v", got, err)
	}
	if _, err := ranking.SearchTFIDF(r, []string{"gat"}, -1); err == nil {
		t.Fatal("k negativo debe rechazarse")
	}
	if _, err := ranking.SearchTFIDF(r, []string{""}, 1); err == nil {
		t.Fatal("termino vacio debe rechazarse")
	}
	if _, err := ranking.SearchTFIDF(nil, []string{"gat"}, 1); err == nil {
		t.Fatal("indice nil debe rechazarse")
	}
	common := buildRealSPIMI(t, [][]string{{"comun"}, {"comun", "x"}, {"comun"}})
	got, err = ranking.SearchTFIDF(common, []string{"comun"}, 10)
	if err != nil || len(got) != 0 {
		t.Fatalf("df=N -> IDF=0: %v %v", got, err)
	}
}

func TestTFIDFTieBreakByDocID(t *testing.T) {
	r := buildRealSPIMI(t, [][]string{{"gat"}, {"gat"}, {"perr"}, {}})
	hits, err := ranking.SearchTFIDF(r, []string{"gat"}, 1)
	if err != nil || len(hits) != 1 || hits[0].DocID != 0 {
		t.Fatalf("empate top1: %v, %v", hits, err)
	}
}

type tracingIndex struct {
	*spimi.Reader
	requested []string
	fault     bool
}

func (i *tracingIndex) Postings(term string) ([]textindex.Posting, error) {
	i.requested = append(i.requested, term)
	if i.fault {
		return nil, errors.New("fallo de lectura")
	}
	return i.Reader.Postings(term)
}

func TestTFIDFReadsOnlyQueriedPostingsAndPropagatesErrors(t *testing.T) {
	r := smallIndex(t)
	i := &tracingIndex{Reader: r}
	if _, err := ranking.SearchTFIDF(i, []string{"gat", "gat"}, 2); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(i.requested, []string{"gat"}) {
		t.Fatalf("SPIMI deberia leer solo un termino: %v", i.requested)
	}
	i.requested = nil
	i.fault = true
	if _, err := ranking.SearchTFIDF(i, []string{"gat"}, 2); err == nil {
		t.Fatal("se perdio error de SPIMI.Postings")
	}
}

var _ ranking.TFIDFIndex = (*spimi.Reader)(nil)
