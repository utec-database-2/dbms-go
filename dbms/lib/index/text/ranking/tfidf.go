package ranking

import (
	"container/heap"
	"fmt"
	"math"
	"sort"

	"github.com/dbms-go/v2/dbms/lib/index/text/textindex"
)
type TFIDFIndex interface {
	textindex.InvertedIndex
	DocNorm(docID uint32) float64
}

// TFIDFResul -> documento recuperado y su coseno [0, 1].
type TFIDFResult struct {
	DocID uint32  `json:"doc_id"`
	Score float64 `json:"score"`
}

// tfidfMinHeap mantiene arriba el PEOR candidato (menor score y, en empate,
// mayor DocID). Permite Top-K sin ordenar todos los candidatos.
type tfidfMinHeap []TFIDFResult

func (h tfidfMinHeap) Len() int { return len(h) }
func (h tfidfMinHeap) Less(i, j int) bool {
	if h[i].Score == h[j].Score {
		return h[i].DocID > h[j].DocID
	}
	return h[i].Score < h[j].Score
}
func (h tfidfMinHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *tfidfMinHeap) Push(x any)   { *h = append(*h, x.(TFIDFResult)) }
func (h *tfidfMinHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

func betterTFIDF(a, b TFIDFResult) bool {
	return a.Score > b.Score || (a.Score == b.Score && a.DocID < b.DocID)
}

// SearchTFIDF calcula la similitud coseno sobre el INDICE SPIMI REAL.
// Peso de documento y consulta: (1 + ln(tf)) * ln(N/df).
// Anterior TF Python (desactivada): 1 + log10(tf).
// Anterior IDF Python (desactivada): log10(N/df).
// El IDF=0 para df=N; los documentos con coseno cero no se devuelven.
// Costo por consulta: O(sum(|postings(t)|) + C log(k)), C=candidatos.
func SearchTFIDF(index TFIDFIndex, queryTerms []string, k int) ([]TFIDFResult, error) {
	if index == nil {
		return nil, fmt.Errorf("TF-IDF: indice nil")
	}
	if k < 0 {
		return nil, fmt.Errorf("TF-IDF: k debe ser >= 0")
	}
	if k == 0 || len(queryTerms) == 0 {
		return []TFIDFResult{}, nil
	}
	n := index.NumDocs()
	if n < 0 {
		return nil, fmt.Errorf("TF-IDF: numero de documentos invalido")
	}
	if n == 0 {
		return []TFIDFResult{}, nil
	}

	qtf := make(map[string]uint32, len(queryTerms))
	for _, t := range queryTerms {
		if t == "" {
			return nil, fmt.Errorf("TF-IDF: termino de consulta vacio")
		}
		if qtf[t] == math.MaxUint32 {
			return nil, fmt.Errorf("TF-IDF: frecuencia de consulta excedida")
		}
		qtf[t]++
	}
	terms := make([]string, 0, len(qtf))
	for term := range qtf {
		terms = append(terms, term)
	}
	sort.Strings(terms) // orden fijo para que la suma de flotantes sea reproducible

	dot := make(map[uint32]float64)
	var qNormSquared float64
	for _, term := range terms {
		df := index.DF(term)
		if df < 0 || df > n {
			return nil, fmt.Errorf("TF-IDF: DF invalida para %q: %d", term, df)
		}
		// Un termino ausente o en todos los documentos tiene IDF=0.
		if df == 0 || df == n {
			continue
		}
		qWeight := textindex.TFIDFWeight(qtf[term], df, n)
		qNormSquared += qWeight * qWeight

		postings, err := index.Postings(term)
		if err != nil {
			return nil, fmt.Errorf("TF-IDF: Postings(%q): %w", term, err)
		}
		if len(postings) != df {
			return nil, fmt.Errorf("TF-IDF: DF(%q)=%d pero hay %d postings", term, df, len(postings))
		}
		for _, p := range postings {
			if int(p.DocID) >= n || p.TF == 0 {
				return nil, fmt.Errorf("TF-IDF: posting invalido para %q (docID=%d, TF=%d)", term, p.DocID, p.TF)
			}
			dot[p.DocID] += textindex.TFIDFWeight(p.TF, df, n) * qWeight
		}
	}
	if qNormSquared == 0 {
		return []TFIDFResult{}, nil
	}
	qNorm := math.Sqrt(qNormSquared)
	top := make(tfidfMinHeap, 0, min(k, len(dot)))
	for id, product := range dot {
		if product <= 0 {
			continue
		}
		norm := index.DocNorm(id)
		if norm <= 0 || math.IsNaN(norm) || math.IsInf(norm, 0) {
			return nil, fmt.Errorf("TF-IDF: DocNorm invalida para docID %d: %v", id, norm)
		}
		score := product / (norm * qNorm)
		if math.IsNaN(score) || math.IsInf(score, 0) {
			return nil, fmt.Errorf("TF-IDF: coseno invalido para docID %d", id)
		}
		// Tolerancia de redondeo: un coseno matematico no supera 1.
		score = math.Min(1, math.Max(0, score))
		hit := TFIDFResult{DocID: id, Score: score}
		if len(top) < k {
			heap.Push(&top, hit)
		} else if betterTFIDF(hit, top[0]) {
			top[0] = hit
			heap.Fix(&top, 0)
		}
	}
	out := []TFIDFResult(top)
	sort.Slice(out, func(i, j int) bool { return betterTFIDF(out[i], out[j]) })
	return out, nil
}
