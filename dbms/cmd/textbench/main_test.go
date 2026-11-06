package main

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTextbenchSizesAndBuckets(t *testing.T) {
	got, err := parseSizes("12,1,100")
	if err != nil || len(got) != 3 || got[0] != 1 || got[1] != 12 || got[2] != 100 {
		t.Fatalf("sizes=%v, %v", got, err)
	}
	for _, bad := range []string{"", "1,1", "0", "3,-5", "abc", "1,"} {
		if _, err := parseSizes(bad); err == nil {
			t.Fatalf("se aceptó sizes=%q", bad)
		}
	}
	if queryBucket(1) != "1" || queryBucket(3) != "3" || queryBucket(5) != "5+" || queryBucket(2) != "other" {
		t.Fatal("buckets incorrectos")
	}
}

func TestTextbenchPrecisionRecallSubset(t *testing.T) {
	known := map[string]bool{"d0": true, "d1": true, "d2": true}
	rel := map[string]bool{"d0": true, "d2": true, "d99": true}
	n, p, r := metrics([]string{"d0", "d1"}, known, rel, 10)
	if n != 2 || p != "0.10000000" || r != "0.50000000" {
		t.Fatalf("got %d,%s,%s", n, p, r)
	}
	n, p, r = metrics([]string{"d0"}, known, map[string]bool{"d99": true}, 10)
	if n != 0 || p != "" || r != "" {
		t.Fatal("sin relevantes en prefijo debe ser N/A")
	}
	n, p, r = metrics(nil, known, nil, 10)
	if n != -1 || p != "" || r != "" {
		t.Fatal("sin qrels deben omitirse métricas")
	}
}

func TestTextbenchGoSmoke(t *testing.T) {
	td := t.TempDir()
	cfg := config{
		documents: filepath.Join("testdata", "docs.jsonl"),
		queries:   filepath.Join("testdata", "queries.json"),
		qrels:     filepath.Join("testdata", "qrels.json"),
		sizes:     "12", topK: 10, repeats: 3, warmup: 1,
		k1: 1.2, b: 0.75, output: filepath.Join(td, "summary.csv"),
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
		t.Fatalf("esperadas cabecera + 6 filas, hay %d", len(rows))
	}
	for _, row := range rows[1:] {
		if row[0] != "12" || row[1] == "" || (row[2] != "tfidf" && row[2] != "bm25") || row[22] == "" || row[23] == "" {
			t.Fatalf("fila incompleta: %v", row)
		}
		if !strings.HasPrefix(row[24], "[") {
			t.Fatalf("faltan muestras: %v", row)
		}
	}
	if err := run(cfg); err == nil {
		t.Fatal("no se debe sobrescribir CSV existente")
	}
}
