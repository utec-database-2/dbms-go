// Endpoints de búsqueda textual (Parte 3) y por similitud vectorial (Parte 4)
// para el frontend. Usan las piezas del motor tal cual están en dbms/lib:
//
//   - Texto: text.PreprocessText (stopwords + stemming), el índice invertido
//     SPIMI (index/text/spimi) y el ranking TF-IDF + coseno o BM25
//     (index/text/ranking).
//   - Vectores: búsqueda secuencial exacta, IVF (index/ivf) y HNSW
//     (index/hnsw) con distancia euclidiana, coseno o producto punto.
//
// Los índices se construyen la primera vez que se consulta una columna y se
// cachean; se descartan cuando una sentencia o una importación toca la tabla.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/dbms-go/v2/dbms/lib/index/hnsw"
	"github.com/dbms-go/v2/dbms/lib/index/ivf"
	"github.com/dbms-go/v2/dbms/lib/index/text/ranking"
	"github.com/dbms-go/v2/dbms/lib/index/text/spimi"
	"github.com/dbms-go/v2/dbms/lib/sql"
	text "github.com/dbms-go/v2/dbms/lib/text"
	"github.com/dbms-go/v2/dbms/lib/vector"
	metricpkg "github.com/dbms-go/v2/dbms/lib/vector/metric"
)

const (
	defaultSearchK = 10
	maxSearchK     = 200
	// maxListRows acota cuántas filas devuelve /api/vector/items.
	maxListRows = 2000
	// maxTermBytes descarta tokens anómalos (el builder SPIMI los rechaza).
	maxTermBytes = 128
)

// ms convierte una duración a milisegundos con decimales.
func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// ---------------------------------------------------------------------------
// Caché de índices
// ---------------------------------------------------------------------------

type searchCache struct {
	mu   sync.Mutex
	root string
	text map[string]*textEntry
	vec  map[string]*vecEntry
}

func newSearchCache() (*searchCache, error) {
	root, err := os.MkdirTemp("", "minigestor-busqueda-*")
	if err != nil {
		return nil, err
	}
	return &searchCache{root: root, text: map[string]*textEntry{}, vec: map[string]*vecEntry{}}, nil
}

func cacheKey(table, column string) string {
	return strings.ToLower(table) + "|" + strings.ToLower(column)
}

// invalidate descarta los índices construidos sobre la tabla.
func (c *searchCache) invalidate(table string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prefix := strings.ToLower(table) + "|"
	for k, e := range c.text {
		if strings.HasPrefix(k, prefix) {
			e.close()
			delete(c.text, k)
		}
	}
	for k, e := range c.vec {
		if strings.HasPrefix(k, prefix) {
			e.close()
			delete(c.vec, k)
		}
	}
}

// invalidateTouched descarta los índices de toda tabla que el script modifica.
func (c *searchCache) invalidateTouched(eng *sql.Engine, script string) {
	c.mu.Lock()
	tables := map[string]bool{}
	for k := range c.text {
		tables[strings.SplitN(k, "|", 2)[0]] = true
	}
	for k := range c.vec {
		tables[strings.SplitN(k, "|", 2)[0]] = true
	}
	c.mu.Unlock()
	for t := range tables {
		if touches, err := eng.ScriptTouches(script, t); err != nil || touches {
			c.invalidate(t)
		}
	}
}

func (c *searchCache) close() {
	c.mu.Lock()
	for _, e := range c.text {
		e.close()
	}
	for _, e := range c.vec {
		e.close()
	}
	c.mu.Unlock()
	_ = os.RemoveAll(c.root)
}

// snapshot lee la tabla completa con el motor SQL y devuelve también una
// firma del archivo para detectar cambios hechos por fuera del servidor.
func snapshot(eng *sql.Engine, table string) (cols []string, rows [][]any, sig string, err error) {
	sig, ok := tableSignature(eng, table)
	if !ok {
		return nil, nil, "", fmt.Errorf("la tabla %q no existe", table)
	}
	res, err := eng.Exec("SELECT * FROM " + table + ";")
	if err != nil {
		return nil, nil, "", err
	}
	return res.Columns, res.Rows, sig, nil
}

func tableSignature(eng *sql.Engine, table string) (string, bool) {
	for _, f := range eng.Files() {
		if strings.EqualFold(f.Name, table) {
			return fmt.Sprintf("%d/%d/%d/%d", f.Rows, f.TotalSize, f.DeadSlots, f.AuxSlots), true
		}
	}
	return "", false
}

