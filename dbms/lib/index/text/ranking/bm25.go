package ranking

import (
	"fmt"
	"math"
	"sort"

	"github.com/dbms-go/v2/dbms/lib/index/text/textindex"
)

// BM25Result representa un documento y su puntuación.
type BM25Result struct {
	DocID uint32
	Score float64
}

// bm25TermScore calcula la contribución de un término.
func bm25TermScore(
	tf, docLen, nDocs, df int,
	avgdl, k1, b float64,
) float64 {
	if tf <= 0 || docLen < 0 ||
		nDocs <= 0 || df <= 0 || df > nDocs {
		return 0
	}

	if avgdl <= 0 || k1 < 0 || b < 0 || b > 1 {
		return 0
	}

	idf := math.Log(
		1 + (float64(nDocs-df)+0.5)/
			(float64(df)+0.5),
	)

	freq := float64(tf)

	lengthNorm := 1 - b + b*float64(docLen)/avgdl

	// Contribución BM25 del término.
	return idf * (freq * (k1 + 1)) /
		(freq + k1*lengthNorm)
}

// SearchBM25 busca documentos con los términos indicados
// y los devuelve ordenados por puntuación descendente.
// queryTerms debe estar tokenizado y normalizado
func SearchBM25(
	index textindex.InvertedIndex,
	queryTerms []string,
	k1, b float64,
) ([]BM25Result, error) {
	if index == nil {
		return nil, fmt.Errorf("BM25: el índice es nil")
	}

	if math.IsNaN(k1) || math.IsInf(k1, 0) ||
		math.IsNaN(b) || math.IsInf(b, 0) ||
		k1 < 0 || b < 0 || b > 1 {
		return nil, fmt.Errorf("BM25: parámetros k1 o b inválidos")
	}

	nDocs := index.NumDocs()
	avgdl := index.AvgDocLen()

	if nDocs == 0 || avgdl <= 0 {
		return []BM25Result{}, nil
	}

	// Evitamos puntuar dos veces un término repetido
	seen := make(map[string]bool)
	terms := make([]string, 0, len(queryTerms))

	for _, term := range queryTerms {
		if term == "" || seen[term] {
			continue
		}

		seen[term] = true
		terms = append(terms, term)
	}

	// Guardamos la puntuación acumulada de cada documento.
	scores := make(map[uint32]float64)

	for _, term := range terms {
		postings, err := index.Postings(term)
		if err != nil {
			return nil, err
		}

		if len(postings) == 0 {
			continue
		}

		df := index.DF(term)

		if df <= 0 || df > nDocs {
			return nil, fmt.Errorf(
				"BM25: frecuencia documental inválida para %q",
				term,
			)
		}

		for _, posting := range postings {
			if int(posting.DocID) >= nDocs {
				return nil, fmt.Errorf(
					"BM25: DocID %d fuera del índice",
					posting.DocID,
				)
			}

			docLen := index.DocLen(posting.DocID)

			termScore := bm25TermScore(
				int(posting.TF),
				docLen,
				nDocs,
				df,
				avgdl,
				k1,
				b,
			)

			scores[posting.DocID] += termScore
		}
	}

	// lista de resultados.
	results := make([]BM25Result, 0, len(scores))

	for docID, score := range scores {
		if score > 0 {
			results = append(results, BM25Result{
				DocID: docID,
				Score: score,
			})
		}
	}

	// Ordenar mayor a menor
	// Si hay empate, menor ID primero para hacerlo determinista.
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].DocID < results[j].DocID
		}

		return results[i].Score > results[j].Score
	})

	return results, nil
}