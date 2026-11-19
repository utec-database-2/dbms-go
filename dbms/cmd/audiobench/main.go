package main

import (
	"encoding/csv"
	"encoding/gob"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dbms-go/v2/dbms/lib/index/hnsw"
	"github.com/dbms-go/v2/dbms/lib/index/ivf"
	metricpkg "github.com/dbms-go/v2/dbms/lib/vector/metric"
)

type audioGobRecord struct {
	TrackID string
	Path    string
	Vector  []float64
}

type AudioRecord struct {
	TrackIDs []string
	Vectors  [][]float32
	Files    []string
	Genres   []string
}

type benchRow struct {
	Experiment string
	Metric     string
	N          int
	Method     string
	Params     string
	BuildMs    float64
	AvgQueryMs float64
	P95QueryMs float64
	Speedup    float64
	Recall5    float64
	Recall10   float64
	Recall20   float64
}

func loadAudioFeatures(path string) (*AudioRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var raw []audioGobRecord
	if err := gob.NewDecoder(f).Decode(&raw); err != nil {
		return nil, err
	}

	rec := &AudioRecord{
		TrackIDs: make([]string, len(raw)),
		Vectors:  make([][]float32, len(raw)),
		Files:    make([]string, len(raw)),
	}
	for i, r := range raw {
		rec.TrackIDs[i] = r.TrackID
		rec.Files[i] = r.Path
		vec := make([]float32, len(r.Vector))
		for j, v := range r.Vector {
			vec[j] = float32(v)
		}
		rec.Vectors[i] = vec
	}
	return rec, nil
}

func parseSizes(raw string) ([]int, error) {
	parts := strings.Split(raw, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := strconv.Atoi(p)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("tamaño inválido %q en -sizes", p)
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("debe indicar al menos un tamaño en -sizes")
	}
	return out, nil
}

// buildDatasetForN devuelve N vectores: si N <= len(base), usa los primeros N
// vectores reales; si N > len(base), conserva los vectores reales intactos y
// completa hasta N mediante bootstrap con ruido gaussiano controlado sobre las
// dimensiones activas BoAW+TF-IDF para evitar duplicados exactos.
func buildDatasetForN(base [][]float32, targetN int, seed int64) [][]float32 {
	if targetN <= len(base) {
		return base[:targetN]
	}
	rng := rand.New(rand.NewSource(seed))
	out := make([][]float32, targetN)
	copy(out, base)
	dim := len(base[0])

	for i := len(base); i < targetN; i++ {
		src := base[rng.Intn(len(base))]
		vec := make([]float32, dim)
		var normSq float64
		for j, v := range src {
			if v > 0 {
				nv := float64(v) * (1.0 + rng.NormFloat64()*0.03)
				if nv < 0 {
					nv = 0
				}
				vec[j] = float32(nv)
				normSq += nv * nv
			}
		}
		if normSq > 0 {
			invNorm := float32(1.0 / math.Sqrt(normSq))
			for j := range vec {
				vec[j] *= invNorm
			}
		}
		out[i] = vec
	}
	return out
}

func parseMetric(name string) (metricpkg.Metric, hnsw.Metric, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "cosine", "coseno":
		return metricpkg.Cosine, hnsw.Cosine, nil
	case "euclidean", "euclidiana", "l2":
		return metricpkg.Euclidean, hnsw.Euclidean, nil
	default:
		return 0, "", fmt.Errorf("métrica no soportada %q (usa cosine o euclidean)", name)
	}
}

func ms(d time.Duration) float64 {
	return float64(d.Nanoseconds()) / 1e6
}