func columnIndex(cols []string, name string) (int, bool) {
	for i, c := range cols {
		if strings.EqualFold(c, name) {
			return i, true
		}
	}
	return 0, false
}

func clampK(k int) int {
	if k <= 0 {
		return defaultSearchK
	}
	if k > maxSearchK {
		return maxSearchK
	}
	return k
}

// ---------------------------------------------------------------------------
// Parte 3: búsqueda textual con SPIMI + TF-IDF / BM25
// ---------------------------------------------------------------------------

type textEntry struct {
	sig     string
	columns []string
	rows    [][]any
	col     int
	dir     string
	reader  *spimi.Reader
	stats   spimi.BuildStats
	buildMs float64
	prepMs  float64
}

func (e *textEntry) close() {
	if e.reader != nil {
		_ = e.reader.Close()
	}
	if e.dir != "" {
		_ = os.RemoveAll(e.dir)
	}
}

func cleanTokens(tokens []string) []string {
	out := tokens[:0]
	for _, t := range tokens {
		if t != "" && len(t) <= maxTermBytes {
			out = append(out, t)
		}
	}
	return out
}

// textIndex devuelve el índice SPIMI de la columna, construyéndolo si hace
// falta. El llamador debe tener c.mu tomado mientras use el índice.
func (c *searchCache) textIndex(eng *sql.Engine, table, column string) (*textEntry, bool, error) {
	key := cacheKey(table, column)
	if e, ok := c.text[key]; ok {
		if sig, ok := tableSignature(eng, table); ok && sig == e.sig {
			return e, true, nil
		}
		e.close()
		delete(c.text, key)
	}

	cols, rows, sig, err := snapshot(eng, table)
	if err != nil {
		return nil, false, err
	}
	col, ok := columnIndex(cols, column)
	if !ok {
		return nil, false, fmt.Errorf("la tabla %q no tiene la columna %q", table, column)
	}

	// Preprocesamiento (tokenización, stopwords y stemming) de todos los
	// documentos; luego el builder SPIMI arma el índice en bloques.
	t0 := time.Now()
	docs := make([][]string, len(rows))
	for i, row := range rows {
		if col >= len(row) {
			continue
		}
		s, _ := row[col].(string)
		tokens, err := text.PreprocessText(s)
		if err != nil {
			return nil, false, fmt.Errorf("preprocesando la fila %d: %w", i+1, err)
		}
		docs[i] = cleanTokens(tokens)
	}
	prep := time.Since(t0)

	dir, err := os.MkdirTemp(c.root, "spimi-"+strings.ToLower(table)+"-*")
	if err != nil {
		return nil, false, err
	}
	t1 := time.Now()
	b, err := spimi.NewBuilder(spimi.Options{Dir: dir})
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, false, err
	}
	for _, toks := range docs {
		if _, err := b.AddDocument(toks); err != nil {
			_ = b.Close()
			_ = os.RemoveAll(dir)
			return nil, false, err
		}
	}
	stats, err := b.Finish()
	_ = b.Close()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, false, err
	}
	reader, err := spimi.Open(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, false, err
	}
	e := &textEntry{
		sig: sig, columns: cols, rows: rows, col: col, dir: dir, reader: reader,
		stats: stats, buildMs: ms(time.Since(t1)), prepMs: ms(prep),
	}
	c.text[key] = e
	return e, false, nil
}

type textSearchRequest struct {
	Table   string   `json:"table"`
	Column  string   `json:"column"`
	Query   string   `json:"query"`
	Methods []string `json:"methods"`
	K       int      `json:"k"`
	K1      float64  `json:"k1"`
	B       float64  `json:"b"`
}

type textHit struct {
	Doc     uint32   `json:"doc"`
	Score   float64  `json:"score"`
	Row     []any    `json:"row"`
	Matched []string `json:"matched"`
}

type textMethodResult struct {
	Method    string    `json:"method"`
	Label     string    `json:"label"`
	ElapsedMs float64   `json:"elapsedMs"`
	Hits      []textHit `json:"hits"`
	Steps     []string  `json:"steps"`
	Error     string    `json:"error,omitempty"`
}

