package textindex

import "math"

// TFIDFWeight - ponderacion - (ln):
// (1 + ln(tf)) * ln(N/df), para tf, df y N positivos.
//
// (1 + math.Log10(float64(tf))) * math.Log10(float64(numDocs)/float64(df))
func TFIDFWeight(tf uint32, df, numDocs int) float64 {
	if tf == 0 || df <= 0 || numDocs <= 0 {
		return 0
	}
	return (1 + math.Log(float64(tf))) * math.Log(float64(numDocs)/float64(df))
}
