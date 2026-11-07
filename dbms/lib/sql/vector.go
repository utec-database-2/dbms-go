package sql

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/dbms-go/v2/dbms/lib/dsl/ast"
	ivfpkg "github.com/dbms-go/v2/dbms/lib/index/ivf"
	"github.com/dbms-go/v2/dbms/lib/storage"
	metricpkg "github.com/dbms-go/v2/dbms/lib/vector/metric"
)

const (
	// defaultIVFNProbe es conservador: una sola lista. Se puede subir con
	// WITH (nprobe = ...).
	defaultIVFNProbe = 1
)

type vectorKNNIndex interface {
	Query(query []float32, topK, searchParameter int) ([]storage.Tuple, error)
	vectorDim() int
	vectorMetric() metricpkg.Metric
	searchParameter() int
	vectorKind() string
	Name() string
	Entries() int
}

// ivfConfig son las opciones de un índice IVF. NList <= 0 pide el valor por
// defecto, calculado a partir del número de filas.
type ivfConfig struct {
	NList  int
	NProbe int
	Metric metricpkg.Metric
	// K es el tamaño del codebook; solo lo usa el índice IMAGE.
	K int
}

// parseIVFConfig traduce las opciones del AST. Los valores admiten tanto
// literales ('cosine', 16) como identificadores sin comillas (cosine).
func parseIVFConfig(n *ast.CreateIndex) (ivfConfig, error) {
	cfg := ivfConfig{Metric: metricpkg.Euclidean, NProbe: defaultIVFNProbe}
	for key, node := range n.Options {
		switch strings.ToLower(key) {
		case "nlist":
			v, err := optionInt(node)
			if err != nil {
				return cfg, fmt.Errorf("sql: opción nlist: %w", err)
			}
			cfg.NList = v
		case "nprobe":
			v, err := optionInt(node)
			if err != nil {
				return cfg, fmt.Errorf("sql: opción nprobe: %w", err)
			}
			cfg.NProbe = v
		case "k":
			v, err := optionInt(node)
			if err != nil {
				return cfg, fmt.Errorf("sql: opción k: %w", err)
			}
			if v <= 0 {
				return cfg, fmt.Errorf("sql: k debe ser positivo")
			}
			cfg.K = v
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
			return cfg, fmt.Errorf("sql: opción de índice desconocida %q", key)
		}
	}
	if cfg.NList < 0 {
		return cfg, fmt.Errorf("sql: nlist debe ser positivo")
	}
	if cfg.NProbe <= 0 {
		cfg.NProbe = defaultIVFNProbe
	}
	if cfg.Metric == metricpkg.DotProduct {
		return cfg, fmt.Errorf("sql: IVF no puede entrenarse con producto punto; use euclidean o cosine")
	}
	if err := metricpkg.Validate(cfg.Metric); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func optionInt(node ast.ASTNode) (int, error) {
	switch n := node.(type) {
	case *ast.IntExpr:
		return n.Value, nil
	case *ast.FloatExpr:
		if math.Trunc(n.Value) != n.Value {
			return 0, fmt.Errorf("se esperaba un entero")
		}
		return int(n.Value), nil
	case *ast.StringExpr:
		v, err := strconv.Atoi(strings.TrimSpace(n.Value))
		if err != nil {
			return 0, fmt.Errorf("%q no es un entero", n.Value)
		}
		return v, nil
	case *ast.IdExpr:
		v, err := strconv.Atoi(strings.TrimSpace(n.Name))
		if err != nil {
			return 0, fmt.Errorf("%q no es un entero", n.Name)
		}
		return v, nil
	}
	return 0, fmt.Errorf("valor no entero")
}

func optionString(node ast.ASTNode) (string, error) {
	switch n := node.(type) {
	case *ast.StringExpr:
		return n.Value, nil
	case *ast.IdExpr:
		return n.Name, nil
	case *ast.IntExpr:
		return strconv.Itoa(n.Value), nil
	}
	return "", fmt.Errorf("se esperaba un texto")
}

func parseVectorMetric(v string) (metricpkg.Metric, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "euclidean", "euclidiana", "euclidiana2", "l2":
		return metricpkg.Euclidean, nil
	case "cosine", "coseno", "cos":
		return metricpkg.Cosine, nil
	case "dot", "dot_product", "dotproduct", "producto_punto", "ip":
		return metricpkg.DotProduct, nil
	}
	return 0, fmt.Errorf("sql: métrica vectorial desconocida %q (use euclidean o cosine)", v)
}

