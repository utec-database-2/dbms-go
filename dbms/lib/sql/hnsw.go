package sql

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/dbms-go/v2/dbms/lib/dsl/ast"
	hnswpkg "github.com/dbms-go/v2/dbms/lib/index/hnsw"
	"github.com/dbms-go/v2/dbms/lib/storage"
	metricpkg "github.com/dbms-go/v2/dbms/lib/vector/metric"
)

// hnswConfig contiene las opciones del índice HNSW expuestas por SQL.
type hnswConfig struct {
	EfConstruction int
	EfSearch       int
	M              int
	Ml             float64
	Metric         metricpkg.Metric
}

func defaultHNSWConfig() hnswConfig {
	cfg := hnswpkg.DefaultIndexConfig(hnswpkg.Euclidean)
	return hnswConfig{
		EfConstruction: cfg.EfConstruction,
		EfSearch:       cfg.EfSearch,
		M:              cfg.M,
		Ml:             cfg.Ml,
		Metric:         metricpkg.Euclidean,
	}
}

func parseHNSWConfig(n *ast.CreateIndex) (hnswConfig, error) {
	cfg := defaultHNSWConfig()
	for key, node := range n.Options {
		switch strings.ToLower(key) {
		case "ef_construction", "efconstruction":
			v, err := optionInt(node)
			if err != nil {
				return cfg, fmt.Errorf("sql: opción ef_construction: %w", err)
			}
			cfg.EfConstruction = v
		case "ef_search", "efsearch":
			v, err := optionInt(node)
			if err != nil {
				return cfg, fmt.Errorf("sql: opción ef_search: %w", err)
			}
			cfg.EfSearch = v
		case "m":
			v, err := optionInt(node)
			if err != nil {
				return cfg, fmt.Errorf("sql: opción m: %w", err)
			}
			cfg.M = v
		case "ml":
			v, err := optionFloat(node)
			if err != nil {
				return cfg, fmt.Errorf("sql: opción ml: %w", err)
			}
			cfg.Ml = v
		case "metric":
			v, err := optionString(node)
			if err != nil {
				return cfg, fmt.Errorf("sql: opción metric: %w", err)
			}
			m, err := parseVectorMetric(v)
			if err != nil {
				return cfg, err
			}
			cfg.Metric = m
		default:
			return cfg, fmt.Errorf("sql: opción HNSW desconocida %q", key)
		}
	}
	if cfg.EfConstruction < 1 || cfg.EfSearch < 1 || cfg.M < 1 || cfg.Ml <= 0 || cfg.Ml >= 1 {
		return cfg, fmt.Errorf("sql: configuración HNSW inválida")
	}
	return cfg, nil
}

func optionFloat(node ast.ASTNode) (float64, error) {
	switch n := node.(type) {
	case *ast.IntExpr:
		return float64(n.Value), nil
	case *ast.FloatExpr:
		return n.Value, nil
	case *ast.StringExpr:
		var value float64
		_, err := fmt.Sscan(strings.TrimSpace(n.Value), &value)
		return value, err
	case *ast.IdExpr:
		var value float64
		_, err := fmt.Sscan(strings.TrimSpace(n.Name), &value)
		return value, err
	default:
		return 0, fmt.Errorf("se esperaba un número")
	}
}

func hnswMetricName(m metricpkg.Metric) string { return vectorMetricName(m) }

func toHNSWMetric(m metricpkg.Metric) hnswpkg.Metric {
	switch m {
	case metricpkg.Cosine:
		return hnswpkg.Cosine
	case metricpkg.DotProduct:
		return hnswpkg.ProductDot
	default:
		return hnswpkg.Euclidean
	}
}

type hnswRef struct {
	rid storage.RID
	pk  storage.Tuple
}

type hnswIndex struct {
	baseIndex
	pos    int
	dim    int
	config hnswConfig
	path   string
	index  *hnswpkg.HNSWIndex

	mu     sync.RWMutex
	refs   map[uint64]hnswRef
	byPK   map[string]uint64
	nextID uint64
}

