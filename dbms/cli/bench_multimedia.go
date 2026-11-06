package main

import (
	"encoding/csv"
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

	"github.com/dbms-go/v2/dbms/lib/index/hnsw"
	"github.com/dbms-go/v2/dbms/lib/index/ivf"
	metricpkg "github.com/dbms-go/v2/dbms/lib/vector/metric"
)

type multimediaVector struct {
	ID    uint64
	Path  string
	Label string
	Vec   []float32
}

type multimediaHit struct {
	ID   uint64
	Dist float64
}

func benchMultimedia(args []string) error {
	fs := flagSet("multimedia")
	vectorsPath := fs.String("vectors", "experiments/multimedia/data/index_vectors.csv", "CSV de vectores de índice")
	queriesPath := fs.String("queries-file", "experiments/multimedia/data/query_vectors.csv", "CSV de vectores de consulta")
	n := fs.Int("n", 1000, "cantidad de vectores a indexar")
	queryLimit := fs.Int("query-limit", 100, "cantidad de consultas a evaluar")
	k := fs.Int("k", 10, "cantidad de vecinos")
	out := fs.String("out", "experiments/multimedia/resultados", "directorio de salida")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *n < *k || *k <= 0 || *queryLimit <= 0 {
		return fmt.Errorf("n debe ser >= k y k/query-limit deben ser positivos")
	}

	all, err := readMultimediaVectors(*vectorsPath)
	if err != nil {
		return err
	}
	queries, err := readMultimediaVectors(*queriesPath)
	if err != nil {
		return err
	}
	if len(all) < *n {
		return fmt.Errorf("el CSV contiene %d vectores; solicitaste %d", len(all), *n)
	}
	if len(queries) > *queryLimit {
		queries = queries[:*queryLimit]
	}
	if len(queries) == 0 {
		return fmt.Errorf("no hay vectores de consulta")
	}
	all = all[:*n]
	dim := len(all[0].Vec)
	for _, v := range all {
		if len(v.Vec) != dim {
			return fmt.Errorf("dimensiones inconsistentes en vectores de índice")
		}
	}
	for _, v := range queries {
		if len(v.Vec) != dim {
			return fmt.Errorf("dimensión de consulta %d distinta de %d", len(v.Vec), dim)
		}
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}

	byID := make(map[uint64]multimediaVector, len(all))
	for _, v := range all {
		byID[v.ID] = v
	}
	resultFile, err := os.Create(filepath.Join(*out, fmt.Sprintf("multimedia_%d.csv", *n)))
	if err != nil {
		return err
	}
	defer resultFile.Close()
	w := csv.NewWriter(resultFile)
	defer w.Flush()
	if err := w.Write([]string{"dataset_size", "index", "metric", "build_ms", "query_avg_ms", "recall_at_10", "memory_mb", "index_size_mb", "query_id", "query_path", "query_label", "rank", "result_id", "result_path", "result_label", "distance", "relevant"}); err != nil {
		return err
	}
	sf, err := os.Create(filepath.Join(*out, fmt.Sprintf("resumen_%d.csv", *n)))
	if err != nil {
		return err
	}
	defer sf.Close()
	sw := csv.NewWriter(sf)
	defer sw.Flush()
	if err := sw.Write([]string{"dataset_size", "index", "metric", "build_ms", "query_avg_ms", "recall_at_10", "memory_mb", "index_size_mb"}); err != nil {
		return err
	}

	metrics := []struct {
		name    string
		value   metricpkg.Metric
		hmetric hnsw.Metric
	}{
		{"euclidean", metricpkg.Euclidean, hnsw.Euclidean},
		{"cosine", metricpkg.Cosine, hnsw.Cosine},
	}
	for _, m := range metrics {
		// Referencia exacta: calcula los vecinos reales usando todos los vectores.
		exactByQuery := make(map[uint64]map[uint64]bool, len(queries))
		for _, q := range queries {
			exactByQuery[q.ID] = exactKNN(all, q.Vec, *k, m.value)
		}

		// IVF-Flat propio.
		nlist := int(math.Sqrt(float64(*n)))
		if nlist < 2 {
			nlist = 2
		}
		if nlist > 64 {
			nlist = 64
		}
		runtime.GC()
		before := heapAlloc()
		start := time.Now()
		ivfIndex, err := ivf.NewIVFFlatIndex(dim, nlist, m.value)
		if err != nil {
			return err
		}
		if err := ivfIndex.Train(vectorsOnly(all)); err != nil {
			return fmt.Errorf("IVF Train (%s): %w", m.name, err)
		}
		for _, v := range all {
			if err := ivfIndex.Insert(v.ID, v.Vec); err != nil {
				return fmt.Errorf("IVF Insert: %w", err)
			}
		}
		build := time.Since(start)
		runtime.GC()
		mem := heapAlloc() - before
		probe := nlist / 4
		if probe < 1 {
			probe = 1
		}
		var totalQuery time.Duration
		recall := 0.0
		for _, q := range queries {
			t0 := time.Now()
			hits, err := ivfIndex.Query(q.Vec, *k, probe)
			totalQuery += time.Since(t0)
			if err != nil {
				return err
			}
			got := make(map[uint64]bool, len(hits))
			for _, h := range hits {
				got[h.ID] = true
			}
			recall += recallOf(got, exactByQuery[q.ID], *k)
			if err := writeHits(w, *n, "IVF-Flat", m.name, build, totalQuery, recall, mem, 0, q, hitsToRows(hits), byID, exactByQuery[q.ID]); err != nil {
				return err
			}
		}
		if err := sw.Write([]string{strconv.Itoa(*n), "IVF-Flat", m.name, fmt.Sprintf("%.6f", msMM(build)), fmt.Sprintf("%.6f", msMM(totalQuery)/float64(len(queries))), fmt.Sprintf("%.6f", recall/float64(len(queries))), fmt.Sprintf("%.6f", mbMM(mem)), "0"}); err != nil {
			return err
		}
		fmt.Printf("IVF-Flat %-9s build=%.2f ms query=%.4f ms recall=%.3f heap=%.2f MB nlist=%d nprobe=%d\n", m.name, msMM(build), msMM(totalQuery)/float64(len(queries)), recall/float64(len(queries)), mbMM(mem), nlist, probe)

		// HNSW propio. Cada métrica se guarda en un archivo distinto.
		hnswPath := filepath.Join(*out, fmt.Sprintf("hnsw_%s_%d.idx", m.name, *n))
		_ = os.Remove(hnswPath)
		cfg := hnsw.DefaultIndexConfig(m.hmetric)
		runtime.GC()
		before = heapAlloc()
		start = time.Now()
		idx, err := hnsw.OpenWithConfig(hnswPath, cfg)
		if err != nil {
			return err
		}
		for _, v := range all {
			if err := idx.Add(v.ID, v.Vec); err != nil {
				return err
			}
		}
		if err := idx.Save(); err != nil {
			return err
		}
		build = time.Since(start)
		runtime.GC()
		mem = heapAlloc() - before
		info, _ := os.Stat(hnswPath)
		indexSize := int64(0)
		if info != nil {
			indexSize = info.Size()
		}
		totalQuery = 0
		recall = 0
		for _, q := range queries {
			t0 := time.Now()
			hits, err := idx.KNNSearch(q.Vec, *k, cfg.EfSearch)
			totalQuery += time.Since(t0)
			if err != nil {
				return err
			}
			got := make(map[uint64]bool, len(hits))
			for _, h := range hits {
				got[h.ID] = true
			}
			recall += recallOf(got, exactByQuery[q.ID], *k)
			rows := make([]ivf.Candidate, len(hits))
			for i, h := range hits {
				rows[i] = ivf.Candidate{ID: h.ID, Dist: h.Distance}
			}
			if err := writeHits(w, *n, "HNSW", m.name, build, totalQuery, recall, mem, indexSize, q, rows, byID, exactByQuery[q.ID]); err != nil {
				return err
			}
		}
		if err := sw.Write([]string{strconv.Itoa(*n), "HNSW", m.name, fmt.Sprintf("%.6f", msMM(build)), fmt.Sprintf("%.6f", msMM(totalQuery)/float64(len(queries))), fmt.Sprintf("%.6f", recall/float64(len(queries))), fmt.Sprintf("%.6f", mbMM(mem)), fmt.Sprintf("%.6f", mbMM(uint64(indexSize)))}); err != nil {
			return err
		}
		fmt.Printf("HNSW     %-9s build=%.2f ms query=%.4f ms recall=%.3f heap=%.2f MB file=%.2f MB\n", m.name, msMM(build), msMM(totalQuery)/float64(len(queries)), recall/float64(len(queries)), mbMM(mem), mbMM(uint64(indexSize)))
		if err := idx.Close(); err != nil {
			return err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	sw.Flush()
	if err := sw.Error(); err != nil {
		return err
	}
	fmt.Printf("\nResultados guardados en %s\n", resultFile.Name())
	return nil
}

func readMultimediaVectors(path string) ([]multimediaVector, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("abrir %s: %w", path, err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	head, err := r.Read()
	if err != nil {
		return nil, err
	}
	if len(head) < 4 {
		return nil, fmt.Errorf("CSV %s debe tener id,path,label y al menos una dimensión", path)
	}
	var out []multimediaVector
	for line := 2; ; line++ {
		row, err := r.Read()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("%s línea %d: %w", path, line, err)
		}
		if len(row) != len(head) {
			return nil, fmt.Errorf("%s línea %d: columnas incorrectas", path, line)
		}
		id, err := strconv.ParseUint(row[0], 10, 64)
		if err != nil {
			return nil, err
		}
		v := multimediaVector{ID: id, Path: row[1], Label: row[2], Vec: make([]float32, len(row)-3)}
		for i, s := range row[3:] {
			x, err := strconv.ParseFloat(strings.TrimSpace(s), 32)
			if err != nil {
				return nil, fmt.Errorf("%s línea %d dimensión %d: %w", path, line, i, err)
			}
			v.Vec[i] = float32(x)
		}
		out = append(out, v)
	}
	return out, nil
}

func vectorsOnly(v []multimediaVector) [][]float32 {
	out := make([][]float32, len(v))
	for i := range v {
		out[i] = v[i].Vec
	}
	return out
}
func exactKNN(data []multimediaVector, q []float32, k int, m metricpkg.Metric) map[uint64]bool {
	hits := make([]multimediaHit, 0, len(data))
	for _, v := range data {
		hits = append(hits, multimediaHit{v.ID, metricpkg.Distance(q, v.Vec, m)})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Dist < hits[j].Dist })
	if len(hits) > k {
		hits = hits[:k]
	}
	out := make(map[uint64]bool, len(hits))
	for _, h := range hits {
		out[h.ID] = true
	}
	return out
}
func recallOf(got, expected map[uint64]bool, k int) float64 {
	if len(expected) == 0 {
		return 0
	}
	n := 0
	for id := range expected {
		if got[id] {
			n++
		}
	}
	return float64(n) / float64(k)
}
func hitsToRows(h []ivf.Candidate) []ivf.Candidate { return h }
func writeHits(w *csv.Writer, n int, index, metric string, build, elapsed time.Duration, recall float64, mem uint64, disk int64, q multimediaVector, hits []ivf.Candidate, byID map[uint64]multimediaVector, exact map[uint64]bool) error {
	for rank, h := range hits {
		v, ok := byID[h.ID]
		if !ok {
			continue
		}
		relevant := exact[h.ID]
		row := []string{strconv.Itoa(n), index, metric, fmt.Sprintf("%.6f", msMM(build)), fmt.Sprintf("%.6f", msMM(elapsed)), fmt.Sprintf("%.6f", recall), fmt.Sprintf("%.6f", mbMM(mem)), fmt.Sprintf("%.6f", mbMM(uint64(max64(disk, 0)))), strconv.FormatUint(q.ID, 10), q.Path, q.Label, strconv.Itoa(rank + 1), strconv.FormatUint(v.ID, 10), v.Path, v.Label, fmt.Sprintf("%.8f", h.Dist), strconv.FormatBool(relevant)}
		if err := w.Write(row); err != nil {
			return err
		}
	}
	return nil
}
func flagSet(name string) *flag.FlagSet { return flag.NewFlagSet(name, flag.ContinueOnError) }
func heapAlloc() uint64                 { var m runtime.MemStats; runtime.ReadMemStats(&m); return m.HeapAlloc }
func msMM(d time.Duration) float64      { return float64(d.Nanoseconds()) / 1e6 }
func mbMM(n uint64) float64             { return float64(n) / (1024 * 1024) }
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