func vectorMetricName(m metricpkg.Metric) string {
	switch m {
	case metricpkg.Cosine:
		return "cosine"
	case metricpkg.DotProduct:
		return "dot_product"
	default:
		return "euclidean"
	}
}

// newIVFIndex construye el adaptador SQL del IVF y lo puebla con las filas que
// ya existen. Si la tabla no tiene vectores, el índice queda en espera y se
// entrena con los primeros INSERT.
func newIVFIndex(name, col string, tbl *Table, cfg ivfConfig) (Index, error) {
	pos, ok := tbl.ColumnIndex(col)
	if !ok {
		return nil, fmt.Errorf("sql: la columna %s no existe en %s", col, tbl.Name())
	}
	if tbl.Schema.Types[pos] != storage.TypeVector {
		return nil, fmt.Errorf("sql: el índice IVF necesita una columna VECTOR, %s es %s",
			col, tbl.Schema.Types[pos])
	}
	dim := tbl.Schema.MaxLen[pos]
	if dim <= 0 {
		return nil, fmt.Errorf("sql: la columna %s no tiene dimensión vectorial", col)
	}
	if cfg.NProbe <= 0 {
		cfg.NProbe = defaultIVFNProbe
	}
	if cfg.NList < 0 {
		return nil, fmt.Errorf("sql: nlist no puede ser negativo")
	}
	if cfg.Metric == metricpkg.DotProduct {
		return nil, fmt.Errorf("sql: IVF no puede entrenar con producto punto")
	}
	idx := &ivfIndex{
		baseIndex: baseIndex{
			name:   name,
			kind:   IVFIndexKind,
			col:    tbl.Schema.Columns[pos],
			unique: false,
			table:  tbl,
		},
		pos:    pos,
		dim:    dim,
		nlist:  cfg.NList,
		nprobe: cfg.NProbe,
		metric: cfg.Metric,
	}
	if err := idx.Rebuild(); err != nil {
		return nil, err
	}
	return idx, nil
}

type ivfRef struct {
	rid storage.RID
	pk  storage.Tuple
}

type ivfPending struct {
	id  uint64
	vec []float32
}

// ivfIndex adapta IVFFlatIndex a la interfaz Index del motor. Guarda la clave
// primaria en cada entrada, igual que el R-Tree, para que un UPDATE del
// secuencial (que cambia el RID sintético) no invalide el mapeo.
type ivfIndex struct {
	baseIndex

	pos    int
	dim    int
	nlist  int
	nprobe int
	metric metricpkg.Metric

	// image, si no es nil, indica un índice IMAGE: la columna guarda la ruta o
	// los bytes de una imagen y el vector se calcula con el codebook.
	image *imageSource

	mu      sync.RWMutex
	index   *ivfpkg.IVFFlatIndex
	pending []ivfPending
	refs    map[uint64]ivfRef
	byPK    map[string]uint64
	nextID  uint64
}

var _ Index = (*ivfIndex)(nil)

func (idx *ivfIndex) SupportsRange() bool            { return false }
func (idx *ivfIndex) vectorDim() int                 { return idx.dim }
func (idx *ivfIndex) vectorMetric() metricpkg.Metric { return idx.metric }
func (idx *ivfIndex) searchParameter() int           { return idx.nprobe }
func (idx *ivfIndex) vectorKind() string {
	if idx.image != nil {
		return "IMAGE"
	}
	return "IVF"
}

