package main

import (
	"math"
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

