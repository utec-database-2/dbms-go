// pgtextbench mide PostgreSQL GIN + ts_rank
package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/dbms-go/v2/dbms/lib/benchclock"
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
	inputDir, qrels, output, ranking, planMode string
	topK, repeats, warmup, targetMS, maxBatch  int
	serverRepeats, prepRepeats                 int
}
type manifest struct {
	SchemaVersion        string `json:"schema_version"`
	Documents            int    `json:"documents"`
	Queries              int    `json:"queries"`
	CorpusSHA256         string `json:"corpus_sha256"`
	PostgreSQLTextConfig string `json:"postgresql_text_config"`
}
type query struct {
	ID, Raw, Expression string
	RawWords            int
}
type hit struct {
	ID    int
	Score float64
}
type sampleFunc func() ([]hit, error)

// El ranking se selecciona por lista cerrada: nunca se inserta SQL del usuario.
// El tsquery se prepara por PostgreSQL FUERA del cronómetro, igual que
// PreprocessText para SPIMI en textbench.
const querySQLTemplate = `SELECT doc_id, %s(search_vector, $1::tsquery)::double precision AS score
FROM bd2_textbench.documents
WHERE search_vector @@ $1::tsquery
ORDER BY score DESC, doc_id ASC LIMIT $2`

const prepareTSQuerySQL = `SELECT websearch_to_tsquery('spanish'::regconfig, $1)::text`