func (idx *ivfIndex) resetLocked() {
	idx.index = nil
	idx.pending = nil
	idx.refs = make(map[uint64]ivfRef)
	idx.byPK = make(map[string]uint64)
	idx.nextID = 0
}

func (idx *ivfIndex) vectorOf(row storage.Tuple) ([]float32, error) {
	if idx.pos >= len(row) {
		return nil, fmt.Errorf("sql: fila con %d columnas, se esperaba la %d", len(row), idx.pos)
	}
	if row[idx.pos] == nil {
		return nil, nil // una columna vectorial nula no se indexa
	}
	var vec []float32
	if idx.image != nil {
		v, err := idx.image.vector(row[idx.pos])
		if err != nil {
			return nil, err
		}
		vec = v
	} else {
		v, ok := row[idx.pos].([]float32)
		if !ok {
			return nil, fmt.Errorf("sql: la columna %s no contiene un vector", idx.col)
		}
		vec = v
	}
	if len(vec) != idx.dim {
		return nil, fmt.Errorf("sql: el vector de %s tiene %d dimensiones, se esperaban %d",
			idx.col, len(vec), idx.dim)
	}
	for _, component := range vec {
		if math.IsNaN(float64(component)) || math.IsInf(float64(component), 0) {
			return nil, fmt.Errorf("sql: el vector de %s contiene NaN o Inf", idx.col)
		}
	}
	return vec, nil
}

func (idx *ivfIndex) pkKeyOf(row storage.Tuple) (string, error) {
	key, err := idx.table.Schema.EncodeKey(idx.table.Schema.KeyOf(row))
	if err != nil {
		return "", err
	}
	return string(key), nil
}

func (idx *ivfIndex) Insert(row storage.Tuple, rid storage.RID) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	vec, err := idx.vectorOf(row)
	if err != nil {
		return err
	}
	if vec == nil {
		return nil
	}
	key, err := idx.pkKeyOf(row)
	if err != nil {
		return err
	}

	// La tabla no debería reinsertar una PK existente, pero si ocurre se
	// reemplaza la entrada vieja para no dejar vectores huérfanos.
	if old, ok := idx.byPK[key]; ok {
		if err := idx.removeLocked(old); err != nil {
			return err
		}
	}

	id := idx.nextID
	idx.nextID++
	idx.refs[id] = ivfRef{rid: rid, pk: idx.table.Schema.KeyOf(row)}
	idx.byPK[key] = id

	if idx.index != nil {
		if err := idx.index.Insert(id, vec); err != nil {
			delete(idx.refs, id)
			delete(idx.byPK, key)
			return err
		}
		return nil
	}

	idx.pending = append(idx.pending, ivfPending{id: id, vec: append([]float32(nil), vec...)})
	if idx.nlist <= 0 {
		idx.nlist = defaultNList(len(idx.pending))
	}
	// En una tabla que se creó vacía esperamos a tener suficientes vectores
	// para que el k-means tenga sentido. Mientras tanto Query usa fuerza bruta
	// sobre pending, así que la respuesta sigue siendo exacta.
	if len(idx.pending) >= idx.nlist {
		if err := idx.trainPendingLocked(); err != nil {
			// La entrada recién agregada queda en pending; no se pierde.
			return err
		}
	}
	return nil
}

