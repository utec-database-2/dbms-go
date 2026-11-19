package ranking

import (
	"container/heap"
	"fmt"
	"github.com/dbms-go/v2/dbms/lib/benchclock"
	"github.com/dbms-go/v2/dbms/lib/index/text/textindex"
	"math"
	"sort"
	"time"
)

type ProfileStats struct {
	Index, Accumulate, TopK time.Duration
	Candidates, Postings    int
}

func SearchProfiled(index textindex.InvertedIndex, method Method, terms []string, opts SearchOptions) ([]SearchResult, ProfileStats, error) {
	var p ProfileStats
	switch method {
	case MethodTFIDF:
		ix, ok := index.(TFIDFIndex)
		if !ok {
			return nil, p, fmt.Errorf("TF-IDF requiere DocNorm")
		}
		hits, err := SearchTFIDFProfiled(ix, terms, opts.TopK, &p)
		out := make([]SearchResult, len(hits))
		for i, h := range hits {
			out[i] = SearchResult{DocID: h.DocID, Score: h.Score}
		}
		return out, p, err
	case MethodBM25:
		hits, err := SearchBM25TopKProfiled(index, terms, opts.TopK, opts.BM25K1, opts.BM25B, &p)
		out := make([]SearchResult, len(hits))
		for i, h := range hits {
			out[i] = SearchResult{DocID: h.DocID, Score: h.Score}
		}
		return out, p, err
	}
	return nil, p, fmt.Errorf("metodo desconocido")
}
func SearchTFIDFProfiled(index TFIDFIndex, queryTerms []string, k int, pstats *ProfileStats) ([]TFIDFResult, error) {
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
		clock := benchclock.Now()
		df := index.DF(term)
		pstats.Index += benchclock.Since(clock)
		if df < 0 || df > n {
			return nil, fmt.Errorf("TF-IDF: DF invalida para %q: %d", term, df)
		}
		// Un termino ausente o en todos los documentos tiene IDF=0.
		if df == 0 || df == n {
			continue
		}
		qWeight := textindex.TFIDFWeight(qtf[term], df, n)
		qNormSquared += qWeight * qWeight

		clock = benchclock.Now()
		postings, err := index.Postings(term)
		pstats.Index += benchclock.Since(clock)
		pstats.Postings += len(postings)
		if err != nil {
			return nil, fmt.Errorf("TF-IDF: Postings(%q): %w", term, err)
		}
		if len(postings) != df {
			return nil, fmt.Errorf("TF-IDF: DF(%q)=%d pero hay %d postings", term, df, len(postings))
		}
		clock = benchclock.Now()
		for _, p := range postings {
			if int(p.DocID) >= n || p.TF == 0 {
				return nil, fmt.Errorf("TF-IDF: posting invalido para %q (docID=%d, TF=%d)", term, p.DocID, p.TF)
			}
			dot[p.DocID] += textindex.TFIDFWeight(p.TF, df, n) * qWeight
		}
		pstats.Accumulate += benchclock.Since(clock)
	}
	if qNormSquared == 0 {
		return []TFIDFResult{}, nil
	}
	qNorm := math.Sqrt(qNormSquared)
	pstats.Candidates = len(dot)
	clockTop := benchclock.Now()
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
	pstats.TopK += benchclock.Since(clockTop)
	return out, nil
}
func SearchBM25TopKProfiled(index textindex.InvertedIndex, queryTerms []string, k int, k1, b float64, pstats *ProfileStats) ([]BM25Result, error) {
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
		clock := benchclock.Now()
		df := index.DF(term)
		pstats.Index += benchclock.Since(clock)
		if df < 0 || df > n {
			return nil, fmt.Errorf("BM25: DF inválida para %q: %d", term, df)
		}
		if df == 0 {
			continue
		}
		clock = benchclock.Now()
		postings, err := index.Postings(term)
		pstats.Index += benchclock.Since(clock)
		pstats.Postings += len(postings)
		if err != nil {
			return nil, fmt.Errorf("BM25: Postings(%q): %w", term, err)
		}
		if len(postings) != df {
			return nil, fmt.Errorf("BM25: DF(%q)=%d, pero hay %d publicaciones", term, df, len(postings))
		}
		var prev uint32
		clock = benchclock.Now()
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
		pstats.Accumulate += benchclock.Since(clock)
	}

	pstats.Candidates = len(scores)
	clockTop := benchclock.Now()
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
	pstats.TopK += benchclock.Since(clockTop)
	return out, nil
}
