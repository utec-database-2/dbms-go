package textsearch

import "math"

// bm25TermScore: calcula la relevancia de un término en un documento usando BM25.
// tf: frecuencia del término en el documento
// docLen: cantidad de palabras del documento
// nDocs: total de documentos de la coleccion
// df: documentos que contienen el término
// avgdl: longitud media de los documentos

func bm25TermScore(
	tf, docLen, nDocs, df int,
    avgdl, k1, b float64,
) float64 {
	if tf <= 0 || nDocs <= 0 || df <= 0 || df > nDocs {
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

	return idf * (freq * (k1 + 1))/ (freq + k1*lengthNorm)
}