func (idx *ivfIndex) removeLocked(id uint64) error {
	ref, ok := idx.refs[id]
	if !ok {
		return nil
	}
	if idx.index != nil {
		if err := idx.index.Delete(id); err != nil && !errors.Is(err, ivfpkg.ErrIDNotFound) {
			return err
		}
	} else {
		for i := range idx.pending {
			if idx.pending[i].id == id {
				idx.pending = append(idx.pending[:i], idx.pending[i+1:]...)
				break
			}
		}
	}
	delete(idx.refs, id)
	// ref.pk ya es la tupla de clave primaria completa. Volver a aplicar KeyOf
	// la reordenaría mal cuando la PK tiene más de una columna.
	if key, err := storage.EncodeKey(ref.pk); err == nil {
		delete(idx.byPK, string(key))
	}
	// Si la tabla quedó sin vectores, se descarta el entrenamiento viejo: los
	// próximos INSERT vuelven a formar pending y entrenan con datos actuales.
	if len(idx.refs) == 0 {
		idx.index = nil
		idx.pending = nil
	}
	return nil
}

func (idx *ivfIndex) DeleteRID(_ storage.RID, row storage.Tuple) error {
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
	return idx.removeLocked(id)
}

func (idx *ivfIndex) UpdateRID(rid storage.RID, prev, next storage.Tuple) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	key, err := idx.pkKeyOf(prev)
	if err != nil {
		return err
	}
	// Valida el valor nuevo antes de despublicar el viejo: si el vector es
	// inválido, el índice sigue siendo consistente con la fila anterior.
	vec, err := idx.vectorOf(next)
	if err != nil {
		return err
	}
	var newKey string
	if vec != nil {
		newKey, err = idx.pkKeyOf(next)
		if err != nil {
			return err
		}
	}

	if id, ok := idx.byPK[key]; ok {
		if err := idx.removeLocked(id); err != nil {
			return err
		}
	}
	if vec == nil {
		return nil
	}

	// Inserta el valor nuevo sin volver a tomar el lock.
	id := idx.nextID
	idx.nextID++
	idx.refs[id] = ivfRef{rid: rid, pk: idx.table.Schema.KeyOf(next)}
	idx.byPK[newKey] = id
	if idx.index != nil {
		if err := idx.index.Insert(id, vec); err != nil {
			delete(idx.refs, id)
			delete(idx.byPK, newKey)
			return err
		}
		return nil
	}
	idx.pending = append(idx.pending, ivfPending{id: id, vec: append([]float32(nil), vec...)})
	if idx.nlist <= 0 {
		idx.nlist = defaultNList(len(idx.pending))
	}
	if len(idx.pending) >= idx.nlist {
		return idx.trainPendingLocked()
	}
	return nil
}

func (idx *ivfIndex) trainPendingLocked() error {
	if len(idx.pending) == 0 {
		return nil
	}
	nlist := idx.nlist
	if nlist <= 0 {
		nlist = defaultNList(len(idx.pending))
	}
	if nlist > len(idx.pending) {
		nlist = len(idx.pending)
	}
	if nlist <= 0 {
		return nil
	}
	vectors := make([][]float32, len(idx.pending))
	for i, p := range idx.pending {
		vectors[i] = p.vec
	}
	trained, err := ivfpkg.NewIVFFlatIndex(idx.dim, nlist, idx.metric)
	if err != nil {
		return err
	}
	if err := trained.Train(vectors); err != nil {
		return err
	}
	for _, p := range idx.pending {
		if err := trained.Insert(p.id, p.vec); err != nil {
			return err
		}
	}
	idx.index = trained
	idx.nlist = nlist
	idx.pending = nil
	return nil
}

