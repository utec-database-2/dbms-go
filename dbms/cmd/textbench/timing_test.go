package main

import (
	"encoding/csv"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/dbms-go/v2/dbms/lib/index/text/ranking"
)

func TestTextbenchBatchCallsAndLastResult(t *testing.T) {
	calls := 0
	call := func() ([]ranking.SearchResult, error) {
		calls++
		return []ranking.SearchResult{{DocID: uint32(calls), Score: 1}}, nil
	}
	hits, elapsed, err := runSearchBatch(call, 7)
	if err != nil || calls != 7 || len(hits) != 1 || hits[0].DocID != 7 || elapsed < 0 {
		t.Fatalf("batch: calls=%d hits=%v elapsed=%v err=%v", calls, hits, elapsed, err)
	}
	if _, _, err := runSearchBatch(call, 0); err == nil {
		t.Fatal("debe rechazar lote vacío")
	}
	if _, _, err := runSearchBatch(nil, 2); err == nil {
		t.Fatal("debe rechazar función nil")
	}
}

func TestTextbenchBatchPropagatesError(t *testing.T) {
	expected := errors.New("falló el índice")
	calls := 0
	call := func() ([]ranking.SearchResult, error) {
		calls++
		if calls == 3 {
			return nil, expected
		}
		return nil, nil
	}
	if _, _, err := runSearchBatch(call, 10); !errors.Is(err, expected) || calls != 3 {
		t.Fatalf("error de lote no propagado: calls=%d err=%v", calls, err)
	}
}

func TestTextbenchBatchCalibrationAndPrecision(t *testing.T) {
	calls := 0
	call := func() ([]ranking.SearchResult, error) {
		calls++
		return []ranking.SearchResult{{DocID: 0, Score: 1}}, nil
	}
	n, err := calibrateBatch(call, 2*time.Millisecond, 65536)
	if err != nil || n < 1 || n > 65536 || calls < n {
		t.Fatalf("calibración: n=%d calls=%d err=%v", n, calls, err)
	}
	_, elapsed, err := runSearchBatch(call, n)
	if err != nil || elapsed <= 0 {
		t.Fatalf("lote medido: %v %v", elapsed, err)
	}
	perQueryMS := float64(elapsed.Nanoseconds()) / float64(n) / 1e6
	if perQueryMS <= 0 || math.IsInf(perQueryMS, 0) || math.IsNaN(perQueryMS) {
		t.Fatalf("ms por consulta inválidos: %v", perQueryMS)
	}
	if _, err := strconv.ParseFloat(strconv.FormatFloat(perQueryMS, 'f', 9, 64), 64); err != nil {
		t.Fatal(err)
	}
	if _, err := calibrateBatch(call, 0, 100); err == nil {
		t.Fatal("debe rechazar target cero")
	}
	if _, err := calibrateBatch(call, time.Millisecond, 0); err == nil {
		t.Fatal("debe rechazar maxBatch cero")
	}
}

func TestTextbenchSmokeBatchMetadata(t *testing.T) {
	cfg := config{
		documents: filepath.Join("testdata", "docs.jsonl"),
		queries:   filepath.Join("testdata", "queries.json"),
		qrels:     filepath.Join("testdata", "qrels.json"),
		sizes:     "12", topK: 10, repeats: 3, warmup: 1,
		minSampleMS: 2, maxBatch: 65536,
		k1: 1.2, b: 0.75, output: filepath.Join(t.TempDir(), "latencias.csv"),
	}
	if err := run(cfg); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(cfg.output)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 7 {
		t.Fatalf("esperadas 7 filas CSV, hay %d", len(rows))
	}
	names := map[string]int{}
	for i, name := range rows[0] {
		names[name] = i
	}
	for _, field := range []string{"batch_size", "target_sample_ms", "query_median_us", "query_p95_us", "batch_durations_ms"} {
		if _, ok := names[field]; !ok {
			t.Fatalf("campo faltante %s", field)
		}
	}
	for _, row := range rows[1:] {
		size, err := strconv.Atoi(row[names["batch_size"]])
		if err != nil || size < 1 {
			t.Fatalf("lote inválido: %v (%v)", row, err)
		}
		us, err := strconv.ParseFloat(row[names["query_median_us"]], 64)
		if err != nil || us <= 0 {
			t.Fatalf("latencia en µs inválida: %v (%v)", row, err)
		}
		if row[names["batch_durations_ms"]] == "[]" || len(row[names["batch_durations_ms"]]) < 3 {
			t.Fatalf("faltan duraciones crudas de los lotes: %v", row)
		}
	}
}
