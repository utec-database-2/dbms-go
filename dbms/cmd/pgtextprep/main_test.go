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

func TestPGPrepORQueryAndValidation(t *testing.T) {
	if got, want := queryExpression([]string{"gat", "gat", "perr"}), "'gat' | 'perr'"; got != want {
		t.Fatalf("OR: %q != %q", got, want)
	}
	if got := queryExpression(nil); got != "" {
		t.Fatalf("vacia: %q", got)
	}
	for _, token := range []string{"gat", "2026", "informacion"} {
		if err := validateToken(token); err != nil {
			t.Fatal(err)
		}
	}
	for _, token := range []string{"a+b", "hola.mundo", "", "a b", "a'b"} {
		if err := validateToken(token); err == nil {
			t.Fatalf("aceptó token %q", token)
		}
	}
}

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
	if dr[1][2] != "gat gat perr" || dr[2][2] != "" || dr[3][2] != "cas" {
		t.Fatalf("tokens Go inesperados: %v", dr)
	}
	qr := readCSV(t, filepath.Join(dest, "queries.csv"))
	if len(qr) != 3 || qr[1][3] != "'gat' | 'perr'" || qr[2][3] != "" {
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
	if manifest.Documents != 3 || manifest.Queries != 2 || len(manifest.CorpusSHA256) != 64 {
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