// Rebuild reconstruye el índice desde el almacenamiento. En la Opción B el
// vector ya vive en la tabla, así que no hace falta OpenCV ni un codebook: se
// reentrena de forma determinista con los vectores actuales.
func (idx *ivfIndex) Rebuild() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	idx.resetLocked()

	type rowRef struct {
		row storage.Tuple
		rid storage.RID
	}
	var rows []rowRef
	if idx.table.Clustered {
		if idx.table.seq == nil {
			return fmt.Errorf("sql: la tabla %s no tiene almacenamiento", idx.table.Name())
		}
		if err := idx.table.seq.Scan(func(row storage.Tuple) bool {
			rows = append(rows, rowRef{row: row})
			return true
		}); err != nil {
			return err
		}
	} else {
		if idx.table.heap == nil {
			return fmt.Errorf("sql: la tabla %s no tiene almacenamiento", idx.table.Name())
		}
		if err := idx.table.heap.Scan(func(rid storage.RID, row storage.Tuple) bool {
			rows = append(rows, rowRef{row: row, rid: rid})
			return true
		}); err != nil {
			return err
		}
	}

	var vectors [][]float32
	for _, rr := range rows {
		vec, err := idx.vectorOf(rr.row)
		if err != nil {
			return err
		}
		if vec == nil {
			continue
		}
		key, err := idx.pkKeyOf(rr.row)
		if err != nil {
			return err
		}
		id := idx.nextID
		idx.nextID++
		idx.refs[id] = ivfRef{rid: rr.rid, pk: idx.table.Schema.KeyOf(rr.row)}
		idx.byPK[key] = id
		idx.pending = append(idx.pending, ivfPending{id: id, vec: append([]float32(nil), vec...)})
		vectors = append(vectors, vec)
	}
	if len(vectors) == 0 {
		return nil
	}
	if idx.nlist <= 0 {
		idx.nlist = defaultNList(len(vectors))
	}
	if idx.nlist > len(vectors) {
		idx.nlist = len(vectors)
	}
	return idx.trainPendingLocked()
}

