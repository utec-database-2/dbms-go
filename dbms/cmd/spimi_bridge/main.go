package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dbms-go/v2/dbms/lib/index/text/ranking"
	"github.com/dbms-go/v2/dbms/lib/index/text/spimi"
	text "github.com/dbms-go/v2/dbms/lib/text"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "spimi_bridge:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("uso: spimi_bridge build|export|preprocess|search [opciones]")
	}
	switch args[0] {
	case "build":
		fs := flag.NewFlagSet("build", flag.ContinueOnError)
		input := fs.String("input", "", "JSONL con un campo text por documento")
		index := fs.String("index", "", "directorio del índice SPIMI")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *input == "" || *index == "" || fs.NArg() != 0 {
			return errors.New("build requiere --input archivo.jsonl --index directorio")
		}
		return build(*input, *index)
	case "export":
		fs := flag.NewFlagSet("export", flag.ContinueOnError)
		index := fs.String("index", "", "directorio del índice SPIMI")
		output := fs.String("output", "", "archivo JSON para TF-IDF")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *index == "" || *output == "" || fs.NArg() != 0 {
			return errors.New("export requiere --index directorio --output archivo.json")
		}
		return export(*index, *output)
	case "preprocess":
		fs := flag.NewFlagSet("preprocess", flag.ContinueOnError)
		value := fs.String("text", "", "texto a normalizar con Go")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("argumentos inesperados")
		}
		tokens, err := text.PreprocessText(*value)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(tokens)
	case "search":
		fs := flag.NewFlagSet("search", flag.ContinueOnError)
		index := fs.String("index", "", "directorio del índice SPIMI")
		query := fs.String("query", "", "consulta en lenguaje natural")
		topK := fs.Int("top-k", 10, "cantidad de resultados")
		method := fs.String("method", "tfidf", "ranking: tfidf o bm25")
		k1 := fs.Float64("bm25-k1", ranking.DefaultBM25K1, "parámetro k1 de BM25")
		b := fs.Float64("bm25-b", ranking.DefaultBM25B, "parámetro b de BM25")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *index == "" || fs.NArg() != 0 {
			return errors.New("search requiere --index directorio --query texto [--method tfidf|bm25] [--top-k 10]")
		}
		reader, err := spimi.Open(*index)
		if err != nil {
			return err
		}
		defer reader.Close()
		tokens, err := text.PreprocessText(*query)
		if err != nil {
			return err
		}
		opts := ranking.SearchOptions{TopK: *topK, BM25K1: *k1, BM25B: *b}
		hits, err := ranking.Search(reader, ranking.Method(*method), tokens, opts)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(hits)
	default:
		return fmt.Errorf("comando desconocido: %s", args[0])
	}
}

func build(input, index string) error {
	f, err := os.Open(input)
	if err != nil {
		return err
	}
	defer f.Close()

	b, err := spimi.NewBuilder(spimi.Options{Dir: index})
	if err != nil {
		return err
	}
	defer b.Close()

	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 64*1024), 16*1024*1024)
	row, added := 0, 0
	for scan.Scan() {
		row++
		line := strings.TrimSpace(scan.Text())
		if line == "" {
			continue
		}
		var record struct {
			Text *string         `json:"text"`
			ID   json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return fmt.Errorf("JSONL línea %d: %w", row, err)
		}
		if record.Text == nil {
			return fmt.Errorf("JSONL línea %d: falta text de tipo string", row)
		}
		if len(record.ID) != 0 {
			return fmt.Errorf("JSONL línea %d: este puente de validación aún no admite id externos; use solo text", row)
		}
		tokens, err := text.PreprocessText(*record.Text)
		if err != nil {
			return fmt.Errorf("preprocesamiento línea %d: %w", row, err)
		}
		id, err := b.AddDocument(tokens)
		if err != nil {
			return fmt.Errorf("indexación línea %d: %w", row, err)
		}
		if id != uint32(added) {
			return fmt.Errorf("docID %d inesperado", id)
		}
		added++
	}
	if err := scan.Err(); err != nil {
		return err
	}
	stats, err := b.Finish()
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]int{
		"documents": stats.NumDocs, "terms": stats.NumTerms,
	})
}

func export(index, output string) error {
	reader, err := spimi.Open(index) // Resuelve CURRENT; no lee la generación equivocada.
	if err != nil {
		return err
	}
	defer reader.Close()

	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(output), "spimi-export-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := writeExport(f, reader); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), output)
}

func writeExport(dst io.Writer, r *spimi.Reader) error {
	w := bufio.NewWriterSize(dst, 1<<16)
	n := r.NumDocs()
	if _, err := fmt.Fprintf(w, `{"total_documents":%d,"doc_ids":[`, n); err != nil {
		return err
	}
	for i := 0; i < n; i++ {
		if i != 0 {
			if _, err := w.WriteString(","); err != nil {
				return err
			}
		}
		raw, _ := json.Marshal(strconv.Itoa(i))
		if _, err := w.Write(raw); err != nil {
			return err
		}
	}
	if _, err := w.WriteString(`],"postings":{`); err != nil {
		return err
	}
	for ti, term := range r.Terms() {
		if ti != 0 {
			if _, err := w.WriteString(","); err != nil {
				return err
			}
		}
		termJSON, _ := json.Marshal(term)
		if _, err := w.Write(termJSON); err != nil {
			return err
		}
		if _, err := w.WriteString(":{"); err != nil {
			return err
		}
		postings, err := r.Postings(term)
		if err != nil {
			return err
		}
		for j, p := range postings {
			if j != 0 {
				if _, err := w.WriteString(","); err != nil {
					return err
				}
			}
			docJSON, _ := json.Marshal(strconv.FormatUint(uint64(p.DocID), 10))
			if _, err := w.Write(docJSON); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, ":%d", p.TF); err != nil {
				return err
			}
		}
		if _, err := w.WriteString("}"); err != nil {
			return err
		}
	}
	if _, err := w.WriteString("}}"); err != nil {
		return err
	}
	return w.Flush()
}
