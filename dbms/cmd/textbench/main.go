// textbench: benchmark de búsqueda TF-IDF y BM25 sobre el MISMO índice SPIMI.
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
	"hash"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dbms-go/v2/dbms/lib/index/text/ranking"
	"github.com/dbms-go/v2/dbms/lib/index/text/spimi"
	text "github.com/dbms-go/v2/dbms/lib/text"
)

type config struct {
	documents, queries, qrels, sizes, output, indexRoot string
	topK, repeats, warmup                               int
	minSampleMS, maxBatch                               int
	k1, b                                               float64
}

type query struct {
	ID       string   `json:"id"`
	Text     string   `json:"text"`
	Tokens   []string `json:"-"`
	RawWords int      `json:"-"`
}

type corpus struct {
	IDs        []string
	Known      map[string]bool
	Hash       string
	Preprocess time.Duration
	Stats      spimi.BuildStats
}

var methods = []ranking.Method{ranking.MethodTFIDF, ranking.MethodBM25}

func main() {
	cfg := config{}
	flag.StringVar(&cfg.documents, "documents", "", "corpus JSONL: {id, text}; id opcional")
	flag.StringVar(&cfg.queries, "queries", "", "consultas JSON: [{id, text}, ...]")
	flag.StringVar(&cfg.qrels, "qrels", "", "opcional: JSON {query_id: [document_id, ...]}")
	flag.StringVar(&cfg.sizes, "sizes", "1000,10000,100000", "prefijos del corpus")
	flag.StringVar(&cfg.output, "output", "", "CSV de resultados (ruta nueva)")
	flag.StringVar(&cfg.indexRoot, "index-root", "", "opcional: guardar índices en esta carpeta NUEVA")
	flag.IntVar(&cfg.topK, "top-k", 10, "K resultados; por defecto Precision@10/Recall@10")
	flag.IntVar(&cfg.repeats, "repeats", 30, "repeticiones medidas por consulta y algoritmo")
	flag.IntVar(&cfg.warmup, "warmup", 3, "repeticiones de calentamiento no medidas")
	flag.IntVar(&cfg.minSampleMS, "min-sample-ms", defaultMinSampleMS, "duración objetivo mínima de cada lote medido (ms)")
	flag.IntVar(&cfg.maxBatch, "max-batch", defaultMaxBatch, "máximo de búsquedas por lote (calibración adaptativa)")
	flag.Float64Var(&cfg.k1, "bm25-k1", ranking.DefaultBM25K1, "BM25 k1")
	flag.Float64Var(&cfg.b, "bm25-b", ranking.DefaultBM25B, "BM25 b")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "textbench: argumentos extra")
		os.Exit(2)
	}
	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "textbench:", err)
		os.Exit(1)
	}
}

func parseSizes(raw string) ([]int, error) {
	out := []int{}
	seen := make(map[int]bool)
	for _, piece := range strings.Split(raw, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(piece))
		if err != nil || n <= 0 || seen[n] {
			return nil, fmt.Errorf("sizes inválidos: %q", raw)
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Ints(out)
	return out, nil
}

func loadQueries(path string) ([]query, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var qs []query
	if err := json.Unmarshal(raw, &qs); err != nil {
		return nil, fmt.Errorf("queries: %w", err)
	}
	if len(qs) == 0 {
		return nil, errors.New("queries no puede estar vacío")
	}
	used := make(map[string]bool)
	for i := range qs {
		q := &qs[i]
		if q.ID == "" || strings.TrimSpace(q.Text) == "" || used[q.ID] {
			return nil, fmt.Errorf("consulta %d: id duplicado/vacío o texto vacío", i)
		}
		used[q.ID] = true
		q.RawWords = len(strings.Fields(q.Text))
		q.Tokens, err = text.PreprocessText(q.Text)
		if err != nil {
			return nil, fmt.Errorf("consulta %q: %w", q.ID, err)
		}
	}
	return qs, nil
}

func loadQrels(path string) (map[string]map[string]bool, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var input map[string][]string
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, fmt.Errorf("qrels: %w", err)
	}
	out := make(map[string]map[string]bool)
	for queryID, ids := range input {
		if queryID == "" {
			return nil, errors.New("qrels contiene query_id vacío")
		}
		out[queryID] = make(map[string]bool)
		for _, id := range ids {
			if id == "" {
				return nil, fmt.Errorf("qrels %q: id documento vacío", queryID)
			}
			out[queryID][id] = true
		}
	}
	return out, nil
}

func documentID(raw json.RawMessage, fallback int) (string, error) {
	if len(raw) == 0 {
		return strconv.Itoa(fallback), nil
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		if str == "" {
			return "", errors.New("id vacío")
		}
		return str, nil
	}
	var num json.Number
	if err := json.Unmarshal(raw, &num); err == nil {
		if num.String() == "" {
			return "", errors.New("id vacío")
		}
		return num.String(), nil
	}
	return "", errors.New("id debe ser string o número")
}

