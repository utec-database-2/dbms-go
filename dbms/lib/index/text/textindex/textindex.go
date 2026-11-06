// Package textindex define el contrato del índice invertido
// el índice SPIMI lo implementa, el ranking (TF-IDF y BM25) lo consume
// y la extensión SQL lo usa a través de él.
//
// Los docID son enteros consecutivos desde 0. El mapa docID -> RID de la
// tabla no vive acá: lo mantiene quien integra el índice con el motor SQL.
package textindex

// Posting indica que el documento DocID contiene el término TF veces.
type Posting struct {
	DocID uint32
	TF    uint32
}

// InvertedIndex es la vista de solo lectura que necesita el ranking.
type InvertedIndex interface {
	// Postings devuelve la lista de posteos del término, ordenada de forma
	// estrictamente creciente por DocID. Un término desconocido devuelve
	// (nil, nil).
	Postings(term string) ([]Posting, error)
	// DF es la cantidad de documentos que contienen el término (0 si no existe).
	DF(term string) int
	// DocLen es la cantidad de tokens del documento (0 si el docID no existe).
	DocLen(doc uint32) int
	NumDocs() int
	// AvgDocLen es el largo promedio de documento (avgdl de BM25).
	AvgDocLen() float64
}
