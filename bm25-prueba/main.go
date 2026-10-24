package main

import "fmt"

func main() {
	docs := []Document{
		{
			ID:   1,
			Text: "vector search vector databases vector vector vector",
		},
		{
			ID:   2,
			Text: "vector search databases",
		},
		{
			ID:   3,
			Text: "relational database systems",
		},
	}

	query := "vector"

	results := BM25Search(docs, query, 1.2, 0.75)

	fmt.Println("Consulta:", query)
	fmt.Println("Resultados ordenados por BM25:")

	for position, result := range results {
		fmt.Printf(
			"%d. Documento %d | Score: %.4f\n",
			position+1,
			result.DocID,
			result.Score,
		)
	}
}