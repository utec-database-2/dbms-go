package main

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	m := manifest{SchemaVersion: "pgtextprep-native-v2", Documents: 12, Queries: 3, CorpusSHA256: strings.Repeat("a", 64), PostgreSQLTextConfig: "spanish"}
	content, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "manifest.json"), content, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "queries.csv"))
	if err != nil {
		t.Fatal(err)
	}
	w := csv.NewWriter(f)
	for _, r := range [][]string{
		{"query_id", "raw_text", "raw_words"},
		{"q1", "gato", "1"},
		{"q3", "gato casa perro", "3"},
		{"q5", "un perro corre por la casa grande", "7"},
	} {
		if err = w.Write(r); err != nil {
			t.Fatal(err)
		}
	}
	w.Flush()
	if err = w.Error(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}
func TestPGTextbenchManifestQueriesAndBuckets(t *testing.T) {
	dir := fixture(t)
	m, err := readManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	qs, err := readQueries(dir, m.Queries)
	if err != nil {
		t.Fatal(err)
	}
	if m.Documents != 12 || len(qs) != 3 || qs[1].Expression != "gato OR casa OR perro" || qs[2].RawWords != 7 {
		t.Fatalf("manifest/queries: %+v %+v", m, qs)
	}
	got := []string{queryBucket(qs[0].RawWords), queryBucket(qs[1].RawWords), queryBucket(qs[2].RawWords)}
	if !reflect.DeepEqual(got, []string{"1", "3", "5+"}) {
		t.Fatalf("categorías incorrectas: %v", got)
	}
	if _, err := readQueries(dir, 4); err == nil {
		t.Fatal("debe rechazar cantidad distinta a manifest")
	}
}
func TestPGTextbenchMetrics(t *testing.T) {
	known := map[string]bool{"d0": true, "d1": true, "d2": true}
	rel := map[string]bool{"d0": true, "d1": true, "d3": true}
	count, p, r := metrics([]string{"d1", "d2"}, known, rel, 10)
	if count != 2 || p != "0.10000000" || r != "0.50000000" {
		t.Fatalf("métricas: %d %s %s", count, p, r)
	}
	_, p, r = metrics([]string{"d1"}, known, nil, 10)
	if p != "" || r != "" {
		t.Fatal("sin qrels debe dar vacío")
	}
}
func TestPGTextbenchPlanGINSemantics(t *testing.T) {
	samples := []struct {
		in   string
		used bool
		has  string
	}{
		{`[{"Plan":{"Node Type":"Limit","Plans":[{"Node Type":"Sort","Plans":[{"Node Type":"Seq Scan"}]}]}}]`, false, "Seq Scan"},
		{`[{"Plan":{"Node Type":"Limit","Plans":[{"Node Type":"Bitmap Heap Scan","Plans":[{"Node Type":"Bitmap Index Scan","Index Name":"documents_search_vector_gin_idx"}]}]}}]`, true, "Bitmap Index Scan"},
	}
	for _, v := range samples {
		plan, used, err := parsePlan([]byte(v.in))
		if err != nil {
			t.Fatal(err)
		}
		if used != v.used || !strings.Contains(plan, v.has) {
			t.Fatalf("plan %q, gin=%t", plan, used)
		}
	}
	if _, _, err := parsePlan([]byte(`{}`)); err == nil {
		t.Fatal("plan inválido debe rechazarse")
	}
}
func TestPGTextbenchBatchPrecisionAndError(t *testing.T) {
	calls := 0
	call := func() ([]hit, error) { calls++; return []hit{{ID: 3, Score: 0.25}}, nil }
	out, elapsed, err := batch(call, 5)
	if err != nil || calls != 5 || len(out) != 1 || out[0].ID != 3 {
		t.Fatalf("lote %+v %v %d %s", out, err, calls, elapsed)
	}
	delayedCalls := 0
	delayed := func() ([]hit, error) {
		delayedCalls++
		time.Sleep(5 * time.Millisecond)
		return []hit{{ID: 3, Score: 0.25}}, nil
	}
	out, elapsed, err = batch(delayed, 2)
	if err != nil || delayedCalls != 2 || len(out) != 1 || out[0].ID != 3 || elapsed <= 0 {
		t.Fatalf("lote medible %+v %v llamadas=%d duracion=%s", out, err, delayedCalls, elapsed)
	}
	n, err := calibrate(delayed, time.Millisecond, 16)
	if err != nil || n < 1 || n > 16 {
		t.Fatalf("calibración n=%d %v", n, err)
	}
	sentinel := errors.New("lectura SQL")
	if _, _, err = batch(func() ([]hit, error) { return nil, sentinel }, 2); !errors.Is(err, sentinel) {
		t.Fatal("no propagó error")
	}
}
func TestPGTextbenchCSVHeader(t *testing.T) {
	if len(header) != 46 {
		t.Fatalf("cabecera inesperada %d", len(header))
	}
	required := map[string]bool{"n_documents": true, "corpus_sha256": true, "method": true, "query_id": true, "query_median_us": true, "postgres_gin_bytes": true, "gin_used": true}
	for _, v := range header {
		delete(required, v)
	}
	if len(required) != 0 {
		t.Fatalf("faltan columnas: %v", required)
	}
}

// La consulta debe recibir texto original. PostgreSQL hace stemming nativo.
func TestPGTextbenchNativeSQLAndCorpus(t *testing.T) {
	if !strings.Contains(querySQL, "websearch_to_tsquery('spanish'") ||
		strings.Contains(querySQL, "to_tsquery('simple'") ||
		!strings.Contains(querySQL, "search_vector @@") {
		t.Fatalf("SQL no utiliza el analizador spanish nativo: %s", querySQL)
	}
	dir := fixture(t)
	qs, err := readQueries(dir, 3)
	if err != nil {
		t.Fatal(err)
	}
	if qs[0].Expression != "gato" || qs[1].Expression != "gato OR casa OR perro" || qs[2].RawWords != 7 {
		t.Fatalf("expresiones originales: %+v", qs)
	}
	if !strings.Contains(strings.Join(header, "|"), "postgres_fts_config") {
		t.Fatal("falta configuración FTS")
	}
}
