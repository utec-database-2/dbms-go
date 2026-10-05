package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func main() {
	cfg, err := parseConfig()
	if err != nil {
		fatal(err)
	}
	if err := os.MkdirAll(cfg.outDir, 0o755); err != nil {
		fatal(err)
	}
	if err := os.RemoveAll(cfg.workDir); err != nil {
		fatal(err)
	}
	if err := os.MkdirAll(cfg.workDir, 0o755); err != nil {
		fatal(err)
	}

	construction, err := newCSVSink(filepath.Join(cfg.outDir, "construction.csv"), []string{"n", "repeat", "structure", "total_ms", "index_bytes"})
	if err != nil {
		fatal(err)
	}
	defer construction.close()
	equality, err := newCSVSink(filepath.Join(cfg.outDir, "equality.csv"), []string{"n", "repeat", "structure", "queries", "total_ms", "avg_us"})
	if err != nil {
		fatal(err)
	}
	defer equality.close()
	rangeSink, err := newCSVSink(filepath.Join(cfg.outDir, "range.csv"), []string{"n", "repeat", "structure", "mode", "queries", "range_width", "selectivity", "total_ms", "avg_ms"})
	if err != nil {
		fatal(err)
	}
	defer rangeSink.close()
	ordering, err := newCSVSink(filepath.Join(cfg.outDir, "ordering.csv"), []string{"n", "repeat", "structure", "mode", "total_ms"})
	if err != nil {
		fatal(err)
	}
	defer ordering.close()
	storageSink, err := newCSVSink(filepath.Join(cfg.outDir, "storage.csv"), []string{"n", "structure", "index_bytes", "index_mb"})
	if err != nil {
		fatal(err)
	}
	defer storageSink.close()
	updates, err := newCSVSink(filepath.Join(cfg.outDir, "updates.csv"), []string{"n", "repeat", "structure", "operation", "operations", "total_ms", "avg_us"})
	if err != nil {
		fatal(err)
	}
	defer updates.close()

	started := time.Now()
	fmt.Printf("Benchmark de índices: sizes=%v repeats=%d\n", cfg.sizes, cfg.repeats)
	fmt.Printf("CSV -> %s\n", cfg.outDir)

	for _, n := range cfg.sizes {
		fmt.Printf("\n=== N=%d ===\n", n)
		baseDir := filepath.Join(cfg.workDir, fmt.Sprintf("n_%d", n), "base")
		fmt.Println("[1/6] preparando almacenamiento base (fuera de las métricas)...")
		bs, err := createBaseStorage(baseDir, n, cfg)
		if err != nil {
			fatal(err)
		}

		fmt.Println("[2/6] construcción...")
		if err := runConstruction(bs, n, cfg, construction); err != nil {
			bs.close()
			fatal(err)
		}

		fmt.Println("[3/6] creando índices de consulta...")
		li, err := createLiveIndexes(bs, cfg)
		if err != nil {
			bs.close()
			fatal(err)
		}
		if err := runStorage(n, li, storageSink); err != nil {
			li.close()
			bs.close()
			fatal(err)
		}

		fmt.Println("[4/6] igualdad y rango...")
		if err := runEquality(bs, li, n, cfg, equality); err != nil {
			li.close()
			bs.close()
			fatal(err)
		}
		if err := runRange(bs, li, n, cfg, rangeSink); err != nil {
			li.close()
			bs.close()
			fatal(err)
		}

		fmt.Println("[5/6] ordenamiento...")
		if err := runOrdering(bs, li, n, cfg, ordering); err != nil {
			li.close()
			bs.close()
			fatal(err)
		}
		if err := li.close(); err != nil {
			bs.close()
			fatal(err)
		}
		if err := bs.close(); err != nil {
			fatal(err)
		}

		fmt.Println("[6/6] inserciones/eliminaciones frecuentes...")
		if err := runUpdates(filepath.Join(cfg.workDir, fmt.Sprintf("n_%d", n)), n, cfg, updates); err != nil {
			fatal(err)
		}
		fmt.Printf("N=%d completado.\n", n)
	}

	fmt.Printf("\nListo. Duración total: %s\n", time.Since(started).Round(time.Millisecond))
	fmt.Printf("Resultados: %s\n", cfg.outDir)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "ERROR:", err)
	os.Exit(1)
}