type textIndexInfo struct {
	Docs        int     `json:"docs"`
	Terms       int     `json:"terms"`
	Postings    int64   `json:"postings"`
	AvgDocLen   float64 `json:"avgDocLen"`
	Blocks      int     `json:"blocks"`
	MergeRounds int     `json:"mergeRounds"`
	Bytes       int64   `json:"bytes"`
	BuildMs     float64 `json:"buildMs"`
	PrepMs      float64 `json:"prepMs"`
	Cached      bool    `json:"cached"`
}

// matchedWords devuelve las palabras originales del documento cuyo stem
// coincide con algún término de la consulta (para resaltarlas en pantalla).
func matchedWords(doc string, terms map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range strings.FieldsFunc(doc, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		lw := strings.ToLower(w)
		if seen[lw] {
			continue
		}
		seen[lw] = true
		toks, err := text.PreprocessText(lw)
		if err != nil {
			continue
		}
		for _, t := range toks {
			if terms[t] {
				out = append(out, lw)
				break
			}
		}
	}
	return out
}

// POST /api/text/search
func handleTextSearch(db *database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req textSearchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("body inválido: %w", err))
			return
		}
		if req.Table == "" || req.Column == "" {
			writeError(w, http.StatusBadRequest, errors.New("faltan table y column"))
			return
		}
		if strings.TrimSpace(req.Query) == "" {
			writeError(w, http.StatusBadRequest, errors.New("escribe una consulta"))
			return
		}
		k := clampK(req.K)
		if len(req.Methods) == 0 {
			req.Methods = []string{"tfidf", "bm25"}
		}
		k1, b := req.K1, req.B
		if k1 <= 0 {
			k1 = ranking.DefaultBM25K1
		}
		if b < 0 || b > 1 || (b == 0 && req.K1 == 0) {
			b = ranking.DefaultBM25B
		}

		db.search.mu.Lock()
		defer db.search.mu.Unlock()
		entry, cached, err := db.search.textIndex(db.eng, req.Table, req.Column)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		t0 := time.Now()
		tokens, err := text.PreprocessText(req.Query)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		tokens = cleanTokens(tokens)
		prepQuery := time.Since(t0)
		terms := map[string]bool{}
		for _, t := range tokens {
			terms[t] = true
		}

		// Frecuencia de documento de cada término de la consulta.
		df := make(map[string]int, len(tokens))
		for _, t := range tokens {
			df[t] = entry.reader.DF(t)
		}

		results := make([]textMethodResult, 0, len(req.Methods))
		for _, m := range req.Methods {
			res := textMethodResult{Method: strings.ToLower(m)}
			var method ranking.Method
			switch res.Method {
			case "tfidf", "tf_idf", "tf-idf":
				res.Method, res.Label, method = "tfidf", "TF-IDF + coseno", ranking.MethodTFIDF
			case "bm25":
				res.Method, res.Label, method = "bm25", "BM25", ranking.MethodBM25
			default:
				res.Error = fmt.Sprintf("técnica desconocida %q (usa tfidf o bm25)", m)
				results = append(results, res)
				continue
			}

			start := time.Now()
			hits, err := ranking.Search(entry.reader, method, tokens, ranking.SearchOptions{TopK: k, BM25K1: k1, BM25B: b})
			res.ElapsedMs = ms(time.Since(start) + prepQuery)
			if err != nil {
				res.Error = err.Error()
				results = append(results, res)
				continue
			}

			res.Hits = make([]textHit, 0, len(hits))
			for _, h := range hits {
				if int(h.DocID) >= len(entry.rows) {
					continue
				}
				row := entry.rows[h.DocID]
				doc, _ := row[entry.col].(string)
				res.Hits = append(res.Hits, textHit{
					Doc: h.DocID, Score: h.Score, Row: row, Matched: matchedWords(doc, terms),
				})
			}

			res.Steps = []string{
				fmt.Sprintf("Preprocesamiento de la consulta: %q → %v (minúsculas, sin stopwords, stemming)", req.Query, tokens),
				fmt.Sprintf("Índice invertido SPIMI sobre %s.%s: %d documentos, %d términos, %d bloque(s)",
					req.Table, req.Column, entry.reader.NumDocs(), entry.reader.NumTerms(), entry.stats.Blocks),
				fmt.Sprintf("Listas de posteo leídas: %s", formatDF(tokens, df)),
			}
			if method == ranking.MethodBM25 {
				res.Steps = append(res.Steps, fmt.Sprintf("Puntuación BM25 con k1=%.2f, b=%.2f, avgdl=%.1f", k1, b, entry.reader.AvgDocLen()))
			} else {
				res.Steps = append(res.Steps, "Puntuación TF-IDF: producto de pesos tf·idf normalizado por la norma del documento (coseno)")
			}
			res.Steps = append(res.Steps, fmt.Sprintf("Top-%d con un min-heap: %d resultado(s)", k, len(res.Hits)))
			results = append(results, res)
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"table":   req.Table,
			"column":  req.Column,
			"columns": entry.columns,
			"tokens":  tokens,
			"df":      df,
			"index": textIndexInfo{
				Docs: entry.reader.NumDocs(), Terms: entry.reader.NumTerms(), Postings: entry.stats.NumPostings,
				AvgDocLen: entry.reader.AvgDocLen(), Blocks: entry.stats.Blocks, MergeRounds: entry.stats.MergeRounds,
				Bytes: entry.stats.IndexBytes, BuildMs: entry.buildMs, PrepMs: entry.prepMs, Cached: cached,
			},
			"results": results,
		})
	}
}

