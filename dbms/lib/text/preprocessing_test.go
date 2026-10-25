package text

import (
	"testing"
)

func testExistanceOfStopWords() bool {
	// Check if stopWords map is initialized and contains some common stop words
	if stopWords == nil {
		return false
	}
	commonStopWords := []string{"y", "de", "la", "el", "en"}
	for _, word := range commonStopWords {
		if !stopWords[word] {
			return false
		}
	}
	return true
}

func TestLoadStopWords(t *testing.T) {
	if !testExistanceOfStopWords() {
		t.Errorf("Stop words not loaded correctly from stoplist.txt")
	}
}

func TestPreprocessText(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{

		// "Bienvenido" -> raíz: "bienven"
		// "al" y "mundo" suelen ser stopwords. Queda "Bienvenido" -> raíz: "bienven"
		{"Bienvenido al mundo de la programación.", []string{"bienven", "mund", "program"}},

		// "rápido" -> "rapid" (con removeAccents al final se vuelve "rapid")
		// "marrón" -> "marron"
		// "salta" -> "salt"
		// "perezoso" -> "perezos"
		{"El rápido zorro marrón salta sobre el perro perezoso.", []string{"rap", "zorr", "marron", "salt", "perr", "perez"}},
		{"El gato y la casa de Juan", []string{"gat", "cas", "juan"}},    // Stopwords con palabras con acentos
		{"¡Hola!, ¿Cómo estás?", []string{"hol"}},                        // Stopwords con signos de puntuación
		{"niñez canción árboles", []string{"ninez", "cancion", "arbol"}}, // Palabras con acentos y ñ
		{" ", []string{}},            // Cadena vacía
		{"", []string{}},             // Cadena vacía
		{"12345", []string{"12345"}}, // Solo números
		{"programando programas programación", []string{"program", "program", "program"}}, // Palabras con la misma raíz
	}

	for _, test := range tests {
		result := PreprocessText(test.input)
		if !equal(result, test.expected) {
			t.Errorf("PreprocessText(%q) = %q; want %q", test.input, result, test.expected)
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i, v := range a {
		if v != b[i] {
			return false
		}
	}
	return true
}
