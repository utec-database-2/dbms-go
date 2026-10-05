package main

import (
	"encoding/binary"
	"encoding/csv"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dbms-go/v2/dbms/lib/concurrency/sequential"
	"github.com/dbms-go/v2/dbms/lib/storage/heap"
)

const (
	seqPageHeaderBytes = 10
	seqSlotHeaderBytes = 19
)

type config struct {
	sizes           []int
	repeats         int
	queries         int
	deleteFraction  float64
	logicalBytes    int
	heapPageSize    int
	seqPageCapacity int
	seed            int64
	resultsDir      string
}

type csvWriter struct {
	file *os.File
	w    *csv.Writer
}

func newCSV(path string, header []string) (*csvWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := csv.NewWriter(f)
	if err := w.Write(header); err != nil {
		f.Close()
		return nil, err
	}
	return &csvWriter{file: f, w: w}, nil
}

func (c *csvWriter) write(row ...string) error { return c.w.Write(row) }
func (c *csvWriter) close() error {
	c.w.Flush()
	if err := c.w.Error(); err != nil {
		c.file.Close()
		return err
	}
	return c.file.Close()
}

func parseSizes(raw string) ([]int, error) {
	parts := strings.Split(raw, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("invalid size %q", p)
		}
		out = append(out, n)
	}
	return out, nil
}

func keyOrder(n int, seed int64) []int64 {
	r := rand.New(rand.NewSource(seed))
	p := r.Perm(n)
	out := make([]int64, n)
	for i, v := range p {
		out[i] = int64(v + 1)
	}
	return out
}

func queryKeys(n, count int, seed int64) []int64 {
	if count <= 0 {
		count = 1
	}
	r := rand.New(rand.NewSource(seed))
	out := make([]int64, count)
	for i := range out {
		out[i] = int64(r.Intn(n) + 1)
	}
	return out
}

func bodyForKey(key int64, bodyBytes int) []byte {
	out := make([]byte, bodyBytes)
	for i := range out {
		out[i] = byte((int(key) + i*31) % 251)
	}
	return out
}

func heapPayload(key int64, logicalBytes int) []byte {
	out := make([]byte, logicalBytes)
	binary.LittleEndian.PutUint64(out[:8], uint64(key))
	body := bodyForKey(key, logicalBytes-8)
	copy(out[8:], body)
	return out
}

func heapKey(payload []byte) int64 {
	if len(payload) < 8 {
		return 0
	}
	return int64(binary.LittleEndian.Uint64(payload[:8]))
}

func fileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

func rotatedPair(rep int) []string {
	if rep%2 == 0 {
		return []string{"heap", "sequential"}
	}
	return []string{"sequential", "heap"}
}

func benchmarkInsertionAndStorage(cfg config, n, rep int, ins, storage *csvWriter) error {
	order := keyOrder(n, cfg.seed+int64(n)*101+int64(rep)*10007)
	bodyBytes := cfg.logicalBytes - 8

	for _, kind := range rotatedPair(rep) {
		dir, err := os.MkdirTemp("", "dbms-storage-build-")
		if err != nil {
			return err
		}

		switch kind {
		case "heap":
			path := filepath.Join(dir, "data.heap")
			hf, err := heap.Create(path, cfg.heapPageSize)
			if err != nil {
				os.RemoveAll(dir)
				return err
			}
			start := time.Now()
			for _, key := range order {
				if _, err := hf.Insert(heapPayload(key, cfg.logicalBytes)); err != nil {
					hf.Close()
					os.RemoveAll(dir)
					return err
				}
			}
			elapsed := time.Since(start)
			if err := hf.Close(); err != nil {
				os.RemoveAll(dir)
				return err
			}
			bytes := fileSize(path)
			if err := ins.write(strconv.Itoa(n), strconv.Itoa(rep+1), "heap_file", fmt.Sprintf("%.6f", elapsed.Seconds()*1000), fmt.Sprintf("%.3f", float64(elapsed.Nanoseconds())/float64(n))); err != nil {
				os.RemoveAll(dir)
				return err
			}
			if err := storage.write(strconv.Itoa(n), strconv.Itoa(rep+1), "heap_file", strconv.FormatInt(bytes, 10), strconv.FormatInt(bytes, 10), "0", fmt.Sprintf("%.3f", float64(bytes)/float64(n))); err != nil {
				os.RemoveAll(dir)
				return err
			}

		case "sequential":
			mainPath := filepath.Join(dir, "data.seq")
			ovfPath := filepath.Join(dir, "data.ovf")
			sf, err := sequential.Create(mainPath, ovfPath, cfg.seqPageCapacity, bodyBytes)
			if err != nil {
				os.RemoveAll(dir)
				return err
			}
			start := time.Now()
			for _, key := range order {
				if _, err := sf.InsertRecord(key, bodyForKey(key, bodyBytes)); err != nil {
					sf.Close()
					os.RemoveAll(dir)
					return err
				}
			}
			elapsed := time.Since(start)
			if err := sf.Close(); err != nil {
				os.RemoveAll(dir)
				return err
			}
			mainBytes := fileSize(mainPath)
			ovfBytes := fileSize(ovfPath)
			total := mainBytes + ovfBytes
			if err := ins.write(strconv.Itoa(n), strconv.Itoa(rep+1), "sequential_file", fmt.Sprintf("%.6f", elapsed.Seconds()*1000), fmt.Sprintf("%.3f", float64(elapsed.Nanoseconds())/float64(n))); err != nil {
				os.RemoveAll(dir)
				return err
			}
			if err := storage.write(strconv.Itoa(n), strconv.Itoa(rep+1), "sequential_file", strconv.FormatInt(total, 10), strconv.FormatInt(mainBytes, 10), strconv.FormatInt(ovfBytes, 10), fmt.Sprintf("%.3f", float64(total)/float64(n))); err != nil {
				os.RemoveAll(dir)
				return err
			}
		}
		os.RemoveAll(dir)
	}
	return nil
}

