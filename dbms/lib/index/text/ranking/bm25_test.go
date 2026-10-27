package ranking

import (
	"math"
	"testing"
)

//comprueba si dos números son aproximadamente iguales.
func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

// 1. Un término raro debe tener mayor puntuación que uno común.
func TestRareTermHasHigherScore(t *testing.T) {
	common := BM25TermScore(
		1, 100, 1000, 500, 100, 1.2, 0.75,
	)

	rare := BM25TermScore(
		1, 100, 1000, 10, 100, 1.2, 0.75,
	)

	if rare <= common {
		t.Fatalf(
			"el término raro debería puntuar más: raro=%.6f, común=%.6f",
			rare, common,
		)
	}
}

// 2. Más apariciones aumentan la puntuación,
// pero las ganancias disminuyen progresivamente.
func TestFrequencySaturates(t *testing.T) {
	score1 := BM25TermScore(
		1, 100, 1000, 10, 100, 1.2, 0.75,
	)

	score2 := BM25TermScore(
		2, 100, 1000, 10, 100, 1.2, 0.75,
	)

	score5 := BM25TermScore(
		5, 100, 1000, 10, 100, 1.2, 0.75,
	)

	score10 := BM25TermScore(
		10, 100, 1000, 10, 100, 1.2, 0.75,
	)

	if score2 <= score1 {
		t.Fatal("dos apariciones deberían puntuar más que una")
	}

	if score5 <= score2 {
		t.Fatal("cinco apariciones deberían puntuar más que dos")
	}

	if score10 <= score5 {
		t.Fatal("diez apariciones deberían puntuar más que cinco")
	}

	gain1To2 := score2 - score1
	gain5To10 := score10 - score5

	if gain5To10 >= gain1To2 {
		t.Fatalf(
			"se esperaba saturación: ganancia de 1 a 2 = %.6f, de 5 a 10 = %.6f",
			gain1To2, gain5To10,
		)
	}
}

// 3. Con b > 0, un documento corto debe obtener
// una mayor contribución que uno largo, si lo demás es igual.
func TestLengthNormalization(t *testing.T) {
	shortDoc := BM25TermScore(
		3, 50, 1000, 10, 100, 1.2, 0.75,
	)

	longDoc := BM25TermScore(
		3, 200, 1000, 10, 100, 1.2, 0.75,
	)

	if shortDoc <= longDoc {
		t.Fatalf(
			"el documento corto debería puntuar más: corto=%.6f, largo=%.6f",
			shortDoc, longDoc,
		)
	}
}

// 4. Si b = 0, la longitud del documento no debe
// afectar la puntuación.
func TestZeroBDisablesLengthNormalization(t *testing.T) {
	shortDoc := BM25TermScore(
		3, 50, 1000, 10, 100, 1.2, 0,
	)

	longDoc := BM25TermScore(
		3, 200, 1000, 10, 100, 1.2, 0,
	)

	if !almostEqual(shortDoc, longDoc) {
		t.Fatalf(
			"con b=0 las puntuaciones deberían coincidir: corto=%.6f, largo=%.6f",
			shortDoc, longDoc,
		)
	}
}

// 5. Las entradas inválidas deben devolver cero.
func TestInvalidInputsReturnZero(t *testing.T) {
	tests := []struct {
		name                  string
		tf, docLen, nDocs, df int
		avgdl, k1, b          float64
	}{
		{
			name: "TF cero",
			tf:   0, docLen: 10, nDocs: 10, df: 2,
			avgdl: 10, k1: 1.2, b: 0.75,
		},
		{
			name: "TF negativo",
			tf:   -1, docLen: 10, nDocs: 10, df: 2,
			avgdl: 10, k1: 1.2, b: 0.75,
		},
		{
			name: "longitud negativa",
			tf:   1, docLen: -1, nDocs: 10, df: 2,
			avgdl: 10, k1: 1.2, b: 0.75,
		},
		{
			name: "total de documentos cero",
			tf:   1, docLen: 10, nDocs: 0, df: 0,
			avgdl: 10, k1: 1.2, b: 0.75,
		},
		{
			name: "frecuencia documental cero",
			tf:   1, docLen: 10, nDocs: 10, df: 0,
			avgdl: 10, k1: 1.2, b: 0.75,
		},
		{
			name: "DF mayor que N",
			tf:   1, docLen: 10, nDocs: 10, df: 11,
			avgdl: 10, k1: 1.2, b: 0.75,
		},
		{
			name: "longitud media cero",
			tf:   1, docLen: 10, nDocs: 10, df: 2,
			avgdl: 0, k1: 1.2, b: 0.75,
		},
		{
			name: "k1 negativo",
			tf:   1, docLen: 10, nDocs: 10, df: 2,
			avgdl: 10, k1: -1, b: 0.75,
		},
		{
			name: "b negativo",
			tf:   1, docLen: 10, nDocs: 10, df: 2,
			avgdl: 10, k1: 1.2, b: -0.1,
		},
		{
			name: "b mayor que uno",
			tf:   1, docLen: 10, nDocs: 10, df: 2,
			avgdl: 10, k1: 1.2, b: 1.1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := BM25TermScore(
				tt.tf,
				tt.docLen,
				tt.nDocs,
				tt.df,
				tt.avgdl,
				tt.k1,
				tt.b,
			)

			if score != 0 {
				t.Fatalf(
					"se esperaba puntuación 0, se obtuvo %.6f",
					score,
				)
			}
		})
	}
}
