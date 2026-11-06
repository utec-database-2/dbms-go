package main

import (
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func readCSV(t *testing.T, p string) [][]string {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestPGPrepOriginalTextAndUnicode(t *testing.T) {
	root := t.TempDir()
	docs := filepath.Join(root, "docs.jsonl")
	queries := filepath.Join(root, "queries.json")
	original := "mæsəˈtʃusɪts jē₁ran 𐌾𐌴𐍂 – no tokenizar"
	if err := os.WriteFile(docs, []byte(`{"id":"d0","text":`+mustJSON(original)+`}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(queries, []byte(`[{"id":"q1","text":"gato perro"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "export")
	if err := run(options{documents: docs, queries: queries, outputDir: dir, maxDocs: 1}); err != nil {
		t.Fatal(err)
	}
	dr := readCSV(t, filepath.Join(dir, "docs.csv"))
	if len(dr) != 2 || dr[0][2] != "raw_text" || dr[1][2] != original {
		t.Fatalf("texto alterado: %q", dr)
	}
	qr := readCSV(t, filepath.Join(dir, "queries.csv"))
	if len(qr) != 2 || !reflect.DeepEqual(qr[1], []string{"q1", "gato perro", "2"}) {
		t.Fatalf("consulta alterada: %q", qr)
	}
}

func mustJSON(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestPGPrepRealPreprocessingAndIDs(t *testing.T) {
	root := t.TempDir()
	docs := filepath.Join(root, "docs.jsonl")
	qs := filepath.Join(root, "queries.json")
	input := `{"id":"d0","text":"El gato gato y perro"}
{"id":"d1","text":"el y la"}
{"id":"d2","text":"Casa"}
`
	if err := os.WriteFile(docs, []byte(input), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(qs, []byte(`[{"id":"q1","text":"gato gato perro"},{"id":"q2","text":"el y la"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "export")
	if err := run(options{documents: docs, queries: qs, outputDir: dest, maxDocs: 3}); err != nil {
		t.Fatal(err)
	}
	dr := readCSV(t, filepath.Join(dest, "docs.csv"))
	if len(dr) != 4 || !reflect.DeepEqual(dr[1][:2], []string{"0", "d0"}) {
		t.Fatalf("docs: %v", dr)
	}
	if dr[1][2] != "El gato gato y perro" || dr[2][2] != "el y la" || dr[3][2] != "Casa" {
		t.Fatalf("tokens Go inesperados: %v", dr)
	}
	qr := readCSV(t, filepath.Join(dest, "queries.csv"))
	if len(qr) != 3 || qr[1][1] != "gato gato perro" || qr[1][2] != "3" || qr[2][1] != "el y la" {
		t.Fatalf("queries: %v", qr)
	}
	var manifest manifest
	payload, err := os.ReadFile(filepath.Join(dest, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != "pgtextprep-native-v2" || manifest.PostgreSQLTextConfig != "spanish" || manifest.Documents != 3 || manifest.Queries != 2 || len(manifest.CorpusSHA256) != 64 {
		t.Fatalf("manifest: %+v", manifest)
	}
	if err := run(options{documents: docs, queries: qs, outputDir: dest, maxDocs: 3}); err == nil {
		t.Fatal("debe evitar sobrescritura")
	}
}

func TestPGPrepRejectsDuplicateAndShortCorpus(t *testing.T) {
	root := t.TempDir()
	docs := filepath.Join(root, "docs.jsonl")
	qs := filepath.Join(root, "queries.json")
	if err := os.WriteFile(qs, []byte(`[{"id":"q1","text":"gato"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(docs, []byte(`{"id":"x","text":"gato"}`+"\n"+`{"id":"x","text":"perro"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := run(options{documents: docs, queries: qs, outputDir: filepath.Join(root, "duplicate"), maxDocs: 2})
	if err == nil || !strings.Contains(err.Error(), "duplicado") {
		t.Fatalf("duplicado: %v", err)
	}
	if err := os.WriteFile(docs, []byte(`{"id":"x","text":"gato"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = run(options{documents: docs, queries: qs, outputDir: filepath.Join(root, "short"), maxDocs: 2})
	if err == nil || !strings.Contains(err.Error(), "solo 1") {
		t.Fatalf("corto: %v", err)
	}
}