func benchmarkSearch(cfg config, n, rep int, out *csvWriter) error {
	order := keyOrder(n, cfg.seed+int64(n)*211+int64(rep)*11003)
	queries := queryKeys(n, cfg.queries, cfg.seed+int64(n)*307+int64(rep)*19001)
	bodyBytes := cfg.logicalBytes - 8

	for _, kind := range rotatedPair(rep) {
		dir, err := os.MkdirTemp("", "dbms-storage-search-")
		if err != nil {
			return err
		}

		switch kind {
		case "heap":
			path := filepath.Join(dir, "data.heap")
			hf, err := heap.Create(path, cfg.heapPageSize)
			if err != nil {
				os.RemoveAll(dir)
				return err
			}
			for _, key := range order {
				if _, err := hf.Insert(heapPayload(key, cfg.logicalBytes)); err != nil {
					hf.Close()
					os.RemoveAll(dir)
					return err
				}
			}

			found := 0
			start := time.Now()
			for _, target := range queries {
				hit := false
				if err := hf.Scan(func(_ heap.RecordID, payload []byte) bool {
					if heapKey(payload) == target {
						hit = true
						return false
					}
					return true
				}); err != nil {
					hf.Close()
					os.RemoveAll(dir)
					return err
				}
				if hit {
					found++
				}
			}
			elapsed := time.Since(start)
			hf.Close()
			if found != len(queries) {
				os.RemoveAll(dir)
				return fmt.Errorf("heap search found %d/%d", found, len(queries))
			}
			if err := out.write(strconv.Itoa(n), strconv.Itoa(rep+1), "heap_file", strconv.Itoa(len(queries)), fmt.Sprintf("%.6f", elapsed.Seconds()*1000), fmt.Sprintf("%.3f", float64(elapsed.Microseconds())/float64(len(queries)))); err != nil {
				os.RemoveAll(dir)
				return err
			}

		case "sequential":
			mainPath := filepath.Join(dir, "data.seq")
			ovfPath := filepath.Join(dir, "data.ovf")
			sf, err := sequential.Create(mainPath, ovfPath, cfg.seqPageCapacity, bodyBytes)
			if err != nil {
				os.RemoveAll(dir)
				return err
			}
			for _, key := range order {
				if _, err := sf.InsertRecord(key, bodyForKey(key, bodyBytes)); err != nil {
					sf.Close()
					os.RemoveAll(dir)
					return err
				}
			}

			found := 0
			start := time.Now()
			for _, target := range queries {
				_, ok, err := sf.Search(target)
				if err != nil {
					sf.Close()
					os.RemoveAll(dir)
					return err
				}
				if ok {
					found++
				}
			}
			elapsed := time.Since(start)
			sf.Close()
			if found != len(queries) {
				os.RemoveAll(dir)
				return fmt.Errorf("sequential search found %d/%d", found, len(queries))
			}
			if err := out.write(strconv.Itoa(n), strconv.Itoa(rep+1), "sequential_file", strconv.Itoa(len(queries)), fmt.Sprintf("%.6f", elapsed.Seconds()*1000), fmt.Sprintf("%.3f", float64(elapsed.Microseconds())/float64(len(queries)))); err != nil {
				os.RemoveAll(dir)
				return err
			}
		}
		os.RemoveAll(dir)
	}
	return nil
}

