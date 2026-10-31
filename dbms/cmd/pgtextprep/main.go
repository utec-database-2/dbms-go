// pgtextprep prepara documentos y consultas para PostgreSQL GIN con el MISMO
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode"

	text "github.com/dbms-go/v2/dbms/lib/text"
)

type options struct {
	documents string
	queries   string
	outputDir string
	maxDocs   int
}

type manifest struct {
	SchemaVersion        string `json:"schema_version"`
	Documents            int    `json:"documents"`
	Queries              int    `json:"queries"`
	CorpusSHA256         string `json:"corpus_sha256"`
	GoVersion            string `json:"go_version"`
	TextPreprocessor     string `json:"text_preprocessor"`
	PostgreSQLTextConfig string `json:"postgresql_text_config"`
	SearchSemantics      string `json:"search_semantics"`
}

func main() {
	var opts options
	flag.StringVar(&opts.documents, "documents", "", "archivo JSONL usado por textbench")
	flag.StringVar(&opts.queries, "queries", "", "archivo JSON [{id,text}] usado por textbench")
	flag.StringVar(&opts.outputDir, "output-dir", "", "carpeta de salida NUEVA")
	flag.IntVar(&opts.maxDocs, "max-docs", 0, "prefijo de N documentos; 0 = todo el corpus")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "pgtextprep: argumentos inesperados")
		os.Exit(2)
	}
	if err := run(opts); err != nil {
		fmt.Fprintln(os.Stderr, "pgtextprep:", err)
		os.Exit(1)
	}
}

func run(opts options) error {
	if opts.documents == "" || opts.queries == "" || opts.outputDir == "" || opts.maxDocs < 0 {
		return errors.New("requiere --documents, --queries, --output-dir y --max-docs >= 0")
	}
	if _, err := os.Stat(opts.outputDir); err == nil {
		return fmt.Errorf("output-dir ya existe: %s", opts.outputDir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(opts.outputDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, ".pgtextprep-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	n, digest, err := writeDocuments(opts.documents, filepath.Join(tmp, "docs.csv"), opts.maxDocs)
	if err != nil {
		return err
	}
	queries, err := writeQueries(opts.queries, filepath.Join(tmp, "queries.csv"))
	if err != nil {
		return err
	}
	m := manifest{SchemaVersion: "pgtextprep-v1", Documents: n, Queries: queries,
		CorpusSHA256: digest, GoVersion: runtime.Version(),
		TextPreprocessor:     "github.com/dbms-go/v2/dbms/lib/text.PreprocessText",
		PostgreSQLTextConfig: "simple", SearchSemantics: "OR (union of term postings)"}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err = os.WriteFile(filepath.Join(tmp, "manifest.json"), raw, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, opts.outputDir); err != nil {
		return err
	}
	fmt.Printf("pgtextprep: %d documentos; %d consultas; archivos en %s\n", n, queries, opts.outputDir)
	return nil
}

func docID(raw json.RawMessage, fallback int) (string, error) {
	if len(raw) == 0 {
		return strconv.Itoa(fallback), nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return "", errors.New("id vacío")
		}
		return s, nil
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String(), nil
	}
	return "", errors.New("id debe ser string o número")
}

// PostgreSQL 'simple' debe reconocer cada token Go sin volver a dividirlo.
// Rechazamos caracteres ambiguos en lugar de comparar vocabularios distintos.
// Los documentos de texto normalizados preservan repetición y orden.
func validateToken(token string) error {
	if token == "" || len(token) > 2000 {
		return fmt.Errorf("token vacío o demasiado largo: %q", token)
	}
	for _, r := range token {
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) {
			return fmt.Errorf("token no compatible con configuración simple: %q", token)
		}
	}
	return nil
}

func normalize(input string) ([]string, error) {
	toks, err := text.PreprocessText(input)
	if err != nil {
		return nil, err
	}
	for _, tok := range toks {
		if err := validateToken(tok); err != nil {
			return nil, err
		}
	}
	return toks, nil
}

func newCSV(path string) (*os.File, *csv.Writer, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, nil, err
	}
	return f, csv.NewWriter(f), nil
}

