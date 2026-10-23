package text

import (
	"fmt"
	"os"
	"strings"

	"github.com/kljensen/snowball"
)

var stopWords map[string]bool

func init() {
	// Load stop words from stoplist.txt
	stopWords = loadStopWords("stoplist.txt")
}

func loadStopWords(filename string) map[string]bool {
	// Load stop words from a file and return them as a map for quick lookup
	sw := make(map[string]bool)
	data, err := os.ReadFile(filename)
	if err != nil {
		fmt.Printf("Error reading stop words file: %v\n", err)
		return sw
	}
	for _, word := range strings.Fields(string(data)) {
		sw[word] = true
	}
	return sw
}

func removeAccents(text string) string {
	// Implement a function to remove accents from characters
	accentMap := map[rune]rune{
		'á': 'a', 'é': 'e', 'í': 'i', 'ó': 'o', 'ú': 'u',
		'Á': 'A', 'É': 'E', 'Í': 'I', 'Ó': 'O', 'Ú': 'U',
		'ñ': 'n', 'Ñ': 'N',
	}
	text = strings.Map(func(r rune) rune {
		if replacement, ok := accentMap[r]; ok {
			return replacement
		}
		return r
	}, text)
	return text
}

func removeStopWords(text string) string {
	// Load stop words from stoplist.txt
	words := strings.Fields(text)
	var filteredWords []string
	for _, word := range words {
		if !stopWords[word] {
			filteredWords = append(filteredWords, word)
		}
	}
	return strings.Join(filteredWords, " ")
}

func stemText(text string) (string, error) {
	// Implement a simple stemming algorithm or use an existing library
	// For demonstration, we'll just return the text as is
	var result strings.Builder
	words := strings.Fields(text)
	for i, word := range words {
		root, err := snowball.Stem(word, "spanish", true)
		if err != nil {
			return "", err
		}
		result.WriteString(root)
		if i < len(words)-1 {
			result.WriteString(" ")
		}
	}
	return result.String(), nil
}

// PreprocessText normaliza un texto de entrada para que sea más fácil de indexar y buscar.
func PreprocessText(text string) string {
	// lower case
	text = strings.ToLower(text)
	// remove punctuation
	text = strings.Map(func(r rune) rune {
		if strings.ContainsRune(".,;:¡!¿?\"'()[]{}", r) {
			return -1
		}
		return r
	}, text)
	// remove extra whitespace
	text = strings.TrimSpace(text)
	// remove stop words using stoplist.txt
	text = removeStopWords(text)
	// stem words
	text, err := stemText(text)
	if err != nil {
		// Handle the error, e.g., log it or return an error
		return err.Error()
	}
	// remove accents (El stemmer de Snowball necesita los acentos para funcionar correctamente, pero después de hacer el stemming, podemos eliminarlos)
	text = removeAccents(text)

	return text
}
