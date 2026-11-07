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

// Búsqueda por imagen (bolsa de palabras visuales).
//
//	CREATE TABLE fotos (id INT PRIMARY KEY, ruta VARCHAR(255));
//	INSERT INTO fotos VALUES (1, 'img/gato.jpg'), ...;
//	CREATE INDEX idx_fotos ON fotos USING IMAGE (ruta) WITH (k = 64);
//	SELECT id, ruta FROM fotos
//	ORDER BY vector_distance(ruta, IMAGE_VECTOR('img/consulta.jpg')) LIMIT 5;
//
// El índice IMAGE es un IVF-Flat cuyos vectores no se guardan en la tabla: se
// calculan a partir de la imagen. Al crearlo se extraen descriptores SIFT de
// todas las imágenes existentes y se entrena un codebook de K centroides
// (k-means). Cada imagen se resume en un histograma de K componentes (cuántos
// descriptores caen en cada centroide), L2-normalizado, que alimenta al IVF.
// El codebook se guarda junto al catálogo para no reentrenar al reabrir.
//
// Requiere compilar con -tags opencv; sin él las operaciones sobre imágenes
// fallan con vector.ErrOpenCVDisabled y el resto del motor funciona igual.

// defaultImageK es el tamaño del codebook (y la dimensión del vector) si el
// CREATE INDEX no trae WITH (k = ...).
const defaultImageK = 64

// ImageExtractor es lo que el motor necesita de un extractor de imágenes.
// *vector.ImageCVFeature lo implementa; los tests inyectan uno falso para no
// depender de OpenCV.
type ImageExtractor interface {
	Dim() int
	// Train entrena el codebook con los bytes de las imágenes.
	Train(dataset [][]byte) ([][]float64, error)
	// Query devuelve el histograma de una imagen con el codebook entrenado.
	Query(img []byte) ([]float32, error)
}

// newImageExtractor construye el extractor. Es una variable para poder
// sustituirlo en pruebas.
var newImageExtractor = func(k int, metric metricpkg.Metric, codebook [][]float64) ImageExtractor {
	return &vecpkg.ImageCVFeature{K: k, Metric: metric, Codebook: codebook}
}

// imageSource convierte el valor de una columna (ruta o bytes) en el vector
// que se indexa.
type imageSource struct {
	ext          ImageExtractor
	k            int
	codebookPath string
}

func imageBytes(v any) ([]byte, error) {
	switch x := v.(type) {
	case string:
		raw, err := os.ReadFile(x)
		if err != nil {
			return nil, fmt.Errorf("sql: no se pudo leer la imagen %q: %w", x, err)
		}
		return raw, nil
	case []byte:
		return x, nil
	}
	return nil, fmt.Errorf("sql: se esperaba una ruta de imagen o sus bytes, no %T", v)
}

func (s *imageSource) vector(v any) ([]float32, error) {
	raw, err := imageBytes(v)
	if err != nil {
		return nil, err
	}
	vec, err := s.ext.Query(raw)
	if err != nil {
		return nil, fmt.Errorf("sql: no se pudo extraer features de la imagen: %w", err)
	}
	if len(vec) != s.k {
		return nil, fmt.Errorf("sql: el extractor devolvió %d dimensiones, se esperaban %d", len(vec), s.k)
	}
	return vec, nil
}

// ---------------------------------------------------------------------------
// Codebook en disco
// ---------------------------------------------------------------------------

type codebookDisk struct {
	K         int         `json:"k"`
	Metric    string      `json:"metric"`
	Centroids [][]float64 `json:"centroids"`
}

func imageCodebookPath(dir, table, name string) string {
	return filepath.Join(dir, strings.ToLower(table)+"_"+strings.ToLower(name)+".codebook.json")
}