func benchmarkMaintenance(cfg config, n, rep int, out *csvWriter) error {
	order := keyOrder(n, cfg.seed+int64(n)*401+int64(rep)*23003)
	deleteOrder := keyOrder(n, cfg.seed+int64(n)*503+int64(rep)*29009)
	deleteCount := int(float64(n) * cfg.deleteFraction)
	if deleteCount < 1 {
		deleteCount = 1
	}
	deleteKeys := deleteOrder[:deleteCount]
	bodyBytes := cfg.logicalBytes - 8

	// Heap: no existe una reorganización global; cada Delete compacta su página.
	{
		dir, err := os.MkdirTemp("", "dbms-storage-maint-heap-")
		if err != nil {
			return err
		}
		path := filepath.Join(dir, "data.heap")
		hf, err := heap.Create(path, cfg.heapPageSize)
		if err != nil {
			os.RemoveAll(dir)
			return err
		}
		rids := make(map[int64]heap.RecordID, n)
		for _, key := range order {
			rid, err := hf.Insert(heapPayload(key, cfg.logicalBytes))
			if err != nil {
				hf.Close()
				os.RemoveAll(dir)
				return err
			}
			rids[key] = rid
		}
		start := time.Now()
		for _, key := range deleteKeys {
			if err := hf.Delete(rids[key]); err != nil {
				hf.Close()
				os.RemoveAll(dir)
				return err
			}
		}
		elapsed := time.Since(start)
		hf.Close()
		if err := out.write(strconv.Itoa(n), strconv.Itoa(rep+1), "heap_file", "delete_with_page_compaction", strconv.Itoa(deleteCount), fmt.Sprintf("%.6f", elapsed.Seconds()*1000)); err != nil {
			os.RemoveAll(dir)
			return err
		}
		os.RemoveAll(dir)
	}

	// Sequential: primero delete lazy y luego Reorganize explícito.
	{
		dir, err := os.MkdirTemp("", "dbms-storage-maint-seq-")
		if err != nil {
			return err
		}
		mainPath := filepath.Join(dir, "data.seq")
		ovfPath := filepath.Join(dir, "data.ovf")
		sf, err := sequential.Create(mainPath, ovfPath, cfg.seqPageCapacity, bodyBytes)
		if err != nil {
			os.RemoveAll(dir)
			return err
		}
		rids := make(map[int64]sequential.RecordID, n)
		for _, key := range order {
			rid, err := sf.InsertRecord(key, bodyForKey(key, bodyBytes))
			if err != nil {
				sf.Close()
				os.RemoveAll(dir)
				return err
			}
			rids[key] = rid
		}

		// Evita que los deletes disparen automáticamente la reorganización:
		// queremos medir delete lazy y Reorganize por separado.
		sf.SetReorgThreshold(1.0)

		deleteStart := time.Now()
		for _, key := range deleteKeys {
			if err := sf.DeleteRID(rids[key]); err != nil {
				sf.Close()
				os.RemoveAll(dir)
				return err
			}
		}
		deleteElapsed := time.Since(deleteStart)

		reorgStart := time.Now()
		if err := sf.Reorganize(); err != nil {
			sf.Close()
			os.RemoveAll(dir)
			return err
		}
		reorgElapsed := time.Since(reorgStart)
		sf.Close()

		if err := out.write(strconv.Itoa(n), strconv.Itoa(rep+1), "sequential_file", "lazy_delete", strconv.Itoa(deleteCount), fmt.Sprintf("%.6f", deleteElapsed.Seconds()*1000)); err != nil {
			os.RemoveAll(dir)
			return err
		}
		if err := out.write(strconv.Itoa(n), strconv.Itoa(rep+1), "sequential_file", "reorganize", strconv.Itoa(deleteCount), fmt.Sprintf("%.6f", reorgElapsed.Seconds()*1000)); err != nil {
			os.RemoveAll(dir)
			return err
		}
		if err := out.write(strconv.Itoa(n), strconv.Itoa(rep+1), "sequential_file", "delete_plus_reorganize", strconv.Itoa(deleteCount), fmt.Sprintf("%.6f", (deleteElapsed+reorgElapsed).Seconds()*1000)); err != nil {
			os.RemoveAll(dir)
			return err
		}
		os.RemoveAll(dir)
	}

	return nil
}