func rankingQuerySQL(ranking string) (string, error) {
	switch ranking {
	case "ts_rank", "ts_rank_cd":
		return fmt.Sprintf(querySQLTemplate, ranking), nil
	default:
		return "", fmt.Errorf("ranking inválido %q: usa ts_rank o ts_rank_cd", ranking)
	}
}

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
	flag.StringVar(&cfg.ranking, "ranking", "ts_rank", "ranking: ts_rank (principal) o ts_rank_cd (referencia)")
	flag.IntVar(&cfg.serverRepeats, "server-repeats", 5, "repeticiones EXPLAIN ANALYZE para tiempo del servidor")
	flag.IntVar(&cfg.prepRepeats, "prep-repeats", 10, "repeticiones de preparacion de tsquery, fuera de la busqueda")
	flag.StringVar(&cfg.planMode, "plan-mode", "auto", "plan_cache_mode: auto, force_custom_plan, force_generic_plan")
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
	if m.SchemaVersion != "pgtextprep-native-v2" || m.PostgreSQLTextConfig != "spanish" || m.Documents <= 0 || m.Queries <= 0 || len(m.CorpusSHA256) != 64 {
		return m, errors.New("manifest pgtextprep-native-v2 incompleto o inválido")
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
	if len(rows) == 0 || strings.Join(rows[0], "|") != "query_id|raw_text|raw_words" {
		return nil, errors.New("cabecera de queries.csv inválida")
	}
	qs := make([]query, 0, len(rows)-1)
	seen := map[string]bool{}
	for _, row := range rows[1:] {
		if len(row) != 3 || row[0] == "" || seen[row[0]] {
			return nil, errors.New("consulta duplicada o inválida")
		}
		words, err := strconv.Atoi(row[2])
		if err != nil || words <= 0 {
			return nil, fmt.Errorf("raw_words inválido para %q", row[0])
		}
		if strings.TrimSpace(row[1]) == "" || len(strings.Fields(row[1])) != words {
			return nil, fmt.Errorf("consulta %q incompleta o raw_words incorrecto", row[0])
		}
		// OR es el operador booleano común. La tokenización, stopwords y
		// stemming se realizan EXCLUSIVAMENTE en PostgreSQL mediante spanish.
		q := query{ID: row[0], Raw: row[1], Expression: strings.Join(strings.Fields(row[1]), " OR "), RawWords: words}
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
	start := benchclock.Now()
	var last []hit
	for i := 0; i < n; i++ {
		res, err := call()
		if err != nil {
			return nil, 0, err
		}
		last = res
	}
	return last, benchclock.Since(start), nil
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
	if strings.Join(header, "|") != "doc_id|external_id|raw_text" {
		return nil, nil, errors.New("cabecera docs.csv inválida")
	}
	// Verificacion de datos fuera del cronometro: cuenta, DocID, IDs externos y texto.
	rows, err := db.QueryContext(ctx, `SELECT doc_id,external_id,raw_text FROM bd2_textbench.documents ORDER BY doc_id`)
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
		var external, rawText string
		if e = rows.Scan(&id, &external, &rawText); e != nil {
			return nil, nil, e
		}
		if id != i || external != rec[1] || rawText != rec[2] {
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
func pgPlan(ctx context.Context, conn *sql.Conn, executeSQL string) (string, bool, error) {
	// EXPLAIN sin ANALYZE: fuera del cronómetro.
	var raw []byte
	if err := conn.QueryRowContext(ctx, "EXPLAIN (FORMAT JSON) "+executeSQL).Scan(&raw); err != nil {
		return "", false, err
	}
	return parsePlan(raw)
}

func prepareQuery(ctx context.Context, conn *sql.Conn, expression string) (string, error) {
	var tsquery string
	if err := conn.QueryRowContext(ctx, prepareTSQuerySQL, expression).Scan(&tsquery); err != nil {
		return "", err
	}
	return tsquery, nil // cadena vacía: consulta sin lexemas, devuelve cero filas
}

// readExplainTiming extrae Execution Time y Planning Time en MILISEGUNDOS.
// TIMING OFF reduce instrumentación por nodo, pero EXPLAIN ANALYZE
// sigue añadiendo overhead: este valor NO es una medición pura del índice.
func readExplainTiming(raw []byte) (executionMS, planningMS float64, plan string, ginUsed bool, err error) {
	var roots []struct {
		ExecutionTime *float64 `json:"Execution Time"`
		PlanningTime  *float64 `json:"Planning Time"`
	}
	if err = json.Unmarshal(raw, &roots); err != nil {
		return
	}
	if len(roots) != 1 || roots[0].ExecutionTime == nil || roots[0].PlanningTime == nil {
		err = errors.New("EXPLAIN ANALYZE JSON sin tiempos completos")
		return
	}
	executionMS, planningMS = *roots[0].ExecutionTime, *roots[0].PlanningTime
	if math.IsNaN(executionMS) || math.IsInf(executionMS, 0) || executionMS < 0 ||
		math.IsNaN(planningMS) || math.IsInf(planningMS, 0) || planningMS < 0 {
		err = errors.New("EXPLAIN ANALYZE contiene tiempos inválidos")
		return
	}
	plan, ginUsed, err = parsePlan(raw)
	return
}

func pgServerSample(ctx context.Context, conn *sql.Conn, executeSQL string) (executionMS, planningMS float64, plan string, ginUsed bool, err error) {
	var raw []byte
	err = conn.QueryRowContext(ctx, "EXPLAIN (ANALYZE, BUFFERS, TIMING OFF, FORMAT JSON) "+executeSQL).Scan(&raw)
	if err != nil {
		return
	}
	return readExplainTiming(raw)
}

// Unicamente estas tres configuraciones llegan a SET; nunca se inyecta SQL libre.
const bd2PreparedName = "bd2_rank_benchmark"

func validPlanMode(mode string) bool {
	switch mode {
	case "auto", "force_custom_plan", "force_generic_plan":
		return true
	}
	return false
}

// SQL PREPARE/EXECUTE de PostgreSQL en una sesion fija. format(%I,%L) cita
// correctamente tanto el nombre como el tsquery (no interpolar entrada SQL).
func makeExecuteSQL(ctx context.Context, conn *sql.Conn, tsquery string, k int) (string, error) {
	var executeSQL string
	err := conn.QueryRowContext(ctx,
		"SELECT format('EXECUTE %I(%L::tsquery, %s)', $1::text, $2::text, $3::integer)",
		bd2PreparedName, tsquery, k).Scan(&executeSQL)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(executeSQL, "EXECUTE "+bd2PreparedName+"(") {
		return "", errors.New("SQL EXECUTE producido por PostgreSQL inesperado")
	}
	return executeSQL, nil
}

func readPreparedCounters(ctx context.Context, conn *sql.Conn) (custom, generic int64, err error) {
	err = conn.QueryRowContext(ctx,
		"SELECT custom_plans, generic_plans FROM pg_prepared_statements WHERE name=$1",
		bd2PreparedName).Scan(&custom, &generic)
	return
}

func planUsage(custom, generic int64) string {
	if custom < 0 || generic < 0 {
		return "invalid"
	}
	switch {
	case generic > 0 && custom > 0:
		return "mixed"
	case generic > 0:
		return "generic_only"
	case custom > 0:
		return "custom_only"
	default:
		return "none"
	}
}

func doSearch(ctx context.Context, stmt *sql.Stmt, k int) ([]hit, error) {
	rows, err := stmt.QueryContext(ctx)
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

var header = []string{
	"n_documents", "corpus_sha256", "method", "query_id", "query_text", "raw_query_terms", "normalized_query_terms", "query_bucket", "top_k", "warmup", "repeats",
	"preprocess_documents_ms", "spimi_build_ms", "spimi_open_ms", "spimi_index_bytes", "index_terms", "index_postings",
	"query_median_ms", "query_p95_ms", "query_mean_ms", "retrieved_ids", "relevant_in_subset", "precision_at_k", "recall_at_k", "latencies_ms",
	"go_version", "goos", "num_cpu", "gomaxprocs", "bm25_k1", "bm25_b", "timing_scope", "index_source",
	"batch_size", "target_sample_ms", "query_median_us", "query_p95_us", "batch_durations_ms",
	"postgres_gin_bytes", "postgres_table_bytes", "postgres_total_bytes", "postgres_version", "plan_nodes", "gin_used", "postgres_fts_config", "postgres_query_semantics",
	"ranking_model", "prepared_tsquery", "server_execution_median_ms", "server_execution_p95_ms", "server_planning_median_ms",
	"server_execution_samples_ms", "server_planning_samples_ms", "server_plan_nodes", "server_gin_used", "server_timing_scope",
	"plan_cache_mode", "measured_custom_plans", "measured_generic_plans", "measured_plan_choice", "prepared_plan_scope",
	"preprocess_pg_client_median_ms", "preprocess_pg_client_samples_ms",
	"profile_pg_json", "pg_select1_baseline_samples_ms", "pg_open_db_ms",
}

func run(cfg settings) error {
	if cfg.planMode == "" {
		cfg.planMode = "auto"
	}
	if !validPlanMode(cfg.planMode) {
		return fmt.Errorf("plan-mode invalido %q", cfg.planMode)
	}
	if cfg.prepRepeats == 0 {
		cfg.prepRepeats = 10
	}
	if cfg.prepRepeats < 1 || cfg.prepRepeats > 1000 {
		return errors.New("prep-repeats debe estar en [1,1000]")
	}
	if cfg.ranking == "" {
		cfg.ranking = "ts_rank"
	}
	if cfg.serverRepeats == 0 {
		cfg.serverRepeats = 5
	}
	searchSQL, err := rankingQuerySQL(cfg.ranking)
	if err != nil {
		return err
	}
	if cfg.serverRepeats < 1 || cfg.serverRepeats > 50 {
		return errors.New("server-repeats debe estar en [1,50]")
	}
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
	openStart := benchclock.Now()
	db, err := openDB(ctx)
	openDBMS := float64(benchclock.Since(openStart).Nanoseconds()) / 1e6
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
	// Conservar la misma sesion desde PREPARE hasta EXPLAIN EXECUTE.
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "SET plan_cache_mode = "+cfg.planMode); err != nil {
		return err
	}
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
	baseline, e := finalPGBaseline(ctx, conn)
	if e != nil {
		out.Close()
		return e
	}
	for _, q := range qs {
		// Preparar tsquery fuera del cronometro y crear SQL PREPARE solo para esta consulta.
		prepSamples := make([]float64, 0, cfg.prepRepeats)
		var tsquery string
		for i := 0; i < cfg.prepRepeats; i++ {
			t0 := benchclock.Now()
			prepared, e := prepareQuery(ctx, conn, q.Expression)
			if e != nil {
				out.Close()
				return fmt.Errorf("preparar tsquery %q: %w", q.ID, e)
			}
			prepSamples = append(prepSamples, float64(benchclock.Since(t0).Nanoseconds())/1e6)
			if i > 0 && prepared != tsquery {
				out.Close()
				return fmt.Errorf("tsquery inestable en %s", q.ID)
			}
			tsquery = prepared
		}
		if _, err = conn.ExecContext(ctx, "PREPARE "+bd2PreparedName+"(tsquery,integer) AS "+searchSQL); err != nil {
			out.Close()
			return fmt.Errorf("SQL PREPARE %s: %w", q.ID, err)
		}
		executeSQL, e := makeExecuteSQL(ctx, conn, tsquery, cfg.topK)
		if e != nil {
			out.Close()
			return fmt.Errorf("crear SQL EXECUTE: %w", e)
		}
		stmt, e := conn.PrepareContext(ctx, executeSQL)
		if e != nil {
			out.Close()
			return fmt.Errorf("preparar SQL EXECUTE: %w", e)
		}
		call := func() ([]hit, error) { return doSearch(ctx, stmt, cfg.topK) }
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
		customBefore, genericBefore, e := readPreparedCounters(ctx, conn)
		if e != nil {
			out.Close()
			return fmt.Errorf("contadores iniciales: %w", e)
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
		// Contar solo las ejecuciones cronometradas, sin calentamiento.
		customAfter, genericAfter, e := readPreparedCounters(ctx, conn)
		if e != nil {
			out.Close()
			return fmt.Errorf("contadores finales: %w", e)
		}
		measuredCustom := customAfter - customBefore
		measuredGeneric := genericAfter - genericBefore
		if measuredCustom < 0 || measuredGeneric < 0 {
			out.Close()
			return errors.New("contadores de planes retrocedieron")
		}
		if e = stmt.Close(); e != nil {
			out.Close()
			return e
		}

		// EXPLAIN del MISMO SQL PREPARE y en la MISMA sesion.
		// Se captura DESPUES de medir para no alterar los contadores medidos.
		plan, ginUsed, e := pgPlan(ctx, conn, executeSQL)
		if e != nil {
			out.Close()
			return fmt.Errorf("EXPLAIN EXECUTE: %w", e)
		}
		// La medición interna NO incluye la ida y vuelta del cliente Go ni el
		// preprocesamiento del tsquery. Es una ejecución diferente a la normal,
		// instrumentada por PostgreSQL (EXPLAIN ANALYZE EXECUTE TIMING OFF).
		serverTimes := make([]float64, 0, cfg.serverRepeats)
		planningTimes := make([]float64, 0, cfg.serverRepeats)
		serverPlan := ""
		serverGIN := false
		for i := 0; i < cfg.serverRepeats; i++ {
			execMS, planMS, nodePlan, used, e := pgServerSample(ctx, conn, executeSQL)
			if e != nil {
				out.Close()
				return fmt.Errorf("EXPLAIN ANALYZE consulta %q: %w", q.ID, e)
			}
			serverTimes = append(serverTimes, execMS)
			planningTimes = append(planningTimes, planMS)
			if i == 0 {
				serverPlan, serverGIN = nodePlan, used
			}
		}
		if measuredCustom+measuredGeneric != int64(size)*int64(cfg.repeats) {
			out.Close()
			return fmt.Errorf("contadores no coinciden con ejecuciones medidas")
		}
		profiles, e := finalPGProfiles(ctx, conn, executeSQL, cfg.serverRepeats)
		if e != nil {
			out.Close()
			return fmt.Errorf("perfil PG %s: %w", q.ID, e)
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
		serverJSON, _ := json.Marshal(serverTimes)
		planningJSON, _ := json.Marshal(planningTimes)
		prepJSON, _ := json.Marshal(prepSamples)
		sum := 0.0
		for _, v := range times {
			sum += v
		}
		row := []string{
			strconv.Itoa(m.Documents), m.CorpusSHA256, "pg_gin", q.ID, q.Raw, strconv.Itoa(q.RawWords), "", queryBucket(q.RawWords),
			strconv.Itoa(cfg.topK), strconv.Itoa(cfg.warmup), strconv.Itoa(cfg.repeats),
			"", "", "", "", "", "", // SPIMI metadata no aplica a PostgreSQL
			fmt.Sprintf("%.9f", median(times)), fmt.Sprintf("%.9f", p95(times)), fmt.Sprintf("%.9f", sum/float64(len(times))),
			strings.Join(retrieved, "|"), countText, p, r, string(latJSON), runtime.Version(), runtime.GOOS, strconv.Itoa(runtime.NumCPU()), strconv.Itoa(runtime.GOMAXPROCS(0)),
			"", "", "hot_batched_sql_prepare_execute_same_session", "postgres_gin_spanish", strconv.Itoa(size), strconv.Itoa(cfg.targetMS),
			fmt.Sprintf("%.6f", median(times)*1000), fmt.Sprintf("%.6f", p95(times)*1000), string(batchJSON),
			strconv.FormatInt(ginBytes, 10), strconv.FormatInt(tableBytes, 10), strconv.FormatInt(totalBytes, 10), pgVersion, plan, strconv.FormatBool(ginUsed), "spanish", "OR websearch_to_tsquery(spanish) preparado fuera del cronometro",
			cfg.ranking, tsquery, fmt.Sprintf("%.9f", median(serverTimes)), fmt.Sprintf("%.9f", p95(serverTimes)),
			fmt.Sprintf("%.9f", median(planningTimes)), string(serverJSON), string(planningJSON),
			serverPlan, strconv.FormatBool(serverGIN), "postgres_explain_analyze_execute_timing_off_execution_time",
			cfg.planMode, strconv.FormatInt(measuredCustom, 10), strconv.FormatInt(measuredGeneric, 10),
			planUsage(measuredCustom, measuredGeneric), "explain_execute_after_measured_batches_same_session",
			fmt.Sprintf("%.9f", median(prepSamples)), string(prepJSON), string(profiles), string(baseline), fmt.Sprintf("%.9f", openDBMS),
		}
		if len(row) != len(header) {
			out.Close()
			return fmt.Errorf("CSV: %d valores contra %d columnas", len(row), len(header))
		}
		if err = cw.Write(row); err != nil {
			out.Close()
			return err
		}
		fmt.Fprintf(os.Stderr, "q=%s | %s | mode=%s | medidos=custom:%d generic:%d | Go-RT=%.3f ms | PG-Execution=%.3f ms | EXPLAIN GIN=%t | batch=%d\n", q.ID, cfg.ranking, cfg.planMode, measuredCustom, measuredGeneric, median(times), median(serverTimes), serverGIN, size)
		if _, err = conn.ExecContext(ctx, "DEALLOCATE "+bd2PreparedName); err != nil {
			out.Close()
			return err
		}
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