func saveCodebook(path string, k int, metric metricpkg.Metric, centroids [][]float64) error {
	raw, err := json.Marshal(codebookDisk{K: k, Metric: vectorMetricName(metric), Centroids: centroids})
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// loadCodebook devuelve found=false si el archivo no existe (hay que entrenar).
func loadCodebook(path string, k int, metric metricpkg.Metric) (centroids [][]float64, found bool, err error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var disk codebookDisk
	if err := json.Unmarshal(raw, &disk); err != nil {
		return nil, false, fmt.Errorf("sql: codebook %s ilegible: %w", path, err)
	}
	if disk.K != k || len(disk.Centroids) != k || disk.Metric != vectorMetricName(metric) {
		return nil, false, fmt.Errorf("sql: el codebook %s (k=%d, %s) no coincide con el índice (k=%d, %s)",
			path, disk.K, disk.Metric, k, vectorMetricName(metric))
	}
	return disk.Centroids, true, nil
}

// ---------------------------------------------------------------------------
// Índice IMAGE
// ---------------------------------------------------------------------------

func (e *Engine) createImageIndex(tbl *Table, name, col string, cfg ivfConfig) (Index, error) {
	path := imageCodebookPath(e.dbDir(), tbl.Name(), name)
	idx, err := newImageIndex(name, col, tbl, cfg, path)
	if err != nil {
		return nil, fmt.Errorf("sql: no se pudo crear el índice IMAGE %s: %w", name, err)
	}
	tbl.addIndex(idx)
	return idx, nil
}

// newImageIndex carga o entrena el codebook y puebla un IVF con los
// histogramas de las imágenes existentes.
func newImageIndex(name, col string, tbl *Table, cfg ivfConfig, codebookPath string) (Index, error) {
	pos, ok := tbl.ColumnIndex(col)
	if !ok {
		return nil, fmt.Errorf("la columna %s no existe en %s", col, tbl.Name())
	}
	if ty := tbl.Schema.Types[pos]; ty != storage.TypeString && ty != storage.TypeBytes {
		return nil, fmt.Errorf("el índice IMAGE necesita una columna con la ruta de la imagen (VARCHAR) o sus bytes (BLOB), %s es %s",
			col, ty)
	}
	if len(tbl.Schema.KeyCols) == 0 {
		return nil, fmt.Errorf("el índice IMAGE necesita que %s tenga clave primaria", tbl.Name())
	}
	k := cfg.K
	if k <= 0 {
		k = defaultImageK
	}
	if cfg.Metric == metricpkg.DotProduct {
		return nil, fmt.Errorf("el codebook no puede entrenarse con producto punto; use cosine o euclidean")
	}
	if cfg.NProbe <= 0 {
		cfg.NProbe = defaultIVFNProbe
	}

	centroids, found, err := loadCodebook(codebookPath, k, cfg.Metric)
	if err != nil {
		return nil, err
	}
	ext := newImageExtractor(k, cfg.Metric, centroids)
	if !found {
		rows, err := tbl.scanTable()
		if err != nil {
			return nil, err
		}
		var dataset [][]byte
		for _, row := range rows {
			if pos >= len(row) || row[pos] == nil {
				continue
			}
			raw, err := imageBytes(row[pos])
			if err != nil {
				return nil, err
			}
			dataset = append(dataset, raw)
		}
		if len(dataset) == 0 {
			return nil, fmt.Errorf("el codebook se entrena con las imágenes existentes: inserte filas con la ruta de una imagen en %s antes de crear el índice", tbl.Name())
		}
		trained, err := ext.Train(dataset)
		if err != nil {
			return nil, fmt.Errorf("entrenando el codebook: %w", err)
		}
		if len(trained) != k {
			return nil, fmt.Errorf("el codebook tiene %d centroides, se pidieron %d", len(trained), k)
		}
		if err := saveCodebook(codebookPath, k, cfg.Metric, trained); err != nil {
			return nil, fmt.Errorf("guardando el codebook: %w", err)
		}
	}

	idx := &ivfIndex{
		baseIndex: baseIndex{name: name, kind: ImageIndexKind, col: tbl.Schema.Columns[pos], table: tbl},
		pos:       pos,
		dim:       k,
		nlist:     cfg.NList,
		nprobe:    cfg.NProbe,
		metric:    cfg.Metric,
		image:     &imageSource{ext: ext, k: k, codebookPath: codebookPath},
	}
	if err := idx.Rebuild(); err != nil {
		return nil, err
	}
	return idx, nil
}

// imageIndex devuelve el índice IMAGE de la tabla (el primero, si hay varios).
func (t *Table) imageIndex() (*ivfIndex, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, name := range t.order {
		if idx, ok := t.indexes[name].(*ivfIndex); ok && idx.image != nil {
			return idx, true
		}
	}
	return nil, false
}

// removeImageCodebooks borra los codebooks de los índices IMAGE de una tabla.
func removeImageCodebooks(tbl *Table) {
	tbl.mu.RLock()
	var paths []string
	for _, idx := range tbl.indexes {
		if ivf, ok := idx.(*ivfIndex); ok && ivf.image != nil {
			paths = append(paths, ivf.image.codebookPath)
		}
	}
	tbl.mu.RUnlock()
	for _, p := range paths {
		_ = os.Remove(p)
	}
}

// vectorArg convierte un argumento de vector_distance en vector. Además de
// VECTOR y '[1,2,3]', acepta la ruta (o los bytes) de una imagen si la tabla
// tiene un índice IMAGE.
func (ec *evalContext) vectorArg(v any) ([]float32, error) {
	vec, err := toVector32(v)
	if err == nil {
		return vec, nil
	}
	switch v.(type) {
	case string, []byte:
		if ec.table != nil {
			if idx, ok := ec.table.imageIndex(); ok {
				return idx.image.vector(v)
			}
		}
	}
	return nil, err
}

// evalImageVector evalúa IMAGE_VECTOR(ruta | bytes) con el codebook del índice
// IMAGE de la tabla en curso.
func (ec *evalContext) evalImageVector(arg any) ([]float32, error) {
	if ec.table == nil {
		return nil, fmt.Errorf("sql: IMAGE_VECTOR necesita una tabla con índice IMAGE")
	}
	idx, ok := ec.table.imageIndex()
	if !ok {
		return nil, fmt.Errorf("sql: la tabla %s no tiene un índice IMAGE (CREATE INDEX ... USING IMAGE)", ec.table.Name())
	}
	return idx.image.vector(arg)
}
