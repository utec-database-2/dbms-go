package main

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// Document representa un documento de prueba.
type Document struct {
	ID   int
	Text string
}

// SearchResult contiene un documento y su puntuación BM25.
type SearchResult struct {
	DocID int
	Score float64
}

// bm25TermScore calcula la contribución de un término
// a la puntuación BM25 de un documento.
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

	return idf * (freq * (k1 + 1)) /
		(freq + k1*lengthNorm)
}

// tokenize separa el texto en palabras y pasa todo a minúsculas.
func tokenize(text string) []string {
	return strings.FieldsFunc(
		strings.ToLower(text),
		func(r rune) bool {
			return !unicode.IsLetter(r) &&
				!unicode.IsDigit(r)
		},
	)
}

// documentStats guarda las estadísticas de un documento.
type documentStats struct {
	doc    Document
	length int
	tf     map[string]int
}

// BM25Search busca documentos y los ordena por relevancia.

func BM25Search(
	docs []Document,
	query string,
	k1, b float64,
)[]SearchResult {
	if len(docs) == 0 || k1 < 0 || b < 0 || b > 1 {
		return nil
	}

	queryTokens := tokenize(query)
	if len(queryTokens) == 0 {
		return nil;
	}

	// Evitar contar 2 veces
	seen := make(map[string]bool)
	var terms []string

	for _, term := range queryTokens{
		if !seen[term]{
			seen[term]=true
			terms = append(terms,term)
		}
	}

	// Calcula la frecuencia de cada término en cada documento
	// y la frecuencia documental de cada término.
	stats := make([]documentStats, 0, len(docs))
	df := make(map[string]int)
	totalLength := 0

	for _, doc := range docs {
		tokens := tokenize(doc.Text)
		tf := make(map[string]int)

		for _, token := range tokens {
			tf[token]++
		}

		for term := range tf {
			df[term]++
		}

		totalLength += len(tokens)

		stats = append(stats, documentStats{
			doc:    doc,
			length: len(tokens),
			tf:     tf,
		})
	}

	nDocs := len(docs)
	avgdl := float64(totalLength) / float64(nDocs)

	var results []SearchResult

	for _,stat := range stats{
		var score float64

		for _, term := range terms{
			freq := stat.tf[term]
			if freq == 0{
				continue
			}

			score += bm25TermScore(
				freq,
				stat.length,
				nDocs,
				df[term],
				avgdl,
				k1,
				b,
			)
		}

		// No devolvemos documentos sin coincidencias.
		if score > 0 {
			results = append(results, SearchResult{
				DocID: stat.doc.ID,
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

	return results
}

