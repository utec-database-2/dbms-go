package ranking

import (
	"container/heap"
	"fmt"
	"math"
	"sort"

	"github.com/dbms-go/v2/dbms/lib/index/text/textindex"
)

const (
	DefaultBM25K1 = 1.2
	DefaultBM25B  = 0.75
)

// BM25Result representa un documento recuperado y su puntuación BM25.
type BM25Result struct {
	DocID uint32  `json:"doc_id"`
	Score float64 `json:"score"`
}

// bm25TermScore implementa Okapi BM25 con IDF positivo (suavizado):
//
//	idf = ln(1 + (N - df + 0.5) / (df + 0.5))
//	score = idf * tf*(k1+1) / (tf + k1*(1-b+b*dl/avgdl))
//
// Adaptado de la implementación existente en la rama frontend.
func bm25TermScore(tf, docLen, nDocs, df int, avgdl, k1, b float64) float64 {
	if tf <= 0 || docLen < 0 || nDocs <= 0 || df <= 0 || df > nDocs || avgdl <= 0 {
		return 0
	}
	idf := math.Log(1 + (float64(nDocs-df)+0.5)/(float64(df)+0.5))
	freq := float64(tf)
	lengthNorm := 1 - b + b*float64(docLen)/avgdl
	return idf * (freq * (k1 + 1)) / (freq + k1*lengthNorm)
}

// bm25MinHeap mantiene el PEOR resultado en la raíz para seleccionar Top-K.
// A igual puntuación, se prefiere el DocID menor.
type bm25MinHeap []BM25Result

func (h bm25MinHeap) Len() int { return len(h) }
func (h bm25MinHeap) Less(i, j int) bool {
	if h[i].Score == h[j].Score {
		return h[i].DocID > h[j].DocID
	}
	return h[i].Score < h[j].Score
}
func (h bm25MinHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *bm25MinHeap) Push(x any)   { *h = append(*h, x.(BM25Result)) }
func (h *bm25MinHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

func betterBM25(a, b BM25Result) bool {
	return a.Score > b.Score || (a.Score == b.Score && a.DocID < b.DocID)
}

// SearchBM25TopK calcula el ranking BM25 sobre un índice invertido real
// (por ejemplo *spimi.Reader), sin exportar ni duplicar el índice.
//
// queryTerms debe estar previamente normalizado mediante text.PreprocessText.
// Los términos repetidos de consulta se consideran UNA vez (sin factor qtf).
// Los parámetros recomendados inicialmente son DefaultBM25K1 y DefaultBM25B.
// El trabajo de selección Top-K es O(C log k), donde C es el número de
// documentos candidatos; solo se consultan las publicaciones solicitadas.
func SearchBM25TopK(index textindex.InvertedIndex, queryTerms []string, k int, k1, b float64) ([]BM25Result, error) {
	if index == nil {
		return nil, fmt.Errorf("BM25: índice nil")
	}
	if k < 0 {
		return nil, fmt.Errorf("BM25: k debe ser >= 0")
	}
	if math.IsNaN(k1) || math.IsInf(k1, 0) || math.IsNaN(b) || math.IsInf(b, 0) || k1 < 0 || b < 0 || b > 1 {
		return nil, fmt.Errorf("BM25: parámetros k1 o b inválidos")
	}
	if k == 0 || len(queryTerms) == 0 {
		return []BM25Result{}, nil
	}

	n := index.NumDocs()
	if n < 0 {
		return nil, fmt.Errorf("BM25: cantidad de documentos inválida")
	}
	if n == 0 {
		return []BM25Result{}, nil
	}
	avgdl := index.AvgDocLen()
	if math.IsNaN(avgdl) || math.IsInf(avgdl, 0) || avgdl < 0 {
		return nil, fmt.Errorf("BM25: longitud promedio inválida")
	}
	if avgdl == 0 { // Todos los documentos están vacíos.
		return []BM25Result{}, nil
	}

	// Ordenar los términos hace reproducible la acumulación de flotantes.
	seen := make(map[string]bool, len(queryTerms))
	terms := make([]string, 0, len(queryTerms))
	for _, term := range queryTerms {
		if term == "" {
			return nil, fmt.Errorf("BM25: término de consulta vacío")
		}
		if !seen[term] {
			seen[term] = true
			terms = append(terms, term)
		}
	}
	sort.Strings(terms)

	scores := make(map[uint32]float64)
	for _, term := range terms {
		df := index.DF(term)
		if df < 0 || df > n {
			return nil, fmt.Errorf("BM25: DF inválida para %q: %d", term, df)
		}
		if df == 0 {
			continue
		}
		postings, err := index.Postings(term)
		if err != nil {
			return nil, fmt.Errorf("BM25: Postings(%q): %w", term, err)
		}
		if len(postings) != df {
			return nil, fmt.Errorf("BM25: DF(%q)=%d, pero hay %d publicaciones", term, df, len(postings))
		}
		var prev uint32
		for i, p := range postings {
			if int(p.DocID) >= n || p.TF == 0 || (i > 0 && p.DocID <= prev) {
				return nil, fmt.Errorf("BM25: publicación inválida para %q (docID=%d, TF=%d)", term, p.DocID, p.TF)
			}
			prev = p.DocID
			dl := index.DocLen(p.DocID)
			if dl < int(p.TF) { // La longitud contiene, al menos, las ocurrencias del término.
				return nil, fmt.Errorf("BM25: DocLen(%d)=%d menor que TF=%d", p.DocID, dl, p.TF)
			}
			value := bm25TermScore(int(p.TF), dl, n, df, avgdl, k1, b)
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, fmt.Errorf("BM25: puntuación inválida para docID %d", p.DocID)
			}
			scores[p.DocID] += value
		}
	}

	top := make(bm25MinHeap, 0, min(k, len(scores)))
	for docID, score := range scores {
		if score <= 0 {
			continue
		}
		if math.IsNaN(score) || math.IsInf(score, 0) {
			return nil, fmt.Errorf("BM25: puntuación acumulada inválida para docID %d", docID)
		}
		hit := BM25Result{DocID: docID, Score: score}
		if len(top) < k {
			heap.Push(&top, hit)
		} else if betterBM25(hit, top[0]) {
			top[0] = hit
			heap.Fix(&top, 0)
		}
	}
	out := []BM25Result(top)
	sort.Slice(out, func(i, j int) bool { return betterBM25(out[i], out[j]) })
	return out, nil
}

// SearchBM25 conserva la API previa de la rama frontend: devuelve todos los
// documentos con puntuación positiva. Para benchmarks Top-K usar SearchBM25TopK.
func SearchBM25(index textindex.InvertedIndex, queryTerms []string, k1, b float64) ([]BM25Result, error) {
	if index == nil {
		return nil, fmt.Errorf("BM25: índice nil")
	}
	return SearchBM25TopK(index, queryTerms, index.NumDocs(), k1, b)
}