func buildCorpus(path, dir string, limit int) (corpus, error) {
	res := corpus{IDs: make([]string, 0, limit), Known: make(map[string]bool, limit)}
	input, err := os.Open(path)
	if err != nil {
		return res, err
	}
	defer input.Close()
	builder, err := spimi.NewBuilder(spimi.Options{Dir: dir})
	if err != nil {
		return res, err
	}
	defer builder.Close()

	h := sha256.New()
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	lineNo := 0
	for len(res.IDs) < limit && scanner.Scan() {
		lineNo++
		raw := scanner.Bytes()
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue
		}
		var doc struct {
			ID   json.RawMessage `json:"id"`
			Text *string         `json:"text"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return res, fmt.Errorf("documento línea %d: %w", lineNo, err)
		}
		if doc.Text == nil {
			return res, fmt.Errorf("documento línea %d: falta text string", lineNo)
		}
		id, err := documentID(doc.ID, len(res.IDs))
		if err != nil {
			return res, fmt.Errorf("documento línea %d: %w", lineNo, err)
		}
		if res.Known[id] {
			return res, fmt.Errorf("documento línea %d: id duplicado %q", lineNo, id)
		}
		start := time.Now()
		tokens, err := text.PreprocessText(*doc.Text)
		res.Preprocess += time.Since(start)
		if err != nil {
			return res, fmt.Errorf("preprocesar doc %q: %w", id, err)
		}
		got, err := builder.AddDocument(tokens)
		if err != nil {
			return res, fmt.Errorf("SPIMI doc %q: %w", id, err)
		}
		if int(got) != len(res.IDs) {
			return res, fmt.Errorf("docID SPIMI inesperado %d", got)
		}
		res.IDs = append(res.IDs, id)
		res.Known[id] = true
		hashLine(h, raw)
	}
	if err := scanner.Err(); err != nil {
		return res, err
	}
	if len(res.IDs) != limit {
		return res, fmt.Errorf("corpus contiene %d documentos, se solicitaron %d", len(res.IDs), limit)
	}
	stats, err := builder.Finish()
	if err != nil {
		return res, err
	}
	res.Stats = stats
	res.Hash = hex.EncodeToString(h.Sum(nil))
	return res, nil
}

func hashLine(h hash.Hash, line []byte) { _, _ = h.Write(line); _, _ = h.Write([]byte{'\n'}) }

func queryBucket(n int) string {
	switch {
	case n == 1:
		return "1"
	case n == 3:
		return "3"
	case n >= 5:
		return "5+"
	default:
		return "other"
	}
}

func median(samples []float64) float64 {
	v := append([]float64(nil), samples...)
	sort.Float64s(v)
	n := len(v)
	if n%2 == 1 {
		return v[n/2]
	}
	return (v[n/2-1] + v[n/2]) / 2
}

// p95 usa el método nearest-rank (elemento ceil(0.95*n)).
func p95(samples []float64) float64 {
	v := append([]float64(nil), samples...)
	sort.Float64s(v)
	return v[int(math.Ceil(0.95*float64(len(v))))-1]
}

func metrics(ids []string, known, relevant map[string]bool, k int) (count int, precision, recall string) {
	if relevant == nil {
		return -1, "", ""
	}
	for id := range relevant {
		if known[id] {
			count++
		}
	}
	if count == 0 {
		return 0, "", ""
	} // N/A: no hay relevantes en este prefijo.
	matched := 0
	for _, id := range ids {
		if relevant[id] && known[id] {
			matched++
		}
	}
	return count, strconv.FormatFloat(float64(matched)/float64(k), 'f', 8, 64),
		strconv.FormatFloat(float64(matched)/float64(count), 'f', 8, 64)
}

func measure(reader *spimi.Reader, qs []query, cfg config, data corpus,
	allQrels map[string]map[string]bool, hasQrels bool, csvWriter *csv.Writer, openTime time.Duration) error {
	opts := ranking.DefaultSearchOptions(cfg.topK)
	opts.BM25K1, opts.BM25B = cfg.k1, cfg.b
	for qi, q := range qs {
		for _, method := range methods {
			for j := 0; j < cfg.warmup; j++ {
				if _, err := ranking.Search(reader, method, q.Tokens, opts); err != nil {
					return fmt.Errorf("warmup %s %s: %w", method, q.ID, err)
				}
			}
		}
		// Calibración por consulta y algoritmo, DESPUÉS del warmup y FUERA de las muestras.
		// Así, los casos pequeños tienen resolución útil y los grandes no hacen
		// innecesariamente miles de consultas por repetición.
		runners := make(map[ranking.Method]searchCall, len(methods))
		batchSizes := make(map[ranking.Method]int, len(methods))
		times := make(map[ranking.Method][]float64, len(methods))
		batchTimes := make(map[ranking.Method][]float64, len(methods))
		for _, method := range methods {
			method := method
			runners[method] = func() ([]ranking.SearchResult, error) {
				return ranking.Search(reader, method, q.Tokens, opts)
			}
			n, err := calibrateBatch(runners[method], time.Duration(cfg.minSampleMS)*time.Millisecond, cfg.maxBatch)
			if err != nil {
				return fmt.Errorf("calibración %s %s: %w", method, q.ID, err)
			}
			batchSizes[method] = n
			times[method] = make([]float64, 0, cfg.repeats)
			batchTimes[method] = make([]float64, 0, cfg.repeats)
		}
		hits := make(map[ranking.Method][]ranking.SearchResult)
		// Alternar el orden entre algoritmos para reducir sesgo de caché/orden.
		for repeat := 0; repeat < cfg.repeats; repeat++ {
			first, second := methods[0], methods[1]
			if (repeat+qi)%2 == 1 {
				first, second = second, first
			}
			for _, method := range []ranking.Method{first, second} {
				found, elapsed, err := runSearchBatch(runners[method], batchSizes[method])
				if err != nil {
					return fmt.Errorf("búsqueda %s %s: %w", method, q.ID, err)
				}
				if elapsed <= 0 {
					return fmt.Errorf("reloj sin resolución útil para %s %s: aumenta --max-batch", method, q.ID)
				}
				batchMS := float64(elapsed.Nanoseconds()) / 1e6
				batchTimes[method] = append(batchTimes[method], batchMS)
				times[method] = append(times[method], batchMS/float64(batchSizes[method]))
				if repeat == 0 {
					hits[method] = found
				}
			}
		}
		for _, method := range methods {
			ids := make([]string, 0, len(hits[method]))
			for _, hit := range hits[method] {
				if int(hit.DocID) >= len(data.IDs) {
					return fmt.Errorf("docID %d fuera de rango", hit.DocID)
				}
				ids = append(ids, data.IDs[hit.DocID])
			}
			var relevant map[string]bool
			if hasQrels {
				if r, ok := allQrels[q.ID]; ok {
					relevant = r
				}
			}
			count, precision, recall := metrics(ids, data.Known, relevant, cfg.topK)
			countField := ""
			if count >= 0 {
				countField = strconv.Itoa(count)
			}
			series, _ := json.Marshal(times[method])
			batchSeries, _ := json.Marshal(batchTimes[method])
			sum := 0.0
			for _, ms := range times[method] {
				sum += ms
			}
			row := []string{
				strconv.Itoa(len(data.IDs)), data.Hash, string(method), q.ID, q.Text,
				strconv.Itoa(q.RawWords), strconv.Itoa(len(q.Tokens)), queryBucket(q.RawWords),
				strconv.Itoa(cfg.topK), strconv.Itoa(cfg.warmup), strconv.Itoa(cfg.repeats),
				fmt.Sprintf("%.6f", float64(data.Preprocess.Nanoseconds())/1e6),
				fmt.Sprintf("%.6f", float64(data.Stats.Elapsed.Nanoseconds())/1e6),
				fmt.Sprintf("%.6f", float64(openTime.Nanoseconds())/1e6),
				strconv.FormatInt(data.Stats.IndexBytes, 10),
				strconv.Itoa(data.Stats.NumTerms), strconv.FormatInt(data.Stats.NumPostings, 10),
				fmt.Sprintf("%.9f", median(times[method])), fmt.Sprintf("%.9f", p95(times[method])),
				fmt.Sprintf("%.9f", sum/float64(len(times[method]))),
				strings.Join(ids, "|"), countField, precision, recall, string(series),
				runtime.Version(), runtime.GOOS, strconv.Itoa(runtime.NumCPU()),
				strconv.Itoa(runtime.GOMAXPROCS(0)),
				strconv.FormatFloat(cfg.k1, 'f', 3, 64),
				strconv.FormatFloat(cfg.b, 'f', 3, 64),
				"hot_batched_direct_go_call", "spimi_shared",
				strconv.Itoa(batchSizes[method]), strconv.Itoa(cfg.minSampleMS),
				fmt.Sprintf("%.6f", median(times[method])*1000),
				fmt.Sprintf("%.6f", p95(times[method])*1000), string(batchSeries),
			}
			if err := csvWriter.Write(row); err != nil {
				return err
			}
		}
	}
	return nil
}

var csvHeader = []string{
	"n_documents", "corpus_sha256", "method", "query_id", "query_text", "raw_query_terms",
	"normalized_query_terms", "query_bucket", "top_k", "warmup", "repeats",
	"preprocess_documents_ms", "spimi_build_ms", "spimi_open_ms", "spimi_index_bytes",
	"index_terms", "index_postings", "query_median_ms", "query_p95_ms", "query_mean_ms",
	"retrieved_ids", "relevant_in_subset", "precision_at_k", "recall_at_k", "latencies_ms",
	"go_version", "goos", "num_cpu", "gomaxprocs", "bm25_k1", "bm25_b", "timing_scope", "index_source",
	"batch_size", "target_sample_ms", "query_median_us", "query_p95_us", "batch_durations_ms",
}

func run(cfg config) error {
	// El test smoke construye config directamente, sin pasar por flag.Parse.
	if cfg.minSampleMS == 0 {
		cfg.minSampleMS = defaultMinSampleMS
	}
	if cfg.maxBatch == 0 {
		cfg.maxBatch = defaultMaxBatch
	}
	if cfg.minSampleMS < 1 || cfg.minSampleMS > 1000 || cfg.maxBatch < 1 || cfg.maxBatch > 1<<20 {
		return errors.New("min-sample-ms debe estar entre 1 y 1000; max-batch entre 1 y 1048576")
	}
	if cfg.documents == "" || cfg.queries == "" || cfg.output == "" {
		return errors.New("requiere --documents, --queries y --output")
	}
	if cfg.topK < 1 || cfg.repeats < 1 || cfg.warmup < 0 {
		return errors.New("top-k/repeats deben ser > 0; warmup >= 0")
	}
	if math.IsNaN(cfg.k1) || math.IsInf(cfg.k1, 0) || cfg.k1 < 0 || math.IsNaN(cfg.b) || math.IsInf(cfg.b, 0) || cfg.b < 0 || cfg.b > 1 {
		return errors.New("parámetros BM25 inválidos")
	}
	sizes, err := parseSizes(cfg.sizes)
	if err != nil {
		return err
	}
	qs, err := loadQueries(cfg.queries)
	if err != nil {
		return err
	}
	qrels, err := loadQrels(cfg.qrels)
	if err != nil {
		return err
	}
	if _, err := os.Stat(cfg.output); err == nil {
		return fmt.Errorf("output ya existe: %s (no se sobrescribe)", cfg.output)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.output), 0o755); err != nil {
		return err
	}
	output, err := os.CreateTemp(filepath.Dir(cfg.output), "textbench-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(output.Name())
	w := csv.NewWriter(output)
	if err := w.Write(csvHeader); err != nil {
		output.Close()
		return err
	}

	root := cfg.indexRoot
	temporary := root == ""
	if temporary {
		root, err = os.MkdirTemp("", "bd2-textbench-*")
		if err != nil {
			output.Close()
			return err
		}
		defer func() {
			if err := os.RemoveAll(root); err != nil {
				fmt.Fprintln(os.Stderr, "aviso: limpieza temporal:", err)
			}
		}()
	} else {
		if _, err := os.Stat(root); err == nil {
			output.Close()
			return fmt.Errorf("index-root debe ser una carpeta nueva: %s", root)
		} else if !errors.Is(err, os.ErrNotExist) {
			output.Close()
			return err
		}
		if err := os.MkdirAll(root, 0o755); err != nil {
			output.Close()
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "textbench Go=%s GOOS=%s CPUs=%d GOMAXPROCS=%d\n", runtime.Version(), runtime.GOOS, runtime.NumCPU(), runtime.GOMAXPROCS(0))
	for _, n := range sizes {
		dir := filepath.Join(root, fmt.Sprintf("n-%d", n))
		data, err := buildCorpus(cfg.documents, dir, n)
		if err != nil {
			output.Close()
			return fmt.Errorf("n=%d: %w", n, err)
		}
		t0 := time.Now()
		reader, err := spimi.Open(dir)
		openTime := time.Since(t0)
		if err != nil {
			output.Close()
			return err
		}
		err = measure(reader, qs, cfg, data, qrels, qrels != nil, w, openTime)
		closeErr := reader.Close()
		if err != nil {
			output.Close()
			return err
		}
		if closeErr != nil {
			output.Close()
			return closeErr
		}
		fmt.Fprintf(os.Stderr, "n=%d | SPIMI build=%.3fms | índice=%d bytes | consultas=%d x 2\n", n, float64(data.Stats.Elapsed.Nanoseconds())/1e6, data.Stats.IndexBytes, len(qs))
	}
	w.Flush()
	if err := w.Error(); err != nil {
		output.Close()
		return err
	}
	if err := output.Sync(); err != nil {
		output.Close()
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	if err := os.Rename(output.Name(), cfg.output); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "CSV creado:", cfg.output)
	return nil
}
