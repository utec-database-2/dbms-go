package text

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/kljensen/snowball"
)

var stopWords map[string]bool

func init() {
	// Load stop words from stoplist.txt
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		stopWords = make(map[string]bool)
		return
	}
	stopWords = loadStopWords(filepath.Join(filepath.Dir(sourceFile), "stoplist.txt"))
}

func punctuationRemover(text string) string {
	// Remove punctuation from the text
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(".,;:¡!¿?\"'()[]{}--_/", r) {
			return -1
		}
		return r
	}, text)
}

func word_tokenize(text string) []string {
	// Split the text into words based on whitespace
	return strings.Fields(text)
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
		'ñ': 'n', 'Ñ': 'N', 'ü': 'u', 'Ü': 'U',
	}
	text = strings.Map(func(r rune) rune {
		if replacement, ok := accentMap[r]; ok {
			return replacement
		}
		return r
	}, text)
	return text
}

func removeStopWords(tokens []string) []string {
	// Remove stop words from the list of tokens
	filtered := []string{}
	for _, token := range tokens {
		if !stopWords[token] {
			filtered = append(filtered, token)
		}
	}
	return filtered
}

func stemTokens(tokens []string) ([]string, error) {
	stemmed := []string{}
	for _, token := range tokens {
		root, err := snowball.Stem(token, "spanish", true)
		if err != nil {
			return nil, err
		}
		stemmed = append(stemmed, root)
	}
	return stemmed, nil
}

// PreprocessText normaliza un texto de entrada para que sea más fácil de indexar y buscar.
func PreprocessText(text string) []string {
	// lower case
	text = strings.ToLower(text)
	// remove punctuation
	text = punctuationRemover(text)
	// tokenizacion
	tokens := word_tokenize(text)
	// remove stop words using stoplist.txt
	tokens = removeStopWords(tokens)
	// stem words
	tokens, err := stemTokens(tokens)
	if err != nil {
		// Handle the error, e.g., log it or return an error
		fmt.Printf("Error stemming tokens: %v\n", err)
	}
	// remove accents (El stemmer de Snowball necesita los acentos para funcionar correctamente, pero después de hacer el stemming, podemos eliminarlos)
	for i, token := range tokens {
		tokens[i] = removeAccents(token)
	}

	return tokens
}
