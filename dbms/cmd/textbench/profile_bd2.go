package main

import (
	"encoding/json"
	"fmt"
	"github.com/dbms-go/v2/dbms/lib/benchclock"
	"github.com/dbms-go/v2/dbms/lib/index/text/ranking"
	"github.com/dbms-go/v2/dbms/lib/index/text/spimi"
	text "github.com/dbms-go/v2/dbms/lib/text"
	"time"
)

type goStageSample struct {
	Batch        int     `json:"batch"`
	PrepMS       float64 `json:"prep_ms"`
	IndexMS      float64 `json:"index_ms"`
	AccumulateMS float64 `json:"accumulate_ms"`
	TopKMS       float64 `json:"topk_ms"`
	TotalMS      float64 `json:"instrumented_total_ms"`
	OtherMS      float64 `json:"other_residual_ms"`
	Candidates   int     `json:"candidates"`
	Postings     int     `json:"postings"`
}

func equalResults(a, b []ranking.SearchResult) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func finalProfile(reader *spimi.Reader, method ranking.Method, q query, opts ranking.SearchOptions, expected []ranking.SearchResult, batch, repeats int) (string, error) {
	// El cronometro principal sigue siendo ranking.Search
	// Separar las ejecuciones de diagnóstico y limitar su costo.
	if batch > 128 {
		batch = 128
	}
	samples := make([]goStageSample, 0, repeats)
	ms := func(d time.Duration) float64 { return float64(d.Nanoseconds()) / 1e6 / float64(batch) }
	for rep := 0; rep < repeats; rep++ {
		var prep, index, accum, top, total time.Duration
		var candidates, postings int
		for i := 0; i < batch; i++ {
			start := benchclock.Now()
			terms, err := text.PreprocessText(q.Text)
			prep += benchclock.Since(start)
			if err != nil {
				return "", err
			}
			if len(terms) != len(q.Tokens) {
				return "", fmt.Errorf("normalizacion distinta")
			}
			for j := range terms {
				if terms[j] != q.Tokens[j] {
					return "", fmt.Errorf("token distinto")
				}
			}
			start = benchclock.Now()
			hits, p, err := ranking.SearchProfiled(reader, method, terms, opts)
			total += benchclock.Since(start)
			if err != nil {
				return "", err
			}
			// El chequeo nunca está dentro de la medición.
			if !equalResults(hits, expected) {
				return "", fmt.Errorf("Top-K o scores cambiaron")
			}
			if i > 0 && (p.Candidates != candidates || p.Postings != postings) {
				return "", fmt.Errorf("conteos inestables")
			}
			candidates, postings = p.Candidates, p.Postings
			index += p.Index
			accum += p.Accumulate
			top += p.TopK
		}
		other := total - index - accum - top
		if other < 0 {
			return "", fmt.Errorf("intervalos de perfil solapados")
		}
		samples = append(samples, goStageSample{batch, ms(prep), ms(index), ms(accum), ms(top), ms(total), ms(other), candidates, postings})
	}
	raw, err := json.Marshal(samples)
	return string(raw), err
}