func percentile95(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	cp := append([]float64(nil), vals...)
	sort.Float64s(cp)
	idx := int(math.Ceil(0.95*float64(len(cp)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	return cp[idx]
}

// exactTopK devuelve los k vecinos más cercanos en vecs[:n], excluyendo queryIdx.
func exactTopK(vecs [][]float32, n int, queryIdx int, k int, m metricpkg.Metric) ([]int, float64) {
	q := vecs[queryIdx]
	t0 := time.Now()
	type pair struct {
		id   int
		dist float64
	}
	cands := make([]pair, 0, n-1)
	for i := 0; i < n; i++ {
		if i == queryIdx {
			continue
		}
		cands = append(cands, pair{id: i, dist: metricpkg.RankingDistance(q, vecs[i], m)})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].dist == cands[j].dist {
			return cands[i].id < cands[j].id
		}
		return cands[i].dist < cands[j].dist
	})
	elapsed := ms(time.Since(t0))
	if k > len(cands) {
		k = len(cands)
	}
	out := make([]int, k)
	for i := 0; i < k; i++ {
		out[i] = cands[i].id
	}
	return out, elapsed
}

func computeRecall(truth []int, found []int, k int) float64 {
	if k > len(truth) {
		k = len(truth)
	}
	if k == 0 {
		return 1.0
	}
	truthSet := make(map[int]struct{}, k)
	for i := 0; i < k; i++ {
		truthSet[truth[i]] = struct{}{}
	}
	limit := k
	if limit > len(found) {
		limit = len(found)
	}
	hits := 0
	for i := 0; i < limit; i++ {
		if _, ok := truthSet[found[i]]; ok {
			hits++
		}
	}
	return float64(hits) / float64(k)
}

func sampleQueryIndices(realPoolN, numQueries int, seed int64) []int {
	if numQueries >= realPoolN {
		numQueries = realPoolN
	}
	rng := rand.New(rand.NewSource(seed))
	perm := rng.Perm(realPoolN)
	return perm[:numQueries]
}

func buildIVF(vecs [][]float32, dim, nlist int, m metricpkg.Metric) (*ivf.IVFFlatIndex, float64, error) {
	t0 := time.Now()
	idx, err := ivf.NewIVFFlatIndex(dim, nlist, m)
	if err != nil {
		return nil, 0, err
	}
	trainVecs := vecs
	maxTrain := nlist * 32
	if maxTrain < 10000 {
		maxTrain = 10000
	}
	if len(vecs) > maxTrain {
		rng := rand.New(rand.NewSource(42))
		perm := rng.Perm(len(vecs))
		trainVecs = make([][]float32, maxTrain)
		for i := 0; i < maxTrain; i++ {
			trainVecs[i] = vecs[perm[i]]
		}
	}
	if err := idx.Train(trainVecs); err != nil {
		return nil, 0, err
	}
	for i, v := range vecs {
		if err := idx.Insert(uint64(i), v); err != nil {
			return nil, 0, err
		}
	}
	return idx, ms(time.Since(t0)), nil
}

func buildHNSW(vecs [][]float32, m hnsw.Metric, maxM, efConst int) (*hnsw.HNSWIndex, string, float64, error) {
	tmpDir, err := os.MkdirTemp("", "audiobench-hnsw-*")
	if err != nil {
		return nil, "", 0, err
	}
	cfg := hnsw.DefaultIndexConfig(m)
	cfg.M = maxM
	cfg.EfConstruction = efConst

	t0 := time.Now()
	idx, err := hnsw.OpenWithConfig(filepath.Join(tmpDir, "hnsw.bin"), cfg)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return nil, "", 0, err
	}
	for i, v := range vecs {
		if err := idx.Add(uint64(i+1), v); err != nil {
			_ = idx.Close()
			_ = os.RemoveAll(tmpDir)
			return nil, "", 0, err
		}
	}
	return idx, tmpDir, ms(time.Since(t0)), nil
}