func formatDF(tokens []string, df map[string]int) string {
	if len(tokens) == 0 {
		return "ninguna (la consulta quedó vacía tras quitar stopwords)"
	}
	parts := make([]string, 0, len(tokens))
	seen := map[string]bool{}
	for _, t := range tokens {
		if seen[t] {
			continue
		}
		seen[t] = true
		parts = append(parts, fmt.Sprintf("%s (df=%d)", t, df[t]))
	}
	return strings.Join(parts, ", ")
}

// ---------------------------------------------------------------------------
// Parte 4: búsqueda por similitud con IVF, HNSW y secuencial
// ---------------------------------------------------------------------------

type builtIVF struct {
	idx     *ivf.IVFFlatIndex
	nlist   int
	buildMs float64
	err     error
}

type builtHNSW struct {
	idx     *hnsw.HNSWIndex
	buildMs float64
	err     error
}

type vecEntry struct {
	sig     string
	columns []string
	rows    [][]any
	col     int
	dim     int
	ids     []int // fila de cada vector
	vecs    [][]float32
	dir     string
	ivf     map[string]*builtIVF
	hnsw    map[string]*builtHNSW
}

func (e *vecEntry) close() {
	for _, h := range e.hnsw {
		if h.idx != nil {
			_ = h.idx.Close()
		}
	}
	if e.dir != "" {
		_ = os.RemoveAll(e.dir)
	}
}

func asVector(v any) ([]float32, bool) {
	switch x := v.(type) {
	case []float32:
		return x, true
	case []float64:
		return vector.Float64ToFloat32(x), true
	}
	return nil, false
}

func (c *searchCache) vecIndex(eng *sql.Engine, table, column string) (*vecEntry, error) {
	key := cacheKey(table, column)
	if e, ok := c.vec[key]; ok {
		if sig, ok := tableSignature(eng, table); ok && sig == e.sig {
			return e, nil
		}
		e.close()
		delete(c.vec, key)
	}
	cols, rows, sig, err := snapshot(eng, table)
	if err != nil {
		return nil, err
	}
	col, ok := columnIndex(cols, column)
	if !ok {
		return nil, fmt.Errorf("la tabla %q no tiene la columna %q", table, column)
	}
	e := &vecEntry{sig: sig, columns: cols, rows: rows, col: col, ivf: map[string]*builtIVF{}, hnsw: map[string]*builtHNSW{}}
	for i, row := range rows {
		if col >= len(row) {
			continue
		}
		v, ok := asVector(row[col])
		if !ok || len(v) == 0 {
			continue
		}
		if e.dim == 0 {
			e.dim = len(v)
		}
		if len(v) != e.dim {
			continue
		}
		e.ids = append(e.ids, i)
		e.vecs = append(e.vecs, v)
	}
	if len(e.vecs) == 0 {
		return nil, fmt.Errorf("la columna %s.%s no tiene vectores", table, column)
	}
	e.dir, err = os.MkdirTemp(c.root, "vec-"+strings.ToLower(table)+"-*")
	if err != nil {
		return nil, err
	}
	c.vec[key] = e
	return e, nil
}

func defaultNListFor(n int) int {
	nl := int(math.Round(math.Sqrt(float64(n))))
	if nl < 1 {
		nl = 1
	}
	if nl > n {
		nl = n
	}
	return nl
}