func main() {
	rawSizes := flag.String("sizes", "1000,10000,100000", "comma-separated dataset sizes")
	repeats := flag.Int("repeats", 3, "repetitions per dataset size")
	queries := flag.Int("queries", 1000, "primary-key searches per repetition")
	deleteFraction := flag.Float64("delete-fraction", 0.35, "fraction of records deleted before maintenance/reorganization test")
	logicalBytes := flag.Int("record-bytes", 64, "logical record bytes including 8-byte primary key")
	heapPageSize := flag.Int("heap-page-size", 4096, "HeapFile page size")
	seqCapacity := flag.Int("seq-page-capacity", 0, "SequentialFile records per main page; 0 approximates heap-page-size")
	seed := flag.Int64("seed", 20261004, "deterministic seed")
	resultsDir := flag.String("results", "benchmarks/storage/results", "output directory")
	flag.Parse()

	sizes, err := parseSizes(*rawSizes)
	if err != nil {
		panic(err)
	}
	if *logicalBytes < 9 {
		panic("record-bytes must be >= 9")
	}
	bodyBytes := *logicalBytes - 8
	capacity := *seqCapacity
	if capacity <= 0 {
		capacity = (*heapPageSize - seqPageHeaderBytes) / (seqSlotHeaderBytes + bodyBytes)
		if capacity < 1 {
			capacity = 1
		}
	}

	cfg := config{
		sizes:           sizes,
		repeats:         *repeats,
		queries:         *queries,
		deleteFraction:  *deleteFraction,
		logicalBytes:    *logicalBytes,
		heapPageSize:    *heapPageSize,
		seqPageCapacity: capacity,
		seed:            *seed,
		resultsDir:      *resultsDir,
	}

	if err := os.RemoveAll(cfg.resultsDir); err != nil {
		panic(err)
	}
	if err := os.MkdirAll(cfg.resultsDir, 0o755); err != nil {
		panic(err)
	}

	insertion, err := newCSV(filepath.Join(cfg.resultsDir, "insertion.csv"), []string{"n", "repeat", "structure", "time_ms", "ns_per_record"})
	if err != nil {
		panic(err)
	}
	defer insertion.close()

	search, err := newCSV(filepath.Join(cfg.resultsDir, "search.csv"), []string{"n", "repeat", "structure", "queries", "total_ms", "avg_us"})
	if err != nil {
		panic(err)
	}
	defer search.close()

	storage, err := newCSV(filepath.Join(cfg.resultsDir, "storage.csv"), []string{"n", "repeat", "structure", "total_bytes", "main_bytes", "overflow_bytes", "bytes_per_record"})
	if err != nil {
		panic(err)
	}
	defer storage.close()

	maintenance, err := newCSV(filepath.Join(cfg.resultsDir, "reorganization.csv"), []string{"n", "repeat", "structure", "phase", "deleted_records", "time_ms"})
	if err != nil {
		panic(err)
	}
	defer maintenance.close()

	fmt.Printf("Storage benchmark: sizes=%v repeats=%d queries=%d delete_fraction=%.2f\n", cfg.sizes, cfg.repeats, cfg.queries, cfg.deleteFraction)
	fmt.Printf("Heap page=%d bytes | Sequential page capacity=%d | logical record=%d bytes\n", cfg.heapPageSize, cfg.seqPageCapacity, cfg.logicalBytes)

	for _, n := range cfg.sizes {
		fmt.Printf("\n=== N=%d ===\n", n)
		for rep := 0; rep < cfg.repeats; rep++ {
			fmt.Printf("  repetición %d/%d: inserción+espacio...\n", rep+1, cfg.repeats)
			if err := benchmarkInsertionAndStorage(cfg, n, rep, insertion, storage); err != nil {
				panic(err)
			}

			fmt.Printf("  repetición %d/%d: búsqueda...\n", rep+1, cfg.repeats)
			if err := benchmarkSearch(cfg, n, rep, search); err != nil {
				panic(err)
			}

			fmt.Printf("  repetición %d/%d: mantenimiento/reorganización...\n", rep+1, cfg.repeats)
			if err := benchmarkMaintenance(cfg, n, rep, maintenance); err != nil {
				panic(err)
			}
		}
		fmt.Printf("N=%d completado.\n", n)
	}

	fmt.Printf("\nResultados en %s\n", cfg.resultsDir)
}
