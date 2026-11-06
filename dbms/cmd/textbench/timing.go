package main

import (
	"fmt"
	"time"

	"github.com/dbms-go/v2/dbms/lib/index/text/ranking"
)

const (
	defaultMinSampleMS = 5
	defaultMaxBatch    = 65536
)

// searchCall ejecuta UNA búsqueda en el índice ya abierto y con tokens normalizados.
type searchCall func() ([]ranking.SearchResult, error)

// runSearchBatch mide un LOTE de búsquedas. No cronometra cada llamada por
// separado: así se amortiza el coste y la resolución del reloj de Windows.
// La duración total se divide por n para estimar la latencia por consulta.
func runSearchBatch(call searchCall, n int) ([]ranking.SearchResult, time.Duration, error) {
	if n < 1 || call == nil {
		return nil, 0, fmt.Errorf("lote inválido: n=%d", n)
	}
	start := time.Now()
	var last []ranking.SearchResult
	for i := 0; i < n; i++ {
		hits, err := call()
		if err != nil {
			return nil, 0, err
		}
		last = hits
	}
	return last, time.Since(start), nil
}

// calibrateBatch selecciona cuántas búsquedas contiene cada lote de ESTA
// consulta y ESTE método. La calibración se realiza fuera de las muestras.
// Aumenta 1, 2, 4, ... hasta alcanzar el tiempo objetivo o maxBatch.
func calibrateBatch(call searchCall, target time.Duration, maxBatch int) (int, error) {
	if target <= 0 || maxBatch < 1 || call == nil {
		return 0, fmt.Errorf("parámetros de calibración inválidos")
	}
	for size := 1; ; {
		_, elapsed, err := runSearchBatch(call, size)
		if err != nil {
			return 0, err
		}
		if elapsed >= target || size == maxBatch {
			if elapsed <= 0 {
				return 0, fmt.Errorf("no se pudo medir ni un lote de %d búsquedas", size)
			}
			return size, nil
		}
		if size >= maxBatch/2 {
			size = maxBatch
		} else {
			size *= 2
		}
	}
}