var _ Index = (*hnswIndex)(nil)

func newHNSWIndex(name, col string, tbl *Table, path string, cfg hnswConfig) (Index, error) {
	pos, ok := tbl.ColumnIndex(col)
	if !ok {
		return nil, fmt.Errorf("sql: la columna %s no existe en %s", col, tbl.Name())
	}
	if tbl.Schema.Types[pos] != storage.TypeVector {
		return nil, fmt.Errorf("sql: el índice HNSW necesita una columna VECTOR, %s es %s", col, tbl.Schema.Types[pos])
	}
	dim := tbl.Schema.MaxLen[pos]
	if dim <= 0 {
		return nil, fmt.Errorf("sql: la columna %s no tiene dimensión vectorial", col)
	}
	index, err := hnswpkg.OpenWithConfig(path, hnswpkg.IndexConfig{
		Metric:         toHNSWMetric(cfg.Metric),
		EfConstruction: cfg.EfConstruction,
		EfSearch:       cfg.EfSearch,
		M:              cfg.M,
		Ml:             cfg.Ml,
	})
	if err != nil {
		return nil, err
	}
	idx := &hnswIndex{
		baseIndex: baseIndex{name: name, kind: HNSWIndexKind, col: tbl.Schema.Columns[pos], table: tbl},
		pos:       pos, dim: dim, config: cfg, path: path, index: index,
		refs: make(map[uint64]hnswRef), byPK: make(map[string]uint64),
	}
	if err := idx.Rebuild(); err != nil {
		_ = index.Close()
		return nil, err
	}
	return idx, nil
}

func (idx *hnswIndex) SupportsRange() bool            { return false }
func (idx *hnswIndex) vectorDim() int                 { return idx.dim }
func (idx *hnswIndex) vectorMetric() metricpkg.Metric { return idx.config.Metric }
func (idx *hnswIndex) searchParameter() int           { return idx.config.EfSearch }
func (idx *hnswIndex) vectorKind() string             { return "HNSW" }

func (idx *hnswIndex) vectorOf(row storage.Tuple) ([]float32, error) {
	if idx.pos >= len(row) || row[idx.pos] == nil {
		return nil, nil
	}
	vec, ok := row[idx.pos].([]float32)
	if !ok || len(vec) != idx.dim {
		return nil, fmt.Errorf("sql: el vector de %s no tiene dimensión %d", idx.col, idx.dim)
	}
	for _, value := range vec {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil, fmt.Errorf("sql: el vector de %s contiene NaN o Inf", idx.col)
		}
	}
	return vec, nil
}

func (idx *hnswIndex) pkKeyOf(row storage.Tuple) (string, error) {
	key, err := idx.table.Schema.EncodeKey(idx.table.Schema.KeyOf(row))
	return string(key), err
}

func (idx *hnswIndex) Insert(row storage.Tuple, rid storage.RID) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	vec, err := idx.vectorOf(row)
	if err != nil || vec == nil {
		return err
	}
	key, err := idx.pkKeyOf(row)
	if err != nil {
		return err
	}
	if old, ok := idx.byPK[key]; ok {
		_ = idx.index.Delete(old)
		delete(idx.refs, old)
	}
	id := idx.nextID
	idx.nextID++
	if err := idx.index.Add(id, vec); err != nil {
		return err
	}
	idx.refs[id] = hnswRef{rid: rid, pk: idx.table.Schema.KeyOf(row)}
	idx.byPK[key] = id
	return nil
}

func (idx *hnswIndex) DeleteRID(_ storage.RID, row storage.Tuple) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	key, err := idx.pkKeyOf(row)
	if err != nil {
		return err
	}
	id, ok := idx.byPK[key]
	if !ok {
		return nil
	}
	if !idx.index.Delete(id) {
		return nil
	}
	delete(idx.refs, id)
	delete(idx.byPK, key)
	return idx.rebuildGraphLocked()
}