// Query devuelve las filas más cercanas, ya ordenadas por distancia. Si el
// índice todavía no se ha entrenado (tabla pequeña o vacía), usa fuerza bruta
// sobre los vectores pendientes.
func (idx *ivfIndex) Query(query []float32, topK, nprobes int) ([]storage.Tuple, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if len(query) != idx.dim {
		return nil, fmt.Errorf("sql: la consulta tiene %d dimensiones y el índice espera %d", len(query), idx.dim)
	}
	if topK <= 0 {
		return nil, nil
	}
	if idx.index == nil {
		return idx.queryPendingLocked(query, topK)
	}
	if nprobes <= 0 {
		nprobes = idx.nprobe
	}
	candidates, err := idx.index.Query(query, topK, nprobes)
	if err != nil {
		return nil, err
	}
	rows := make([]storage.Tuple, 0, len(candidates))
	for _, c := range candidates {
		ref, ok := idx.refs[c.ID]
		if !ok {
			continue
		}
		row, err := idx.resolveRefLocked(ref)
		if err != nil {
			if errors.Is(err, storage.ErrRecordNotFound) {
				continue
			}
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (idx *ivfIndex) queryPendingLocked(query []float32, topK int) ([]storage.Tuple, error) {
	type candidate struct {
		id   uint64
		dist float64
	}
	if topK > len(idx.pending) {
		topK = len(idx.pending)
	}
	candidates := make([]candidate, 0, topK)
	for _, p := range idx.pending {
		c := candidate{id: p.id, dist: metricpkg.Distance(query, p.vec, idx.metric)}
		if len(candidates) < topK {
			candidates = append(candidates, c)
			continue
		}
		// Mantiene solo los topK: reemplaza el peor sin materializar todos
		// los candidatos pendientes.
		worst := 0
		for i := 1; i < len(candidates); i++ {
			if candidates[i].dist > candidates[worst].dist ||
				(candidates[i].dist == candidates[worst].dist && candidates[i].id > candidates[worst].id) {
				worst = i
			}
		}
		if c.dist < candidates[worst].dist ||
			(c.dist == candidates[worst].dist && c.id < candidates[worst].id) {
			candidates[worst] = c
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].dist == candidates[j].dist {
			return candidates[i].id < candidates[j].id
		}
		return candidates[i].dist < candidates[j].dist
	})
	rows := make([]storage.Tuple, 0, len(candidates))
	for _, c := range candidates {
		ref, ok := idx.refs[c.id]
		if !ok {
			continue
		}
		row, err := idx.resolveRefLocked(ref)
		if err != nil {
			if errors.Is(err, storage.ErrRecordNotFound) {
				continue
			}
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (idx *ivfIndex) resolveRefLocked(ref ivfRef) (storage.Tuple, error) {
	if idx.table.Clustered {
		if idx.table.seq == nil {
			return nil, fmt.Errorf("sql: la tabla %s no tiene secuencial", idx.table.Name())
		}
		return idx.table.seq.Search(ref.pk)
	}
	if idx.table.heap == nil {
		return nil, fmt.Errorf("sql: la tabla %s no tiene heap", idx.table.Name())
	}
	return idx.table.heap.Get(ref.rid)
}

func (idx *ivfIndex) SearchExact(any) ([]storage.Tuple, error) {
	return nil, fmt.Errorf("sql: el índice IVF %s no admite búsquedas exactas", idx.name)
}

func (idx *ivfIndex) SearchRange(any, any) ([]storage.Tuple, error) {
	return nil, fmt.Errorf("sql: el índice IVF %s no admite búsquedas por rango", idx.name)
}

func (idx *ivfIndex) Entries() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.refs)
}

// options devuelve una foto consistente de la configuración mutable (nlist se
// ajusta al entrenar). Evita carreras con INSERT/REBUILD.
func (idx *ivfIndex) options() (nlist, nprobe int, metric metricpkg.Metric) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.nlist, idx.nprobe, idx.metric
}

func (idx *ivfIndex) Close() error { return nil }

func defaultNList(rows int) int {
	if rows <= 0 {
		return 1
	}
	n := int(math.Sqrt(float64(rows)))
	if n < 1 {
		n = 1
	}
	if n > 4096 {
		n = 4096
	}
	return n
}

// ivfOn devuelve el índice IVF de una columna, si existe.
func (t *Table) ivfOn(col int) (*ivfIndex, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, name := range t.order {
		if idx, ok := t.indexes[name].(*ivfIndex); ok && idx.pos == col {
			return idx, true
		}
	}
	return nil, false
}

func (t *Table) hnswOn(col int) (*hnswIndex, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, name := range t.order {
		if idx, ok := t.indexes[name].(*hnswIndex); ok && idx.pos == col {
			return idx, true
		}
	}
	return nil, false
}

func (t *Table) vectorOn(col int) (vectorKNNIndex, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, name := range t.order {
		switch idx := t.indexes[name].(type) {
		case *ivfIndex:
			if idx.pos == col {
				return idx, true
			}
		case *hnswIndex:
			if idx.pos == col {
				return idx, true
			}
		}
	}
	return nil, false
}

// ---------------------------------------------------------------------------
// Literales, funciones y planner vectorial
// ---------------------------------------------------------------------------

func isVectorDistanceFunc(name string) bool {
	switch strings.ToLower(name) {
	case "vector_distance", "distancia_vector", "vector_dist", "knn_distance":
		return true
	}
	return false
}

func toVector32(v any) ([]float32, error) {
	switch x := v.(type) {
	case []float32:
		return x, nil
	case []float64:
		out := make([]float32, len(x))
		for i, component := range x {
			out[i] = float32(component)
		}
		return out, nil
	case string:
		return parseVectorString(x)
	}
	return nil, fmt.Errorf("sql: %T no es un vector", v)
}

// parseVectorString acepta "[1, 2, 3]", "1 2 3" y "1,2,3".
func parseVectorString(raw string) ([]float32, error) {
	body := strings.TrimSpace(raw)
	body = strings.TrimPrefix(body, "[")
	body = strings.TrimSuffix(body, "]")
	body = strings.TrimPrefix(body, "(")
	body = strings.TrimSuffix(body, ")")
	parts := strings.FieldsFunc(body, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == ';'
	})
	if len(parts) == 0 {
		return nil, fmt.Errorf("sql: vector vacío")
	}
	vec := make([]float32, len(parts))
	for i, part := range parts {
		value, err := strconv.ParseFloat(part, 32)
		if err != nil {
			return nil, fmt.Errorf("sql: componente vectorial inválida %q", part)
		}
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("sql: componente vectorial inválida %q", part)
		}
		vec[i] = float32(value)
	}
	return vec, nil
}

