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
		expected string
	}{
		// "cómo" y "estás" suelen ser stopwords. Queda "hola" -> raíz: "hol"
		{"Hola, ¿cómo estás?", "hol"},

		// "rápido" -> "rapid" (con removeAccents al final se vuelve "rapid")
		// "marrón" -> "marron"
		// "salta" -> "salt"
		// "perezoso" -> "perezos"
		{"El rápido zorro marrón salta sobre el perro perezoso.", "rap zorr marron salt perr perez"},

		// "bienvenido" -> "bienvenid"
		// "mundo" -> "mund"
		// "programación" -> "program"
		{"¡Bienvenido al mundo de la programación!", "bienven mund program"},

		// "niña" -> "nin" (si quitaste la ñ) o "niñ" -> "nin"
		// "jugaba" -> "jug"
		// "muñeca" -> "munec"
		// "jardín" -> "jardin"
		{"La niña jugaba con su muñeca en el jardín.", "nin jug munec jardin"},
	}

	for _, test := range tests {
		result := PreprocessText(test.input)
		if result != test.expected {
			t.Errorf("PreprocessText(%q) = %q; want %q", test.input, result, test.expected)
		}
	}
}
