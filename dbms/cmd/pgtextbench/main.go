package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type settings struct {
	inputDir, qrels, output                   string
	topK, repeats, warmup, targetMS, maxBatch int
}
type manifest struct {
	SchemaVersion string `json:"schema_version"`
	Documents     int    `json:"documents"`
	Queries       int    `json:"queries"`
	CorpusSHA256  string `json:"corpus_sha256"`
}
type query struct {
	ID, Raw, Tokens, Expression string
	RawWords, NTokens           int
}
type hit struct {
	ID    int
	Score float64
}
type sampleFunc func() ([]hit, error)

const querySQL = `SELECT doc_id, ts_rank_cd(search_vector, to_tsquery('simple'::regconfig, $1))::double precision AS score
FROM bd2_textbench.documents
WHERE search_vector @@ to_tsquery('simple'::regconfig, $1)
ORDER BY score DESC, doc_id ASC LIMIT $2`

const planSQL = `EXPLAIN (FORMAT JSON) ` + querySQL

func main() {
	cfg := settings{}
	flag.StringVar(&cfg.inputDir, "input-dir", "", "carpeta pgtextprep con docs.csv, queries.csv, manifest.json")
	flag.StringVar(&cfg.qrels, "qrels", "", "JSON opcional con relevancias {query_id:[external_id,...]}")
	flag.StringVar(&cfg.output, "output", "", "CSV de salida (debe ser una ruta nueva)")
	flag.IntVar(&cfg.topK, "top-k", 10, "resultados por consulta")
	flag.IntVar(&cfg.warmup, "warmup", 3, "ejecuciones no medidas")
	flag.IntVar(&cfg.repeats, "repeats", 30, "cantidad de lotes medidos")
	flag.IntVar(&cfg.targetMS, "min-sample-ms", 5, "duración objetivo de un lote (ms)")
	flag.IntVar(&cfg.maxBatch, "max-batch", 65536, "maximo de consultas en un lote")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "pgtextbench: argumentos inesperados")
		os.Exit(2)
	}
	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "pgtextbench:", err)
		os.Exit(1)
	}
}