// evalVectorFunc evalúa VECTOR(...) y vector_distance(...).
func (ec *evalContext) evalVectorFunc(fn *ast.FuncCallExpr) (any, bool, error) {
	name := strings.ToLower(fn.Name)
	switch name {
	case "vector", "vector_literal":
		if len(fn.Args) == 0 {
			return nil, true, fmt.Errorf("sql: VECTOR necesita al menos una componente")
		}
		if len(fn.Args) == 1 {
			v, err := ec.eval(fn.Args[0])
			if err != nil {
				return nil, true, err
			}
			if vec, err := toVector32(v); err == nil {
				return vec, true, nil
			}
			// Un único número también puede ser un vector de dimensión 1.
		}
		vec := make([]float32, len(fn.Args))
		for i, arg := range fn.Args {
			v, err := ec.eval(arg)
			if err != nil {
				return nil, true, err
			}
			f, ok := toFloat64(v)
			if !ok {
				return nil, true, fmt.Errorf("sql: la componente %d de VECTOR no es numérica", i)
			}
			vec[i] = float32(f)
		}
		return vec, true, nil

	case "image_vector", "vector_imagen":
		if len(fn.Args) != 1 {
			return nil, true, fmt.Errorf("sql: IMAGE_VECTOR espera (ruta | bytes)")
		}
		v, err := ec.eval(fn.Args[0])
		if err != nil {
			return nil, true, err
		}
		vec, err := ec.evalImageVector(v)
		return vec, true, err

	case "vector_distance", "distancia_vector", "vector_dist", "knn_distance":
		if len(fn.Args) != 2 && len(fn.Args) != 3 {
			return nil, true, fmt.Errorf("sql: vector_distance espera (vector, vector [, métrica])")
		}
		args := make([]any, 2)
		for i := 0; i < 2; i++ {
			v, err := ec.eval(fn.Args[i])
			if err != nil {
				return nil, true, err
			}
			args[i] = v
		}
		a, err := ec.vectorArg(args[0])
		if err != nil {
			return nil, true, err
		}
		b, err := ec.vectorArg(args[1])
		if err != nil {
			return nil, true, err
		}
		if len(a) != len(b) {
			return nil, true, fmt.Errorf("sql: vectores de distinta dimensión (%d y %d)", len(a), len(b))
		}
		metric := metricpkg.Euclidean
		if len(fn.Args) == 3 {
			value, err := ec.evalMetricArg(fn.Args[2])
			if err != nil {
				return nil, true, err
			}
			metric, err = parseVectorMetric(value)
			if err != nil {
				return nil, true, err
			}
		}
		return metricpkg.Distance(a, b, metric), true, nil
	}
	return nil, false, nil
}

func optionStringFromValue(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case []byte:
		return string(x), nil
	}
	return "", fmt.Errorf("sql: se esperaba una métrica textual")
}

// evalMetricArg acepta tanto 'cosine' como cosine sin comillas.
func (ec *evalContext) evalMetricArg(node ast.ASTNode) (string, error) {
	if id, ok := node.(*ast.IdExpr); ok {
		return id.Name, nil
	}
	v, err := ec.eval(node)
	if err != nil {
		return "", err
	}
	return optionStringFromValue(v)
}

