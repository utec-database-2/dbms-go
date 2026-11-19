package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/dbms-go/v2/dbms/lib/benchclock"
)

type pgStageSample struct {
	Choice  string          `json:"plan_choice"`
	Explain json.RawMessage `json:"explain"`
}

// Referencia de ida/vuelta cliente-servidor en la misma sesión; no se resta
// de ninguna consulta
func finalPGBaseline(ctx context.Context, conn *sql.Conn) ([]byte, error) {
	samples := make([]float64, 0, 30)
	for i := 0; i < 35; i++ {
		start := benchclock.Now()
		var v int
		if err := conn.QueryRowContext(ctx, "SELECT 1").Scan(&v); err != nil {
			return nil, err
		}
		if v != 1 {
			return nil, fmt.Errorf("SELECT 1 inesperado")
		}
		if i >= 5 {
			samples = append(samples, float64(benchclock.Since(start).Nanoseconds())/1e6)
		}
	}
	return json.Marshal(samples)
}

func finalPGProfiles(ctx context.Context, conn *sql.Conn, executeSQL string, repeats int) ([]byte, error) {
	samples := make([]pgStageSample, 0, repeats)
	for i := 0; i < repeats; i++ {
		c0, g0, err := readPreparedCounters(ctx, conn)
		if err != nil {
			return nil, err
		}
		var raw []byte
		if err = conn.QueryRowContext(ctx, "EXPLAIN (ANALYZE, BUFFERS, TIMING ON, FORMAT JSON) "+executeSQL).Scan(&raw); err != nil {
			return nil, err
		}
		if !json.Valid(raw) {
			return nil, fmt.Errorf("EXPLAIN JSON invalido")
		}
		c1, g1, err := readPreparedCounters(ctx, conn)
		if err != nil {
			return nil, err
		}
		if c1-c0+g1-g0 != 1 || c1 < c0 || g1 < g0 {
			return nil, fmt.Errorf("contador de plan EXPLAIN invalido")
		}
		samples = append(samples, pgStageSample{planUsage(c1-c0, g1-g0), json.RawMessage(raw)})
	}
	return json.Marshal(samples)
}