func (idx *hnswIndex) UpdateRID(rid storage.RID, prev, next storage.Tuple) error {
	if err := idx.DeleteRID(rid, prev); err != nil {
		return err
	}
	if err := idx.Insert(next, rid); err != nil {
		return err
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.rebuildGraphLocked()
}

func (idx *hnswIndex) Rebuild() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if err := idx.index.Reset(); err != nil {
		return err
	}
	idx.refs = make(map[uint64]hnswRef)
	idx.byPK = make(map[string]uint64)
	idx.nextID = 0
	return idx.scanRowsLocked()
}

func (idx *hnswIndex) scanRowsLocked() error {
	add := func(row storage.Tuple, rid storage.RID) error {
		vec, err := idx.vectorOf(row)
		if err != nil || vec == nil {
			return err
		}
		key, err := idx.pkKeyOf(row)
		if err != nil {
			return err
		}
		id := idx.nextID
		idx.nextID++
		if err := idx.index.Add(id, vec); err != nil {
			return err
		}
		idx.refs[id] = hnswRef{rid: rid, pk: idx.table.Schema.KeyOf(row)}
		idx.byPK[key] = id
		return nil
	}
	if idx.table.Clustered {
		return idx.table.seq.Scan(func(row storage.Tuple) bool { return add(row, storage.RID{}) == nil })
	}
	return idx.table.heap.Scan(func(rid storage.RID, row storage.Tuple) bool { return add(row, rid) == nil })
}

func (idx *hnswIndex) rebuildGraphLocked() error {
	if err := idx.index.Reset(); err != nil {
		return err
	}
	ids := make([]uint64, 0, len(idx.refs))
	for id := range idx.refs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		row, err := idx.resolveRef(idx.refs[id])
		if err != nil {
			return err
		}
		vec, err := idx.vectorOf(row)
		if err != nil {
			return err
		}
		if vec != nil {
			if err := idx.index.Add(id, vec); err != nil {
				return err
			}
		}
	}
	return nil
}

func (idx *hnswIndex) Query(query []float32, topK, efSearch int) ([]storage.Tuple, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if topK <= 0 {
		return nil, nil
	}
	searchK := topK
	if len(idx.refs) > searchK {
		searchK = len(idx.refs)
	}
	results, err := idx.index.KNNSearch(query, searchK, efSearch)
	if err != nil {
		return nil, err
	}
	rows := make([]storage.Tuple, 0, len(results))
	for _, result := range results {
		ref, ok := idx.refs[result.ID]
		if !ok {
			continue
		}
		row, err := idx.resolveRef(ref)
		if err != nil {
			if errors.Is(err, storage.ErrRecordNotFound) {
				continue
			}
			return nil, err
		}
		rows = append(rows, row)
		if len(rows) == topK {
			break
		}
	}
	return rows, nil
}

func (idx *hnswIndex) resolveRef(ref hnswRef) (storage.Tuple, error) {
	if idx.table.Clustered {
		return idx.table.seq.Search(ref.pk)
	}
	return idx.table.heap.Get(ref.rid)
}

func (idx *hnswIndex) SearchExact(any) ([]storage.Tuple, error) {
	return nil, fmt.Errorf("sql: el índice HNSW %s no admite búsquedas exactas", idx.name)
}
func (idx *hnswIndex) SearchRange(any, any) ([]storage.Tuple, error) {
	return nil, fmt.Errorf("sql: el índice HNSW %s no admite búsquedas por rango", idx.name)
}
func (idx *hnswIndex) Entries() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.refs)
}
func (idx *hnswIndex) options() hnswConfig { return idx.config }
func (idx *hnswIndex) Close() error        { return idx.index.Close() }

func hnswIndexPath(dir, table, name string) string {
	return filepath.Join(dir, strings.ToLower(table)+"_"+strings.ToLower(name)+".hnsw")
}
