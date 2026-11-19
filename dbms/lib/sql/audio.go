package sql

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dbms-go/v2/dbms/lib/storage"
	vecpkg "github.com/dbms-go/v2/dbms/lib/vector"
	metricpkg "github.com/dbms-go/v2/dbms/lib/vector/metric"
)

const defaultAudioK = 128

type AudioExtractor interface {
	Dim() int
	TrainFromPaths(paths []string) ([]byte, error)
	QueryFromPath(path string) ([]float32, error)
	Vector(raw []byte) ([]float32, error)
	LoadVocabJSON(data []byte) error
}

var newAudioExtractor = func(k int, metric metricpkg.Metric) AudioExtractor {
	return &vecpkg.AudioBoWFeature{K: k, Metric: metric}
}

type audioSource struct {
	ext          AudioExtractor
	k            int
	codebookPath string
}

func (s *audioSource) vector(v any) ([]float32, error) {
	var (
		vec []float32
		err error
	)
	switch x := v.(type) {
	case string:
		vec, err = s.ext.QueryFromPath(x)
	case []byte:
		vec, err = s.ext.Vector(x)
	default:
		return nil, fmt.Errorf("sql: se esperaba una ruta de audio o sus bytes, no %T", v)
	}
	if err != nil {
		return nil, fmt.Errorf("sql: no se pudo extraer features del audio: %w", err)
	}
	if len(vec) != s.k {
		return nil, fmt.Errorf("sql: el extractor de audio devolvió %d dimensiones, se esperaban %d", len(vec), s.k)
	}
	return vec, nil
}

// ---------------------------------------------------------------------------
// Vocabulario de audio en disco
// ---------------------------------------------------------------------------

type audioBookDisk struct {
	K      int             `json:"k"`
	Metric string          `json:"metric"`
	Vocab  json.RawMessage `json:"vocab"`
}

func audioCodebookPath(dir, table, name string) string {
	return filepath.Join(dir, strings.ToLower(table)+"_"+strings.ToLower(name)+".audiobook.json")
}

func saveAudioCodebook(path string, k int, metric metricpkg.Metric, vocabJSON []byte) error {
	raw, err := json.Marshal(audioBookDisk{
		K:      k,
		Metric: vectorMetricName(metric),
		Vocab:  vocabJSON,
	})
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadAudioCodebook(path string, k int, metric metricpkg.Metric, ext AudioExtractor) (bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var disk audioBookDisk
	if err := json.Unmarshal(raw, &disk); err != nil {
		return false, fmt.Errorf("sql: audiobook %s ilegible: %w", path, err)
	}
	if disk.K != k || disk.Metric != vectorMetricName(metric) {
		return false, fmt.Errorf("sql: el audiobook %s (k=%d, %s) no coincide con el índice (k=%d, %s)",
			path, disk.K, disk.Metric, k, vectorMetricName(metric))
	}
	if err := ext.LoadVocabJSON(disk.Vocab); err != nil {
		return false, fmt.Errorf("sql: vocabulario corrupto en %s: %w", path, err)
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// Índice AUDIO
// ---------------------------------------------------------------------------

func (e *Engine) createAudioIndex(tbl *Table, name, col string, cfg ivfConfig) (Index, error) {
	path := audioCodebookPath(e.dbDir(), tbl.Name(), name)
	idx, err := newAudioIndex(name, col, tbl, cfg, path)
	if err != nil {
		return nil, fmt.Errorf("sql: no se pudo crear el índice AUDIO %s: %w", name, err)
	}
	tbl.addIndex(idx)
	return idx, nil
}

func newAudioIndex(name, col string, tbl *Table, cfg ivfConfig, codebookPath string) (Index, error) {
	pos, ok := tbl.ColumnIndex(col)
	if !ok {
		return nil, fmt.Errorf("la columna %s no existe en %s", col, tbl.Name())
	}
	if ty := tbl.Schema.Types[pos]; ty != storage.TypeString && ty != storage.TypeBytes {
		return nil, fmt.Errorf("el índice AUDIO necesita una columna con la ruta del audio (VARCHAR) o sus bytes (BLOB), %s es %s",
			col, ty)
	}
	if len(tbl.Schema.KeyCols) == 0 {
		return nil, fmt.Errorf("el índice AUDIO necesita que %s tenga clave primaria", tbl.Name())
	}
	k := cfg.K
	if k <= 0 {
		k = defaultAudioK
	}
	if cfg.Metric == metricpkg.DotProduct {
		return nil, fmt.Errorf("el vocabulario de audio no puede entrenarse con producto punto; use cosine o euclidean")
	}
	if cfg.NProbe <= 0 {
		cfg.NProbe = defaultIVFNProbe
	}

	ext := newAudioExtractor(k, cfg.Metric)
	found, err := loadAudioCodebook(codebookPath, k, cfg.Metric, ext)
	if err != nil {
		return nil, err
	}
	if !found {
		rows, err := tbl.scanTable()
		if err != nil {
			return nil, err
		}
		var paths []string
		for _, row := range rows {
			if pos >= len(row) || row[pos] == nil {
				continue
			}
			if p, ok := row[pos].(string); ok && p != "" {
				paths = append(paths, p)
			}
		}
		if len(paths) == 0 {
			return nil, fmt.Errorf("inserte filas con rutas de audio en %s antes de crear el índice AUDIO", tbl.Name())
		}
		vocabJSON, err := ext.TrainFromPaths(paths)
		if err != nil {
			return nil, fmt.Errorf("entrenando el vocabulario de audio: %w", err)
		}
		if err := saveAudioCodebook(codebookPath, k, cfg.Metric, vocabJSON); err != nil {
			return nil, fmt.Errorf("guardando el vocabulario de audio: %w", err)
		}
	}

	idx := &ivfIndex{
		baseIndex: baseIndex{name: name, kind: AudioIndexKind, col: tbl.Schema.Columns[pos], table: tbl},
		pos:       pos,
		dim:       k,
		nlist:     cfg.NList,
		nprobe:    cfg.NProbe,
		metric:    cfg.Metric,
		audio:     &audioSource{ext: ext, k: k, codebookPath: codebookPath},
	}
	if err := idx.Rebuild(); err != nil {
		return nil, err
	}
	return idx, nil
}

func (t *Table) audioIndex() (*ivfIndex, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, name := range t.order {
		if idx, ok := t.indexes[name].(*ivfIndex); ok && idx.audio != nil {
			return idx, true
		}
	}
	return nil, false
}

func removeAudioCodebooks(tbl *Table) {
	tbl.mu.RLock()
	var paths []string
	for _, idx := range tbl.indexes {
		if ivf, ok := idx.(*ivfIndex); ok && ivf.audio != nil {
			paths = append(paths, ivf.audio.codebookPath)
		}
	}
	tbl.mu.RUnlock()
	for _, p := range paths {
		_ = os.Remove(p)
	}
}

func (ec *evalContext) evalAudioVector(arg any) ([]float32, error) {
	if ec.table == nil {
		return nil, fmt.Errorf("sql: AUDIO_VECTOR necesita una tabla con índice AUDIO")
	}
	idx, ok := ec.table.audioIndex()
	if !ok {
		return nil, fmt.Errorf("sql: la tabla %s no tiene un índice AUDIO (CREATE INDEX ... USING AUDIO)", ec.table.Name())
	}
	return idx.audio.vector(arg)
}