func readManifest(dir string) (manifest, error) {
	var m manifest
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return m, err
	}
	if err = json.Unmarshal(raw, &m); err != nil {
		return m, err
	}
	if m.SchemaVersion != "pgtextprep-v1" || m.Documents <= 0 || m.Queries <= 0 || len(m.CorpusSHA256) != 64 {
		return m, errors.New("manifest pgtextprep-v1 incompleto o inválido")
	}
	return m, nil
}
func readQueries(dir string, expected int) ([]query, error) {
	f, err := os.Open(filepath.Join(dir, "queries.csv"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	rows, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 || strings.Join(rows[0], "|") != "query_id|raw_text|normalized_tokens|or_tsquery|raw_words" {
		return nil, errors.New("cabecera de queries.csv inválida")
	}
	qs := make([]query, 0, len(rows)-1)
	seen := map[string]bool{}
	for _, row := range rows[1:] {
		if len(row) != 5 || row[0] == "" || seen[row[0]] {
			return nil, errors.New("consulta duplicada o inválida")
		}
		words, err := strconv.Atoi(row[4])
		if err != nil || words <= 0 {
			return nil, fmt.Errorf("raw_words inválido para %q", row[0])
		}
		q := query{ID: row[0], Raw: row[1], Tokens: row[2], Expression: row[3], RawWords: words, NTokens: len(strings.Fields(row[2]))}
		if q.Expression == "" || q.NTokens == 0 {
			return nil, fmt.Errorf("consulta %q sin términos normalizados: no es medible con PostgreSQL", q.ID)
		}
		if q.Raw == "" || strings.TrimSpace(q.Expression) == "" {
			return nil, fmt.Errorf("consulta %q incompleta", q.ID)
		}
		seen[q.ID] = true
		qs = append(qs, q)
	}
	if len(qs) != expected {
		return nil, fmt.Errorf("queries.csv tiene %d consultas y manifest indica %d", len(qs), expected)
	}
	return qs, nil
}
func readQrels(file string) (map[string]map[string]bool, error) {
	if file == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var data map[string][]string
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	rel := make(map[string]map[string]bool, len(data))
	for q, ids := range data {
		if q == "" {
			return nil, errors.New("qrel con query_id vacío")
		}
		rel[q] = make(map[string]bool)
		for _, id := range ids {
			if id == "" {
				return nil, fmt.Errorf("qrel %q contiene docID vacío", q)
			}
			rel[q][id] = true
		}
	}
	return rel, nil
}
func queryBucket(words int) string {
	switch {
	case words == 1:
		return "1"
	case words == 3:
		return "3"
	case words >= 5:
		return "5+"
	default:
		return "other"
	}
}
func metrics(ids []string, known map[string]bool, relevant map[string]bool, k int) (count int, p, r string) {
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
	}
	matched := 0
	for _, id := range ids {
		if known[id] && relevant[id] {
			matched++
		}
	}
	return count, fmt.Sprintf("%.8f", float64(matched)/float64(k)), fmt.Sprintf("%.8f", float64(matched)/float64(count))
}
func median(samples []float64) float64 {
	sorted := append([]float64(nil), samples...)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
func p95(samples []float64) float64 {
	sorted := append([]float64(nil), samples...)
	sort.Float64s(sorted)
	return sorted[int(math.Ceil(0.95*float64(len(sorted))))-1]
}
func batch(call sampleFunc, n int) ([]hit, time.Duration, error) {
	if n < 1 || call == nil {
		return nil, 0, errors.New("lote inválido")
	}
	start := time.Now()
	var last []hit
	for i := 0; i < n; i++ {
		res, err := call()
		if err != nil {
			return nil, 0, err
		}
		last = res
	}
	return last, time.Since(start), nil
}
func calibrate(call sampleFunc, target time.Duration, maxBatch int) (int, error) {
	if target <= 0 || maxBatch < 1 || call == nil {
		return 0, errors.New("calibración inválida")
	}
	for n := 1; ; {
		_, elapsed, err := batch(call, n)
		if err != nil {
			return 0, err
		}
		if elapsed >= target || n == maxBatch {
			if elapsed <= 0 {
				return 0, errors.New("reloj sin resolución")
			}
			return n, nil
		}
		if n >= maxBatch/2 {
			n = maxBatch
		} else {
			n *= 2
		}
	}
}
func openDB(ctx context.Context) (*sql.DB, error) {
	// pgx acepta PGPASSWORD o PGPASSFILE. Nunca se registra la contraseña.
	// Una sola conexion persistente: no hay coste de conexion en las muestras.
	db, err := sql.Open("pgx", "host=127.0.0.1 port=5432 user=postgres dbname=postgres sslmode=disable application_name=bd2_pgtextbench")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("PostgreSQL localhost:5432/postgres: %w", err)
	}
	return db, nil
}
func verifyData(ctx context.Context, db *sql.DB, dir string, expected int) (map[int]string, map[string]bool, error) {
	f, err := os.Open(filepath.Join(dir, "docs.csv"))
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	csvr := csv.NewReader(f)
	header, err := csvr.Read()
	if err != nil {
		return nil, nil, err
	}
	if strings.Join(header, "|") != "doc_id|external_id|normalized_text" {
		return nil, nil, errors.New("cabecera docs.csv inválida")
	}
	// Verificacion de datos fuera del cronometro: cuenta, DocID, IDs externos y texto.
	rows, err := db.QueryContext(ctx, `SELECT doc_id,external_id,normalized_text FROM bd2_textbench.documents ORDER BY doc_id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	ids := make(map[int]string, expected)
	known := make(map[string]bool, expected)
	for i := 0; i < expected; i++ {
		rec, e := csvr.Read()
		if e != nil {
			return nil, nil, fmt.Errorf("docs.csv no contiene %d filas: %w", expected, e)
		}
		if len(rec) != 3 || rec[0] != strconv.Itoa(i) || rec[1] == "" || known[rec[1]] {
			return nil, nil, fmt.Errorf("docs.csv fila %d inválida", i+2)
		}
		if !rows.Next() {
			if e = rows.Err(); e != nil {
				return nil, nil, e
			}
			return nil, nil, fmt.Errorf("PostgreSQL tiene menos de %d documentos", expected)
		}
		var id int
		var external, normalized string
		if e = rows.Scan(&id, &external, &normalized); e != nil {
			return nil, nil, e
		}
		if id != i || external != rec[1] || normalized != rec[2] {
			return nil, nil, fmt.Errorf("PostgreSQL no coincide con docs.csv en docID=%d (repetir pg_loader.py load --reset para este corpus)", i)
		}
		ids[i] = external
		known[external] = true
	}
	if _, e := csvr.Read(); e == nil {
		return nil, nil, errors.New("docs.csv tiene filas adicionales")
	} else if !errors.Is(e, io.EOF) {
		return nil, nil, e
	}
	if rows.Next() {
		return nil, nil, errors.New("PostgreSQL contiene documentos adicionales")
	}
	if err = rows.Err(); err != nil {
		return nil, nil, err
	}
	return ids, known, nil
}
func ginMetadata(ctx context.Context, db *sql.DB) (ginBytes, tableBytes, totalBytes int64, version string, err error) {
	var exists bool
	err = db.QueryRowContext(ctx, `SELECT EXISTS (
 SELECT 1 FROM pg_class AS i
 JOIN pg_namespace AS ns ON ns.oid=i.relnamespace
 JOIN pg_index AS ix ON ix.indexrelid=i.oid
 JOIN pg_class AS t ON t.oid=ix.indrelid
 JOIN pg_am AS am ON am.oid=i.relam
 WHERE ns.nspname='bd2_textbench' AND i.relname='documents_search_vector_gin_idx'
 AND t.relname='documents' AND am.amname='gin' AND ix.indisvalid)`).Scan(&exists)
	if err != nil {
		return
	}
	if !exists {
		err = errors.New("no existe índice GIN válido: ejecuta pg_loader.py load")
		return
	}
	err = db.QueryRowContext(ctx, `SELECT pg_relation_size('bd2_textbench.documents_search_vector_gin_idx'::regclass),
 pg_relation_size('bd2_textbench.documents'::regclass),
 pg_total_relation_size('bd2_textbench.documents'::regclass),current_setting('server_version')`).Scan(&ginBytes, &tableBytes, &totalBytes, &version)
	return
}
func parsePlan(data []byte) (string, bool, error) {
	var roots []struct {
		Plan map[string]any `json:"Plan"`
	}
	if err := json.Unmarshal(data, &roots); err != nil {
		return "", false, err
	}
	if len(roots) != 1 {
		return "", false, errors.New("EXPLAIN JSON inesperado")
	}
	var walk func(map[string]any)
	kinds := []string{}
	ginUsed := false
	walk = func(p map[string]any) {
		node, _ := p["Node Type"].(string)
		if node != "" {
			kinds = append(kinds, node)
		}
		idx, _ := p["Index Name"].(string)
		if idx == "documents_search_vector_gin_idx" {
			ginUsed = true
		}
		children, _ := p["Plans"].([]any)
		for _, child := range children {
			if node, ok := child.(map[string]any); ok {
				walk(node)
			}
		}
	}
	walk(roots[0].Plan)
	return strings.Join(kinds, " > "), ginUsed, nil
}
func pgPlan(ctx context.Context, db *sql.DB, expression string, k int) (string, bool, error) {
	// EXPLAIN se ejecuta FUERA de las muestras temporales.
	var raw []byte
	if err := db.QueryRowContext(ctx, planSQL, expression, k).Scan(&raw); err != nil {
		return "", false, err
	}
	return parsePlan(raw)
}
func doSearch(ctx context.Context, stmt *sql.Stmt, expression string, k int) ([]hit, error) {
	rows, err := stmt.QueryContext(ctx, expression, k)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hits := make([]hit, 0, k)
	for rows.Next() {
		var h hit
		if err = rows.Scan(&h.ID, &h.Score); err != nil {
			return nil, err
		}
		if h.ID < 0 || math.IsNaN(h.Score) || math.IsInf(h.Score, 0) {
			return nil, errors.New("resultado PostgreSQL inválido")
		}
		hits = append(hits, h)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return hits, nil
}

// Primeras 38 columnas compatibles por nombre con textbench v2 (CSV puede
// concatenarse tras normalizar columnas; los valores SPIMI no se inventan).
var header = []string{
	"n_documents", "corpus_sha256", "method", "query_id", "query_text", "raw_query_terms", "normalized_query_terms", "query_bucket", "top_k", "warmup", "repeats",
	"preprocess_documents_ms", "spimi_build_ms", "spimi_open_ms", "spimi_index_bytes", "index_terms", "index_postings",
	"query_median_ms", "query_p95_ms", "query_mean_ms", "retrieved_ids", "relevant_in_subset", "precision_at_k", "recall_at_k", "latencies_ms",
	"go_version", "goos", "num_cpu", "gomaxprocs", "bm25_k1", "bm25_b", "timing_scope", "index_source",
	"batch_size", "target_sample_ms", "query_median_us", "query_p95_us", "batch_durations_ms",
	"postgres_gin_bytes", "postgres_table_bytes", "postgres_total_bytes", "postgres_version", "plan_nodes", "gin_used",
}

func run(cfg settings) error {
	if cfg.inputDir == "" || cfg.output == "" {
		return errors.New("requiere --input-dir y --output")
	}
	if cfg.topK <= 0 || cfg.warmup < 0 || cfg.repeats <= 0 || cfg.targetMS < 1 || cfg.targetMS > 1000 || cfg.maxBatch < 1 || cfg.maxBatch > 1<<20 {
		return errors.New("top-k, repeats >0; warmup >=0; min-sample-ms [1,1000]; max-batch [1,1048576]")
	}
	m, err := readManifest(cfg.inputDir)
	if err != nil {
		return err
	}
	qs, err := readQueries(cfg.inputDir, m.Queries)
	if err != nil {
		return err
	}
	relevant, err := readQrels(cfg.qrels)
	if err != nil {
		return err
	}
	if _, err := os.Stat(cfg.output); err == nil {
		return fmt.Errorf("output ya existe: %s", cfg.output)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()
	db, err := openDB(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	ids, known, err := verifyData(ctx, db, cfg.inputDir, m.Documents)
	if err != nil {
		return err
	}
	ginBytes, tableBytes, totalBytes, pgVersion, err := ginMetadata(ctx, db)
	if err != nil {
		return err
	}
	stmt, err := db.PrepareContext(ctx, querySQL)
	if err != nil {
		return err
	}
	defer stmt.Close()
	if err = os.MkdirAll(filepath.Dir(cfg.output), 0o755); err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(cfg.output), "pgtextbench-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	cw := csv.NewWriter(out)
	if err = cw.Write(header); err != nil {
		out.Close()
		return err
	}
	for _, q := range qs {
		plan, ginUsed, err := pgPlan(ctx, db, q.Expression, cfg.topK)
		if err != nil {
			out.Close()
			return err
		}
		call := func() ([]hit, error) { return doSearch(ctx, stmt, q.Expression, cfg.topK) }
		for i := 0; i < cfg.warmup; i++ {
			if _, err = call(); err != nil {
				out.Close()
				return fmt.Errorf("warmup %q: %w", q.ID, err)
			}
		}
		size, err := calibrate(call, time.Duration(cfg.targetMS)*time.Millisecond, cfg.maxBatch)
		if err != nil {
			out.Close()
			return err
		}
		times := make([]float64, 0, cfg.repeats)
		batches := make([]float64, 0, cfg.repeats)
		var hits []hit
		for i := 0; i < cfg.repeats; i++ {
			h, elapsed, e := batch(call, size)
			if e != nil {
				out.Close()
				return fmt.Errorf("consulta %q: %w", q.ID, e)
			}
			if i == 0 {
				hits = h
			}
			times = append(times, float64(elapsed.Nanoseconds())/1e6/float64(size))
			batches = append(batches, float64(elapsed.Nanoseconds())/1e6)
		}
		retrieved := make([]string, 0, len(hits))
		for _, h := range hits {
			id, ok := ids[h.ID]
			if !ok {
				out.Close()
				return fmt.Errorf("DocID %d no pertenece al corpus", h.ID)
			}
			retrieved = append(retrieved, id)
		}
		var rel map[string]bool
		if relevant != nil {
			rel = relevant[q.ID]
		}
		count, p, r := metrics(retrieved, known, rel, cfg.topK)
		countText := ""
		if count >= 0 {
			countText = strconv.Itoa(count)
		}
		latJSON, _ := json.Marshal(times)
		batchJSON, _ := json.Marshal(batches)
		sum := 0.0
		for _, v := range times {
			sum += v
		}
		row := []string{
			strconv.Itoa(m.Documents), m.CorpusSHA256, "pg_gin", q.ID, q.Raw, strconv.Itoa(q.RawWords), strconv.Itoa(q.NTokens), queryBucket(q.RawWords),
			strconv.Itoa(cfg.topK), strconv.Itoa(cfg.warmup), strconv.Itoa(cfg.repeats),
			"", "", "", "", "", "", // SPIMI metadata no aplica a PostgreSQL
			fmt.Sprintf("%.9f", median(times)), fmt.Sprintf("%.9f", p95(times)), fmt.Sprintf("%.9f", sum/float64(len(times))),
			strings.Join(retrieved, "|"), countText, p, r, string(latJSON), runtime.Version(), runtime.GOOS, strconv.Itoa(runtime.NumCPU()), strconv.Itoa(runtime.GOMAXPROCS(0)),
			"", "", "hot_batched_postgres_round_trip_go", "postgres_gin", strconv.Itoa(size), strconv.Itoa(cfg.targetMS),
			fmt.Sprintf("%.6f", median(times)*1000), fmt.Sprintf("%.6f", p95(times)*1000), string(batchJSON),
			strconv.FormatInt(ginBytes, 10), strconv.FormatInt(tableBytes, 10), strconv.FormatInt(totalBytes, 10), pgVersion, plan, strconv.FormatBool(ginUsed),
		}
		if len(row) != len(header) {
			out.Close()
			return fmt.Errorf("CSV: %d valores contra %d columnas", len(row), len(header))
		}
		if err = cw.Write(row); err != nil {
			out.Close()
			return err
		}
		fmt.Fprintf(os.Stderr, "q=%s | pg_gin | mediana %.3f us | batch=%d | plan=%s | GIN usado=%t\n", q.ID, median(times)*1000, size, plan, ginUsed)
	}
	cw.Flush()
	if err = cw.Error(); err != nil {
		out.Close()
		return err
	}
	if err = out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	if err = os.Rename(out.Name(), cfg.output); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "pgtextbench: %d documentos, %d consultas, GIN=%d bytes; CSV: %s\n", m.Documents, len(qs), ginBytes, cfg.output)
	return nil
}
