package main

import (
	"flag"
	"fmt"
	"strconv"
	"strings"
)

type config struct {
	sizes           []int
	repeats         int
	equalityQueries int
	rangeQueries    int
	rangeFraction   float64
	updates         int
	payloadSize     int
	seqPageCapacity int
	bplusOrder      int
	hashBucketSize  int
	seed            int64
	outDir          string
	workDir         string
}

func parseConfig() (config, error) {
	var sizesRaw string
	cfg := config{}

	flag.StringVar(&sizesRaw, "sizes", "1000,10000,100000", "tamaños de dataset separados por coma")
	flag.IntVar(&cfg.repeats, "repeats", 3, "repeticiones por experimento")
	flag.IntVar(&cfg.equalityQueries, "queries", 1000, "consultas de igualdad por repetición")
	flag.IntVar(&cfg.rangeQueries, "range-queries", 100, "consultas de rango por repetición")
	flag.Float64Var(&cfg.rangeFraction, "range-fraction", 0.01, "fracción del dataset incluida en cada rango (0..1]")
	flag.IntVar(&cfg.updates, "updates", 1000, "inserciones y eliminaciones por repetición")
	flag.IntVar(&cfg.payloadSize, "payload-size", 64, "bytes por payload")
	flag.IntVar(&cfg.seqPageCapacity, "seq-page-capacity", 64, "registros por página principal del SequentialFile")
	flag.IntVar(&cfg.bplusOrder, "bplus-order", 64, "orden del B+")
	flag.IntVar(&cfg.hashBucketSize, "hash-bucket-size", 64, "capacidad objetivo de bucket del Extendible Hash")
	flag.Int64Var(&cfg.seed, "seed", 20261004, "semilla determinista")
	flag.StringVar(&cfg.outDir, "out", "benchmarks/indexes/results", "directorio de CSV")
	flag.StringVar(&cfg.workDir, "work", "benchmarks/indexes/work", "directorio temporal de archivos de datos/índices")
	flag.Parse()

	sizes, err := parseSizes(sizesRaw)
	if err != nil {
		return config{}, err
	}
	cfg.sizes = sizes

	if cfg.repeats < 1 || cfg.equalityQueries < 1 || cfg.rangeQueries < 1 || cfg.updates < 1 {
		return config{}, fmt.Errorf("repeats, queries, range-queries y updates deben ser >= 1")
	}
	if cfg.rangeFraction <= 0 || cfg.rangeFraction > 1 {
		return config{}, fmt.Errorf("range-fraction debe estar en (0,1]")
	}
	if cfg.payloadSize < 8 {
		return config{}, fmt.Errorf("payload-size debe ser >= 8")
	}
	if cfg.seqPageCapacity < 1 || cfg.bplusOrder < 3 || cfg.hashBucketSize < 1 {
		return config{}, fmt.Errorf("parámetros de capacidad/orden inválidos")
	}
	return cfg, nil
}

func parseSizes(raw string) ([]int, error) {
	parts := strings.Split(raw, ",")
	out := make([]int, 0, len(parts))
	seen := map[int]bool{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("tamaño inválido %q", p)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("debe especificarse al menos un tamaño")
	}
	return out, nil
}