func closeCSV(f *os.File, w *csv.Writer) error {
	w.Flush()
	if err := w.Error(); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func writeDocuments(src, dst string, limit int) (int, string, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, "", err
	}
	defer in.Close()
	f, w, err := newCSV(dst)
	if err != nil {
		return 0, "", err
	}
	if err := w.Write([]string{"doc_id", "external_id", "normalized_text"}); err != nil {
		f.Close()
		return 0, "", err
	}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	seen := make(map[string]bool)
	h := sha256.New()
	count, line := 0, 0
	for (limit == 0 || count < limit) && scanner.Scan() {
		line++
		raw := scanner.Bytes()
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue
		}
		var row struct {
			ID   json.RawMessage `json:"id"`
			Text *string         `json:"text"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			f.Close()
			return 0, "", fmt.Errorf("línea %d: %w", line, err)
		}
		if row.Text == nil {
			f.Close()
			return 0, "", fmt.Errorf("línea %d: falta text string", line)
		}
		id, err := docID(row.ID, count)
		if err != nil {
			f.Close()
			return 0, "", fmt.Errorf("línea %d: %w", line, err)
		}
		if seen[id] {
			f.Close()
			return 0, "", fmt.Errorf("id duplicado: %q", id)
		}
		toks, err := normalize(*row.Text)
		if err != nil {
			f.Close()
			return 0, "", fmt.Errorf("documento %q: %w", id, err)
		}
		if err := w.Write([]string{strconv.Itoa(count), id, strings.Join(toks, " ")}); err != nil {
			f.Close()
			return 0, "", err
		}
		seen[id] = true
		count++
		_, _ = h.Write(raw)
		_, _ = h.Write([]byte{'\n'})
	}
	if err := scanner.Err(); err != nil {
		f.Close()
		return 0, "", err
	}
	if limit > 0 && count != limit {
		f.Close()
		return 0, "", fmt.Errorf("solo %d documentos; solicitados %d", count, limit)
	}
	if err := closeCSV(f, w); err != nil {
		return 0, "", err
	}
	return count, hex.EncodeToString(h.Sum(nil)), nil
}

func queryExpression(tokens []string) string {
	seen := make(map[string]bool)
	unique := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if !seen[t] {
			unique = append(unique, "'"+t+"'")
			seen[t] = true
		}
	}
	sort.Strings(unique)
	return strings.Join(unique, " | ")
}

func writeQueries(src, dst string) (int, error) {
	raw, err := os.ReadFile(src)
	if err != nil {
		return 0, err
	}
	var qs []struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &qs); err != nil {
		return 0, err
	}
	if len(qs) == 0 {
		return 0, errors.New("queries vacío")
	}
	f, w, err := newCSV(dst)
	if err != nil {
		return 0, err
	}
	if err := w.Write([]string{"query_id", "raw_text", "normalized_tokens", "or_tsquery", "raw_words"}); err != nil {
		f.Close()
		return 0, err
	}
	seen := make(map[string]bool)
	for _, q := range qs {
		if q.ID == "" || strings.TrimSpace(q.Text) == "" || seen[q.ID] {
			f.Close()
			return 0, fmt.Errorf("query_id duplicado/vacío o texto vacío: %q", q.ID)
		}
		toks, err := normalize(q.Text)
		if err != nil {
			f.Close()
			return 0, fmt.Errorf("consulta %q: %w", q.ID, err)
		}
		if err := w.Write([]string{q.ID, q.Text, strings.Join(toks, " "), queryExpression(toks), strconv.Itoa(len(strings.Fields(q.Text)))}); err != nil {
			f.Close()
			return 0, err
		}
		seen[q.ID] = true
	}
	if err := closeCSV(f, w); err != nil {
		return 0, err
	}
	return len(qs), nil
}