func (e *vecEntry) ivfFor(m metricpkg.Metric, nlist int) *builtIVF {
	key := fmt.Sprintf("%d|%d", m, nlist)
	if b, ok := e.ivf[key]; ok {
		return b
	}
	b := &builtIVF{nlist: nlist}
	t0 := time.Now()
	idx, err := ivf.NewIVFFlatIndex(e.dim, nlist, m)
	if err == nil {
		err = idx.Train(e.vecs)
	}
	if err == nil {
		for i, v := range e.vecs {
			if err = idx.Insert(uint64(i), v); err != nil {
				break
			}
		}
	}
	b.idx, b.err, b.buildMs = idx, err, ms(time.Since(t0))
	e.ivf[key] = b
	return b
}

func hnswMetric(m metricpkg.Metric) hnsw.Metric {
	switch m {
	case metricpkg.Cosine:
		return hnsw.Cosine
	case metricpkg.DotProduct:
		return hnsw.ProductDot
	}
	return hnsw.Euclidean
}

func (e *vecEntry) hnswFor(m metricpkg.Metric) *builtHNSW {
	key := fmt.Sprint(m)
	if b, ok := e.hnsw[key]; ok {
		return b
	}
	b := &builtHNSW{}
	t0 := time.Now()
	idx, err := hnsw.OpenWithConfig(filepath.Join(e.dir, fmt.Sprintf("hnsw-%s.bin", m)), hnsw.DefaultIndexConfig(hnswMetric(m)))
	if err == nil {
		for i, v := range e.vecs {
			// Los ID del grafo empiezan en 1.
			if err = idx.Add(uint64(i+1), v); err != nil {
				break
			}
		}
	}
	b.idx, b.err, b.buildMs = idx, err, ms(time.Since(t0))
	e.hnsw[key] = b
	return b
}

type vectorSearchRequest struct {
	Table    string    `json:"table"`
	Column   string    `json:"column"`
	Vector   []float32 `json:"vector"`
	Row      *int      `json:"row"`
	Methods  []string  `json:"methods"`
	Metric   string    `json:"metric"`
	K        int       `json:"k"`
	NList    int       `json:"nlist"`
	NProbe   int       `json:"nprobe"`
	EfSearch int       `json:"efSearch"`
}

type vectorHit struct {
	Row      int       `json:"row"`
	Distance float64   `json:"distance"`
	Score    float64   `json:"score"`
	Values   []any     `json:"values"`
	Vector   []float32 `json:"vector"`
}

type vectorMethodResult struct {
	Method    string      `json:"method"`
	Label     string      `json:"label"`
	ElapsedMs float64     `json:"elapsedMs"`
	BuildMs   float64     `json:"buildMs"`
	Recall    float64     `json:"recall"`
	Hits      []vectorHit `json:"hits"`
	Steps     []string    `json:"steps"`
	Error     string      `json:"error,omitempty"`
}

// similarityScore convierte una distancia en un puntaje "más alto = más
// parecido" para mostrar: coseno → 1−d, producto punto → el producto,
// euclidiana → 1/(1+d).
func similarityScore(d float64, m metricpkg.Metric) float64 {
	switch m {
	case metricpkg.Cosine:
		return 1 - d
	case metricpkg.DotProduct:
		return -d
	}
	return 1 / (1 + d)
}

// rowWithoutVector copia la fila sin la columna vectorial (para no repetirla).
func rowWithoutVector(row []any, col int) []any {
	out := make([]any, len(row))
	copy(out, row)
	if col < len(out) {
		out[col] = nil
	}
	return out
}

func parseMetricName(s string) (metricpkg.Metric, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "cosine", "coseno", "cos":
		return metricpkg.Cosine, nil
	case "euclidean", "euclidiana", "l2":
		return metricpkg.Euclidean, nil
	case "dot", "dot_product", "producto_punto", "ip":
		return metricpkg.DotProduct, nil
	}
	return 0, fmt.Errorf("métrica desconocida %q (usa euclidean, cosine o dot)", s)
}

var metricLabels = map[metricpkg.Metric]string{
	metricpkg.Euclidean:  "distancia euclidiana",
	metricpkg.Cosine:     "similitud del coseno",
	metricpkg.DotProduct: "producto punto",
}

