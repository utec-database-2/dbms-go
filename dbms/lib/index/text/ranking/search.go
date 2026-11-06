package ranking

import (
	"fmt"

	"github.com/dbms-go/v2/dbms/lib/index/text/textindex"
)

// Method identifica el algoritmo; las puntuaciones de ambos no son comparables
// directamente porque utilizan escalas y funciones de relevancia diferentes.
type Method string

const (
	MethodTFIDF Method = "tfidf"
	MethodBM25  Method = "bm25"
)

// SearchOptions configura una consulta. Para parámetros habituales de BM25,
// usar DefaultSearchOptions. BM25K1=0 y BM25B=0 
type SearchOptions struct {
	TopK   int
	BM25K1 float64
	BM25B  float64
}

func DefaultSearchOptions(topK int) SearchOptions {
	return SearchOptions{TopK: topK, BM25K1: DefaultBM25K1, BM25B: DefaultBM25B}
}

// Score representa coseno [0,1] en TF-IDF; en BM25 no está normalizado.
type SearchResult struct {
	DocID uint32  `json:"doc_id"`
	Score float64 `json:"score"`
}

// Search ejecuta uno de los dos rankings SIN copiar el índice invertido.
// BM25 requiere InvertedIndex; TF-IDF exige además DocNorm, almacenado por SPIMI.
func Search(index textindex.InvertedIndex, method Method, queryTerms []string, opts SearchOptions) ([]SearchResult, error) {
	if index == nil {
		return nil, fmt.Errorf("ranking: indice nil")
	}
	if opts.TopK < 0 {
		return nil, fmt.Errorf("ranking: TopK debe ser >= 0")
	}

	switch method {
	case MethodTFIDF:
		withNorm, ok := index.(TFIDFIndex)
		if !ok {
			return nil, fmt.Errorf("ranking: TF-IDF necesita indice con DocNorm")
		}
		hits, err := SearchTFIDF(withNorm, queryTerms, opts.TopK)
		if err != nil {
			return nil, err
		}
		result := make([]SearchResult, len(hits))
		for i, hit := range hits {
			result[i] = SearchResult{DocID: hit.DocID, Score: hit.Score}
		}
		return result, nil

	case MethodBM25:
		hits, err := SearchBM25TopK(index, queryTerms, opts.TopK, opts.BM25K1, opts.BM25B)
		if err != nil {
			return nil, err
		}
		result := make([]SearchResult, len(hits))
		for i, hit := range hits {
			result[i] = SearchResult{DocID: hit.DocID, Score: hit.Score}
		}
		return result, nil

	default:
		return nil, fmt.Errorf("ranking: metodo desconocido %q (use tfidf o bm25)", method)
	}
}