func runSuiteForMetric(rec *AudioRecord, metricName string, sizes []int, numQueries int, efConst int, maxM int) ([]benchRow, error) {
	mVec, mHNSW, err := parseMetric(metricName)
	if err != nil {
		return nil, err
	}
	totalN := len(rec.Vectors)
	dim := len(rec.Vectors[0])
	var rows []benchRow

	fmt.Printf("\n====================================================================\n")
	fmt.Printf("MÉTRICA: %s | Dataset base: %d pistas (%d dims) | Consultas: %d\n", strings.ToUpper(metricName), totalN, dim, numQueries)
	fmt.Printf("====================================================================\n")

	// -----------------------------------------------------------------------
	// EXPERIMENTO 1: Escalabilidad respecto al tamaño N (1000, 10000, 100000)
	// -----------------------------------------------------------------------
	fmt.Printf("\n### Experimento 1: Escalabilidad (Construcción y Consulta k=10)\n\n")
	fmt.Printf("| N | Método | Parámetros | Construcción (ms) | Latencia media (ms) | Latencia P95 (ms) | Speedup | Recall@10 |\n")
	fmt.Printf("|---:|---|---|---:|---:|---:|---:|---:|\n")

	for _, n := range sizes {
		subVecs := buildDatasetForN(rec.Vectors, n, 2026)
		realPool := n
		if realPool > totalN {
			realPool = totalN
		}
		queries := sampleQueryIndices(realPool, numQueries, 42)
		truths := make([][]int, len(queries))
		seqTimes := make([]float64, len(queries))

		for qi, qIdx := range queries {
			truths[qi], seqTimes[qi] = exactTopK(subVecs, n, qIdx, 20, mVec)
		}
		avgSeq := mean(seqTimes)
		p95Seq := percentile95(seqTimes)

		seqRow := benchRow{
			Experiment: "escalabilidad", Metric: metricName, N: n,
			Method: "Secuencial", Params: "exacto",
			BuildMs: 0, AvgQueryMs: avgSeq, P95QueryMs: p95Seq, Speedup: 1.0,
			Recall5: 1.0, Recall10: 1.0, Recall20: 1.0,
		}
		rows = append(rows, seqRow)
		fmt.Printf("| %d | Secuencial | exacto | 0.00 | %.3f | %.3f | 1.00x | 100.0%% |\n", n, avgSeq, p95Seq)

		// IVF con nlist = sqrt(N), nprobe = nlist / 4
		nlist := int(math.Round(math.Sqrt(float64(n))))
		nprobe := int(math.Max(1, math.Round(float64(nlist)/4.0)))
		ivfIdx, ivfBuildMs, err := buildIVF(subVecs, dim, nlist, mVec)
		if err != nil {
			return nil, err
		}
		ivfTimes := make([]float64, len(queries))
		var r5, r10, r20 float64
		for qi, qIdx := range queries {
			t0 := time.Now()
			cands, err := ivfIdx.Query(subVecs[qIdx], 21, nprobe)
			ivfTimes[qi] = ms(time.Since(t0))
			if err != nil {
				return nil, err
			}
			found := make([]int, 0, 20)
			for _, c := range cands {
				if int(c.ID) != qIdx {
					found = append(found, int(c.ID))
				}
			}
			r5 += computeRecall(truths[qi], found, 5)
			r10 += computeRecall(truths[qi], found, 10)
			r20 += computeRecall(truths[qi], found, 20)
		}
		qf := float64(len(queries))
		avgIVF := mean(ivfTimes)
		p95IVF := percentile95(ivfTimes)
		ivfRow := benchRow{
			Experiment: "escalabilidad", Metric: metricName, N: n,
			Method: "IVF-Flat", Params: fmt.Sprintf("nlist=%d, nprobe=%d", nlist, nprobe),
			BuildMs: ivfBuildMs, AvgQueryMs: avgIVF, P95QueryMs: p95IVF, Speedup: avgSeq / avgIVF,
			Recall5: r5 / qf, Recall10: r10 / qf, Recall20: r20 / qf,
		}
		rows = append(rows, ivfRow)
		fmt.Printf("| %d | IVF-Flat | %s | %.2f | %.3f | %.3f | %.2fx | %.1f%% |\n",
			n, ivfRow.Params, ivfBuildMs, avgIVF, p95IVF, ivfRow.Speedup, ivfRow.Recall10*100)

		// HNSW con M=maxM, efConstruction=efConst, efSearch=64
		hnswIdx, tmpDir, hnswBuildMs, err := buildHNSW(subVecs, mHNSW, maxM, efConst)
		if err != nil {
			return nil, err
		}
		hnswTimes := make([]float64, len(queries))
		r5, r10, r20 = 0, 0, 0
		for qi, qIdx := range queries {
			t0 := time.Now()
			cands, err := hnswIdx.KNNSearch(subVecs[qIdx], 21, 64)
			hnswTimes[qi] = ms(time.Since(t0))
			if err != nil {
				_ = hnswIdx.Close()
				_ = os.RemoveAll(tmpDir)
				return nil, err
			}
			found := make([]int, 0, 20)
			for _, c := range cands {
				id := int(c.ID) - 1
				if id != qIdx {
					found = append(found, id)
				}
			}
			r5 += computeRecall(truths[qi], found, 5)
			r10 += computeRecall(truths[qi], found, 10)
			r20 += computeRecall(truths[qi], found, 20)
		}
		_ = hnswIdx.Close()
		_ = os.RemoveAll(tmpDir)

		avgHNSW := mean(hnswTimes)
		p95HNSW := percentile95(hnswTimes)
		hnswRow := benchRow{
			Experiment: "escalabilidad", Metric: metricName, N: n,
			Method: "HNSW", Params: fmt.Sprintf("M=%d, efC=%d, efS=64", maxM, efConst),
			BuildMs: hnswBuildMs, AvgQueryMs: avgHNSW, P95QueryMs: p95HNSW, Speedup: avgSeq / avgHNSW,
			Recall5: r5 / qf, Recall10: r10 / qf, Recall20: r20 / qf,
		}
		rows = append(rows, hnswRow)
		fmt.Printf("| %d | HNSW | %s | %.2f | %.3f | %.3f | %.2fx | %.1f%% |\n",
			n, hnswRow.Params, hnswBuildMs, avgHNSW, p95HNSW, hnswRow.Speedup, hnswRow.Recall10*100)
	}

	// -----------------------------------------------------------------------
	// EXPERIMENTO 2: Trade-off Recall@k vs Latencia sobre las N = 7997 pistas reales
	// -----------------------------------------------------------------------
	queries := sampleQueryIndices(totalN, numQueries, 42)
	truths := make([][]int, len(queries))
	seqTimes := make([]float64, len(queries))
	for qi, qIdx := range queries {
		truths[qi], seqTimes[qi] = exactTopK(rec.Vectors, totalN, qIdx, 20, mVec)
	}
	avgSeq := mean(seqTimes)
	qf := float64(len(queries))

	fmt.Printf("\n### Experimento 2: Compromiso Precisión vs. Velocidad (N = %d pistas reales)\n\n", totalN)
	fmt.Printf("| Índice | Parámetro | Latencia media (ms) | P95 (ms) | Speedup | Recall@5 | Recall@10 | Recall@20 |\n")
	fmt.Printf("|---|---|---:|---:|---:|---:|---:|---:|\n")

	nlist := int(math.Round(math.Sqrt(float64(totalN))))
	ivfIdx, ivfBuildMs, err := buildIVF(rec.Vectors, dim, nlist, mVec)
	if err != nil {
		return nil, err
	}
	nprobes := []int{1, 2, 4, 8, 16, 22, 45, nlist}
	for _, np := range nprobes {
		if np > nlist {
			continue
		}
		qTimes := make([]float64, len(queries))
		var r5, r10, r20 float64
		for qi, qIdx := range queries {
			t0 := time.Now()
			cands, err := ivfIdx.Query(rec.Vectors[qIdx], 21, np)
			qTimes[qi] = ms(time.Since(t0))
			if err != nil {
				return nil, err
			}
			found := make([]int, 0, 20)
			for _, c := range cands {
				if int(c.ID) != qIdx {
					found = append(found, int(c.ID))
				}
			}
			r5 += computeRecall(truths[qi], found, 5)
			r10 += computeRecall(truths[qi], found, 10)
			r20 += computeRecall(truths[qi], found, 20)
		}
		avgQ := mean(qTimes)
		p95Q := percentile95(qTimes)
		row := benchRow{
			Experiment: "tradeoff", Metric: metricName, N: totalN,
			Method: "IVF-Flat", Params: fmt.Sprintf("nlist=%d, nprobe=%d", nlist, np),
			BuildMs: ivfBuildMs, AvgQueryMs: avgQ, P95QueryMs: p95Q, Speedup: avgSeq / avgQ,
			Recall5: r5 / qf, Recall10: r10 / qf, Recall20: r20 / qf,
		}
		rows = append(rows, row)
		fmt.Printf("| IVF-Flat | nprobe=%d/%d | %.3f | %.3f | %.2fx | %.1f%% | %.1f%% | %.1f%% |\n",
			np, nlist, avgQ, p95Q, row.Speedup, row.Recall5*100, row.Recall10*100, row.Recall20*100)
	}

	hnswIdx, tmpDir, hnswBuildMs, err := buildHNSW(rec.Vectors, mHNSW, maxM, efConst)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = hnswIdx.Close()
		_ = os.RemoveAll(tmpDir)
	}()

	efList := []int{10, 16, 32, 64, 128, 256}
	for _, ef := range efList {
		qTimes := make([]float64, len(queries))
		var r5, r10, r20 float64
		for qi, qIdx := range queries {
			t0 := time.Now()
			cands, err := hnswIdx.KNNSearch(rec.Vectors[qIdx], 21, ef)
			qTimes[qi] = ms(time.Since(t0))
			if err != nil {
				return nil, err
			}
			found := make([]int, 0, 20)
			for _, c := range cands {
				id := int(c.ID) - 1
				if id != qIdx {
					found = append(found, id)
				}
			}
			r5 += computeRecall(truths[qi], found, 5)
			r10 += computeRecall(truths[qi], found, 10)
			r20 += computeRecall(truths[qi], found, 20)
		}
		avgQ := mean(qTimes)
		p95Q := percentile95(qTimes)
		row := benchRow{
			Experiment: "tradeoff", Metric: metricName, N: totalN,
			Method: "HNSW", Params: fmt.Sprintf("M=%d, efC=%d, efS=%d", maxM, efConst, ef),
			BuildMs: hnswBuildMs, AvgQueryMs: avgQ, P95QueryMs: p95Q, Speedup: avgSeq / avgQ,
			Recall5: r5 / qf, Recall10: r10 / qf, Recall20: r20 / qf,
		}
		rows = append(rows, row)
		fmt.Printf("| HNSW | efSearch=%d | %.3f | %.3f | %.2fx | %.1f%% | %.1f%% | %.1f%% |\n",
			ef, avgQ, p95Q, row.Speedup, row.Recall5*100, row.Recall10*100, row.Recall20*100)
	}

	return rows, nil
}