// POST /api/vector/search
func handleVectorSearch(db *database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req vectorSearchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("body inválido: %w", err))
			return
		}
		if req.Table == "" || req.Column == "" {
			writeError(w, http.StatusBadRequest, errors.New("faltan table y column"))
			return
		}
		metric, err := parseMetricName(req.Metric)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		k := clampK(req.K)
		if len(req.Methods) == 0 {
			req.Methods = []string{"hnsw", "ivf", "seq"}
		}

		c := db.search
		c.mu.Lock()
		defer c.mu.Unlock()
		e, err := c.vecIndex(db.eng, req.Table, req.Column)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		query := req.Vector
		if req.Row != nil {
			if *req.Row < 0 || *req.Row >= len(e.rows) {
				writeError(w, http.StatusBadRequest, fmt.Errorf("la fila %d no existe", *req.Row))
				return
			}
			v, ok := asVector(e.rows[*req.Row][e.col])
			if !ok {
				writeError(w, http.StatusBadRequest, fmt.Errorf("la fila %d no tiene vector", *req.Row))
				return
			}
			query = v
		}
		if len(query) != e.dim {
			writeError(w, http.StatusBadRequest, fmt.Errorf("la consulta tiene %d dimensiones y la columna %d", len(query), e.dim))
			return
		}
		if k > len(e.vecs) {
			k = len(e.vecs)
		}

		toHit := func(i int) vectorHit {
			d := metricpkg.Distance(query, e.vecs[i], metric)
			row := e.ids[i]
			return vectorHit{
				Row: row, Distance: d, Score: similarityScore(d, metric),
				Values: rowWithoutVector(e.rows[row], e.col), Vector: e.vecs[i],
			}
		}

		// La búsqueda secuencial exacta es la referencia para Recall@k.
		seqStart := time.Now()
		order := make([]int, len(e.vecs))
		dists := make([]float64, len(e.vecs))
		for i := range e.vecs {
			order[i] = i
			dists[i] = metricpkg.RankingDistance(query, e.vecs[i], metric)
		}
		sort.SliceStable(order, func(a, b int) bool { return dists[order[a]] < dists[order[b]] })
		exact := order[:k]
		seqMs := ms(time.Since(seqStart))
		truth := make(map[int]bool, k)
		for _, i := range exact {
			truth[i] = true
		}
		recall := func(found []int) float64 {
			if k == 0 {
				return 1
			}
			n := 0
			for _, i := range found {
				if truth[i] {
					n++
				}
			}
			return float64(n) / float64(k)
		}

		results := make([]vectorMethodResult, 0, len(req.Methods))
		for _, m := range req.Methods {
			res := vectorMethodResult{Method: strings.ToLower(m)}
			var found []int
			switch res.Method {
			case "seq", "secuencial", "scan":
				res.Method, res.Label = "seq", "Secuencial (exacto)"
				found = exact
				res.ElapsedMs = seqMs
				res.Steps = []string{
					fmt.Sprintf("Recorrido secuencial de %d vectores de %d dimensiones", len(e.vecs), e.dim),
					fmt.Sprintf("Distancia: %s para cada vector y orden total", metricLabels[metric]),
					fmt.Sprintf("Top-%d exacto (referencia para Recall@%d)", k, k),
				}

			case "ivf":
				res.Label = "IVF"
				nlist := req.NList
				if nlist <= 0 {
					nlist = defaultNListFor(len(e.vecs))
				}
				if nlist > len(e.vecs) {
					nlist = len(e.vecs)
				}
				nprobe := req.NProbe
				if nprobe <= 0 {
					nprobe = int(math.Max(1, math.Round(float64(nlist)/4)))
				}
				if nprobe > nlist {
					nprobe = nlist
				}
				if metric == metricpkg.DotProduct {
					res.Error = "IVF no puede entrenar sus centroides con producto punto; usa euclidiana o coseno"
					break
				}
				b := e.ivfFor(metric, nlist)
				res.BuildMs = b.buildMs
				if b.err != nil {
					res.Error = b.err.Error()
					break
				}
				start := time.Now()
				cands, err := b.idx.Query(query, k, nprobe)
				res.ElapsedMs = ms(time.Since(start))
				if err != nil {
					res.Error = err.Error()
					break
				}
				for _, cnd := range cands {
					found = append(found, int(cnd.ID))
				}
				res.Steps = []string{
					fmt.Sprintf("Entrenamiento k-means++ de %d centroides (nlist) sobre %d vectores", nlist, len(e.vecs)),
					fmt.Sprintf("Se eligen las %d lista(s) invertidas más cercanas a la consulta (nprobe)", nprobe),
					fmt.Sprintf("Distancia: %s solo dentro de esas listas (~%d%% del dataset)", metricLabels[metric], 100*nprobe/nlist),
					fmt.Sprintf("Top-%d con un max-heap: %d resultado(s)", k, len(found)),
				}

			case "hnsw":
				res.Label = "HNSW"
				b := e.hnswFor(metric)
				res.BuildMs = b.buildMs
				if b.err != nil {
					res.Error = b.err.Error()
					break
				}
				ef := req.EfSearch
				if ef <= 0 {
					ef = 64
				}
				start := time.Now()
				hits, err := b.idx.KNNSearch(query, k, ef)
				res.ElapsedMs = ms(time.Since(start))
				if err != nil {
					res.Error = err.Error()
					break
				}
				for _, h := range hits {
					found = append(found, int(h.ID)-1)
				}
				res.Steps = []string{
					fmt.Sprintf("Grafo jerárquico navegable con %d nodos (M=16, efConstruction=64)", b.idx.Len()),
					"Búsqueda voraz desde el nivel superior hasta la capa 0",
					fmt.Sprintf("Exploración con efSearch=%d candidatos usando %s", ef, metricLabels[metric]),
					fmt.Sprintf("Top-%d: %d resultado(s)", k, len(found)),
				}

			default:
				res.Error = fmt.Sprintf("índice desconocido %q (usa seq, ivf o hnsw)", m)
			}

			if res.Error == "" {
				res.Recall = recall(found)
				res.Hits = make([]vectorHit, 0, len(found))
				for _, i := range found {
					if i >= 0 && i < len(e.vecs) {
						res.Hits = append(res.Hits, toHit(i))
					}
				}
			}
			results = append(results, res)
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"table":   req.Table,
			"column":  req.Column,
			"columns": e.columns,
			"dim":     e.dim,
			"count":   len(e.vecs),
			"metric":  metric.String(),
			"k":       k,
			"query":   query,
			"results": results,
		})
	}
}