// vectorArgs reconoce vector_distance(columna, vector [, métrica]).
func (t *Table) vectorArgs(fn *ast.FuncCallExpr) (vectorKNNIndex, []float32, metricpkg.Metric, bool, error) {
	if len(fn.Args) < 2 {
		return nil, nil, 0, false, nil
	}
	id, ok := fn.Args[0].(*ast.IdExpr)
	if !ok {
		return nil, nil, 0, false, nil
	}
	col, ok := t.ColumnIndex(id.Name)
	if !ok {
		return nil, nil, 0, false, nil
	}
	idx, ok := t.vectorOn(col)
	if !ok {
		return nil, nil, 0, false, nil
	}
	ec := &evalContext{table: t}
	value, err := ec.eval(fn.Args[1])
	if err != nil {
		return nil, nil, 0, false, err
	}
	vec, err := toVector32(value)
	if err != nil {
		return nil, nil, 0, false, err
	}
	if len(vec) != idx.vectorDim() {
		return nil, nil, 0, false, fmt.Errorf("sql: la consulta tiene %d dimensiones y el índice espera %d", len(vec), idx.vectorDim())
	}
	metric := idx.vectorMetric()
	if len(fn.Args) >= 3 {
		name, err := ec.evalMetricArg(fn.Args[2])
		if err != nil {
			return nil, nil, 0, false, err
		}
		metric, err = parseVectorMetric(name)
		if err != nil {
			return nil, nil, 0, false, err
		}
		if metric != idx.vectorMetric() {
			return nil, nil, 0, false, fmt.Errorf(
				"sql: la métrica de la consulta (%s) no coincide con la del índice (%s)",
				vectorMetricName(metric), vectorMetricName(idx.vectorMetric()))
		}
	}
	return idx, vec, metric, true, nil
}

// planVectorKNN resuelve SELECT ... ORDER BY vector_distance(col, vector)
// [ASC] LIMIT k usando el IVF. Igual que el kNN espacial, solo aplica sin
// WHERE, GROUP BY, JOIN ni agregados; en otro caso se usa el sort externo.
func (e *Engine) planVectorKNN(tbl *Table, n *ast.Select) ([]storage.Tuple, []Step, bool, error) {
	if n.Closure != nil || len(n.GroupBy) > 0 || n.Having != nil || len(n.Join) > 0 ||
		n.OrderBy == nil || n.OrderBy.Descendent || n.Limit == nil || n.Limit.Count == nil ||
		selectHasAggregate(n) {
		return nil, nil, false, nil
	}
	fn, ok := n.OrderBy.Expr.(*ast.FuncCallExpr)
	if !ok || !isVectorDistanceFunc(fn.Name) {
		return nil, nil, false, nil
	}
	idx, vec, metric, ok, err := tbl.vectorArgs(fn)
	if err != nil {
		return nil, nil, false, err
	}
	if !ok {
		return nil, nil, false, nil
	}
	ec := &evalContext{table: tbl}
	cv, err := ec.eval(n.Limit.Count)
	if err != nil {
		return nil, nil, false, err
	}
	k, ok := asInt64(cv)
	if !ok || k < 0 {
		return nil, nil, false, fmt.Errorf("sql: LIMIT inválido")
	}
	offset := int64(0)
	if n.Limit.Offset != nil {
		ov, err := ec.eval(n.Limit.Offset)
		if err != nil {
			return nil, nil, false, err
		}
		if offset, ok = asInt64(ov); !ok || offset < 0 {
			return nil, nil, false, fmt.Errorf("sql: OFFSET inválido")
		}
	}
	searchParameter := idx.searchParameter()
	rows, err := idx.Query(vec, int(k+offset), searchParameter)
	if err != nil {
		return nil, nil, false, err
	}
	if int(offset) >= len(rows) {
		rows = nil
	} else {
		rows = rows[offset:]
	}
	return rows, []Step{{
		Kind: StepIndexScan,
		Detail: fmt.Sprintf("k-NN en índice %s %s: %d vecinos, parámetro=%d (%s, sin ordenar la tabla)",
			idx.vectorKind(), idx.Name(), k, searchParameter, vectorMetricName(metric)),
		Rows: len(rows),
		Cost: len(rows) + idx.Entries()/16,
	}}, true, nil
}