func mean(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	var sum float64
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}

func exportCSV(path string, rows []benchRow) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()

	_ = w.Write([]string{
		"experiment", "metric", "N", "method", "params",
		"build_ms", "avg_query_ms", "p95_query_ms", "speedup",
		"recall_5", "recall_10", "recall_20",
	})
	for _, r := range rows {
		_ = w.Write([]string{
			r.Experiment, r.Metric, strconv.Itoa(r.N), r.Method, r.Params,
			fmt.Sprintf("%.4f", r.BuildMs),
			fmt.Sprintf("%.4f", r.AvgQueryMs),
			fmt.Sprintf("%.4f", r.P95QueryMs),
			fmt.Sprintf("%.4f", r.Speedup),
			fmt.Sprintf("%.4f", r.Recall5),
			fmt.Sprintf("%.4f", r.Recall10),
			fmt.Sprintf("%.4f", r.Recall20),
		})
	}
	return w.Error()
}

func main() {
	featuresPath := flag.String("features", "data/audio/audio_features.gob", "ruta al archivo audio_features.gob")
	sizesFlag := flag.String("sizes", "1000,10000,100000", "tamaños N separados por coma para escalabilidad")
	numQueries := flag.Int("queries", 100, "número de pistas de consulta a promediar")
	metricFlag := flag.String("metric", "all", "métrica: cosine, euclidean o all")
	efConst := flag.Int("ef-construction", 100, "efConstruction para HNSW")
	maxM := flag.Int("m", 16, "M para HNSW")
	csvOut := flag.String("csv", "", "ruta opcional para exportar los resultados en CSV")
	flag.Parse()

	sizes, err := parseSizes(*sizesFlag)
	if err != nil {
		log.Fatal(err)
	}

	rec, err := loadAudioFeatures(*featuresPath)
	if err != nil {
		log.Fatalf("error cargando %s: %v", *featuresPath, err)
	}
	if len(rec.Vectors) == 0 {
		log.Fatalf("el dataset en %s no contiene vectores", *featuresPath)
	}

	metrics := []string{*metricFlag}
	if strings.EqualFold(*metricFlag, "all") {
		metrics = []string{"cosine", "euclidean"}
	}

	var allRows []benchRow
	for _, m := range metrics {
		rows, err := runSuiteForMetric(rec, m, sizes, *numQueries, *efConst, *maxM)
		if err != nil {
			log.Fatal(err)
		}
		allRows = append(allRows, rows...)
	}

	if *csvOut != "" {
		if err := exportCSV(*csvOut, allRows); err != nil {
			log.Fatalf("error exportando CSV: %v", err)
		}
		fmt.Printf("\nResultados exportados a %s\n", *csvOut)
	}
}