// POST /api/vector/items {table, column}: filas con vector (sin el vector),
// para elegir la consulta desde la galería.
func handleVectorItems(db *database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Table  string `json:"table"`
			Column string `json:"column"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("body inválido: %w", err))
			return
		}
		c := db.search
		c.mu.Lock()
		defer c.mu.Unlock()
		e, err := c.vecIndex(db.eng, req.Table, req.Column)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		n := len(e.ids)
		if n > maxListRows {
			n = maxListRows
		}
		items := make([]map[string]any, 0, n)
		for _, row := range e.ids[:n] {
			items = append(items, map[string]any{"row": row, "values": rowWithoutVector(e.rows[row], e.col)})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"table": req.Table, "column": req.Column, "columns": e.columns,
			"dim": e.dim, "count": len(e.vecs), "items": items,
		})
	}
}

// POST /api/vector/extract (multipart, campo "file"): extrae el vector de una
// imagen subida para usarla como consulta. Usa el histograma de color, que no
// necesita OpenCV; "dim" pide la dimensión de la columna (debe ser un cubo:
// 8, 27, 64, 125...).
func handleVectorExtract() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(16 << 20); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("form inválido: %w", err))
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("falta el archivo (campo \"file\"): %w", err))
			return
		}
		defer file.Close()
		raw, err := io.ReadAll(file)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		bins := 4
		if d := r.FormValue("dim"); d != "" {
			var dim int
			if _, err := fmt.Sscan(d, &dim); err == nil && dim > 0 {
				b := int(math.Round(math.Cbrt(float64(dim))))
				if b*b*b != dim {
					writeError(w, http.StatusBadRequest, fmt.Errorf(
						"la columna tiene %d dimensiones y el extractor de color produce b³ (8, 27, 64...); para otros vectores hace falta su propio extractor (SIFT/MFCC)", dim))
					return
				}
				bins = b
			}
		}
		ext := vector.ColorHistogram{Bins: bins}
		start := time.Now()
		vec, err := ext.Vector(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("%s: %w (formatos: PNG, JPEG, GIF)", header.Filename, err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"file":      header.Filename,
			"vector":    vec,
			"dim":       len(vec),
			"extractor": fmt.Sprintf("Histograma de color RGB %d×%d×%d (normalizado L2)", bins, bins, bins),
			"elapsedMs": ms(time.Since(start)),
		})
	}
}
