package text

import (
	"testing"
)

func testExistenceOfStopWords() bool {
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
	if !testExistenceOfStopWords() {
		t.Errorf("Stop words not loaded correctly from stoplist.txt")
	}
	text := "el gato y la casa"
	got, _ := PreprocessText(text)
	expected := []string{"gat", "cas"}
	if !equal(got, expected) {
		t.Errorf("PreprocessText(%q) = %q; want %q", text, got, expected)
	}
}

func TestPreprocessText(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{

		// "Bienvenido" -> raíz: "bienven"
		// "al", "de", "la" suelen ser stopwords. Queda "Bienvenido" -> raíz: "bienven", "mundo" -> raíz: "mund", "programación" -> raíz: "program"
		{"Bienvenido al mundo de la programación.", []string{"bienven", "mund", "program"}},

		// "rápido" -> "rapid" (con removeAccents al final se vuelve "rapid")
		// "marrón" -> "marron"
		// "salta" -> "salt"
		// "perezoso" -> "perezos"
		{"El rápido zorro marrón salta sobre el perro perezoso.", []string{"rap", "zorr", "marron", "salt", "perr", "perez"}},
		{"El gato y la casa de Juan", []string{"gat", "cas", "juan"}},      // Stopwords simples
		{"¡Hola!, ¿Cómo estás?", []string{"hol"}},                          // Stopwords con signos de puntuación
		{"niñez canción árboles", []string{"ninez", "cancion", "arbol"}},   // Palabras con acentos y ñ
		{"acción_pre-ütil/niño", []string{"accion", "pre", "util", "nin"}}, // Palabras con guiones, barras y caracteres especiales
		{" ", []string{}},            // Cadena vacía
		{"", []string{}},             // Cadena vacía
		{"12345", []string{"12345"}}, // Solo números
		{"programando programas programación", []string{"program", "program", "program"}}, // Palabras con la misma raíz
	}

	for _, test := range tests {
		result, _ := PreprocessText(test.input)
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
