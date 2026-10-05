// Comando server: expone el executor SQL (dbms/lib/sql) por HTTP para que
// el frontend pueda ejecutar consultas y cargar datos desde un CSV, sin
// tocar el diseño interno de los índices (Hashing, B+) ni de los archivos
// de almacenamiento (Heap, Secuencial) — solo los usa a través de su API pública.
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dbms-go/v2/dbms/lib/spatial"
	"github.com/dbms-go/v2/dbms/lib/sql"
)

func main() {
	addr := flag.String("addr", ":8080", "dirección donde escuchar")
	dir := flag.String("dir", "./data", "directorio donde persisten los .heap de cada tabla")
	flag.Parse()

	engine, err := sql.Open("minigestor", sql.Options{Dir: *dir})
	if err != nil {
		log.Fatalf("no se pudo abrir la base de datos en %q: %v", *dir, err)
	}
	defer engine.Close()
	db := &database{eng: engine}

	//------------------------------------------
	// SPATIAL
	spatialStore := spatial.NewStore(engine, "ubicaciones")
	//------------------------------------------

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tables", handleListTables(db))
	mux.HandleFunc("POST /api/query", handleQuery(db, spatialStore))
	mux.HandleFunc("POST /api/tables/{name}/import", handleImportCSV(db, spatialStore))
	mux.HandleFunc("POST /api/spatial/range", db.locked(handleSpatialRange(spatialStore)))
	mux.HandleFunc("POST /api/spatial/knn", db.locked(handleSpatialKNN(spatialStore)))
	mux.HandleFunc("POST /api/spatial/polygon", db.locked(handleSpatialPolygon(spatialStore)))

	log.Printf("MinigestorBD escuchando en %s (datos en %s)", *addr, *dir)
	log.Fatal(http.ListenAndServe(*addr, withCORS(mux)))
}

// database envuelve el sql.Engine para el servidor HTTP. El engine guarda la
// transacción activa como estado propio (una sola sesión), así que las
// peticiones se serializan con un mutex.
type database struct {
	mu  sync.Mutex
	eng *sql.Engine
}

// locked serializa un handler que toca el engine (los espaciales leen la
// tabla con un SELECT al reconstruir el R-Tree).
func (db *database) locked(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		db.mu.Lock()
		defer db.mu.Unlock()
		h(w, r)
	}
}

// queryResult es la respuesta de /api/query con la forma que espera el
// frontend: el plan va como texto legible, un paso por línea.
type queryResult struct {
	Columns  []string
	Rows     [][]any
	Affected int
	Message  string
	Plan     []string
	// Tree es el plan como árbol de operadores (raíz = último operador).
	Tree      *sql.PlanNode
	ElapsedMs float64
}

// execute corre una sentencia; el llamador debe tener db.mu tomado.
func (db *database) execute(query string) (*queryResult, error) {
	start := time.Now()
	res, err := db.eng.Exec(query)
	if err != nil {
		return nil, err
	}
	out := &queryResult{
		Columns:   res.Columns,
		Rows:      res.Rows,
		Affected:  res.Affected,
		Message:   res.Message,
		Plan:      make([]string, 0, len(res.Plan)),
		Tree:      res.PlanTree(),
		ElapsedMs: float64(time.Since(start).Microseconds()) / 1000,
	}
	if out.Rows == nil {
		out.Rows = [][]any{}
	}
	stepLine := func(st sql.Step) string {
		line := fmt.Sprintf("[%s] %s", st.Kind, st.Detail)
		if st.Rows > 0 {
			line += fmt.Sprintf(" · %d fila(s)", st.Rows)
		}
		if st.Cost > 0 {
			line += fmt.Sprintf(" · costo ≈ %d página(s)", st.Cost)
		}
		return line
	}
	for _, st := range res.Plan {
		// La entrada derecha de un JOIN se lee antes de unir.
		for _, in := range st.Inputs {
			out.Plan = append(out.Plan, stepLine(in))
		}
		out.Plan = append(out.Plan, stepLine(st))
	}
	return out, nil
}

// ColumnInfo y TableInfo describen una tabla para el panel de archivos.
type ColumnInfo struct {
	Name string
	Type string
	Key  bool
}

type TableInfo struct {
	Name      string
	Columns   []ColumnInfo
	Records   int64
	Pages     int32
	IndexName string
	IndexType string
}

func tableInfoFrom(f sql.TableFileInfo) *TableInfo {
	isKey := make(map[string]bool, len(f.PrimaryKey))
	for _, k := range f.PrimaryKey {
		isKey[strings.ToLower(k)] = true
	}
	info := &TableInfo{
		Name:    f.Name,
		Columns: make([]ColumnInfo, len(f.Columns)),
		Records: f.Rows,
		Pages:   f.Pages,
	}
	for i, c := range f.Columns {
		typ := ""
		if i < len(f.Types) {
			typ = f.Types[i]
		}
		info.Columns[i] = ColumnInfo{Name: c, Type: typ, Key: isKey[strings.ToLower(c)]}
	}
	switch {
	case len(f.Indexes) > 0:
		info.IndexName = strings.Join(f.Indexes, ", ")
		info.IndexType = strings.Join(f.IndexKinds, ", ")
		if f.Clustered {
			info.IndexType = "secuencial agrupado + " + info.IndexType
		}
	case f.Clustered:
		info.IndexName = "pk_" + strings.ToLower(f.Name)
		info.IndexType = "secuencial agrupado (" + f.Allocation + ")"
	default:
		info.IndexName = "—"
		info.IndexType = "heap file sin índice"
	}
	return info
}

// tablesInfo y tableInfo leen el catálogo; el llamador debe tener db.mu.
func (db *database) tablesInfo() []*TableInfo {
	files := db.eng.Files()
	out := make([]*TableInfo, 0, len(files))
	for _, f := range files {
		out = append(out, tableInfoFrom(f))
	}
	return out
}

func (db *database) tableInfo(name string) (*TableInfo, bool) {
	for _, f := range db.eng.Files() {
		if strings.EqualFold(f.Name, name) {
			return tableInfoFrom(f), true
		}
	}
	return nil, false
}

// withCORS permite que el frontend (servido en otro puerto por Vite en
// desarrollo) llame a esta API sin bloqueos del navegador.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// GET /api/tables
func handleListTables(db *database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		db.mu.Lock()
		defer db.mu.Unlock()
		writeJSON(w, http.StatusOK, db.tablesInfo())
	}
}

type queryRequest struct {
	SQL string `json:"sql"`
}

// POST /api/query {"sql": "SELECT ..."}
func handleQuery(db *database, spatialStore *spatial.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req queryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("body inválido: %w", err))
			return
		}
		if strings.TrimSpace(req.SQL) == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("falta el campo \"sql\""))
			return
		}
		db.mu.Lock()
		defer db.mu.Unlock()
		res, err := db.execute(req.SQL)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, res)

		// Si la consulta modifica la tabla espacial,
		// reconstruimos el R-Tree.
		upperSQL := strings.ToUpper(
			strings.TrimSpace(req.SQL),
		)

		if strings.HasPrefix(
			upperSQL,
			"CREATE TABLE UBICACIONES",
		) ||
			strings.HasPrefix(
				upperSQL,
				"INSERT INTO UBICACIONES",
			) ||
			strings.HasPrefix(
				upperSQL,
				"DELETE FROM UBICACIONES",
			) ||
			strings.HasPrefix(
				upperSQL,
				"UPDATE UBICACIONES",
			) ||
			strings.HasPrefix(
				upperSQL,
				"DROP TABLE UBICACIONES",
			) ||
			strings.HasPrefix(
				upperSQL,
				"TRUNCATE",
			) ||
			strings.HasPrefix(upperSQL, "COMMIT") ||
			strings.HasPrefix(upperSQL, "ROLLBACK") {

			_ = spatialStore.Refresh()
		}
	}
}

// POST /api/tables/{name}/import — multipart/form-data con un campo "file" (CSV).
// La tabla debe existir de antemano (CREATE TABLE) con el mismo número y
// orden de columnas que el CSV. La primera fila del CSV se asume encabezado
// y se descarta.
func handleImportCSV(db *database, spatialStore *spatial.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tableName := r.PathValue("name")
		db.mu.Lock()
		defer db.mu.Unlock()
		info, ok := db.tableInfo(tableName)
		if !ok {
			writeError(w, http.StatusNotFound, fmt.Errorf("la tabla %q no existe; créala primero con CREATE TABLE", tableName))
			return
		}

		if err := r.ParseMultipartForm(32 << 20); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("form inválido: %w", err))
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("falta el archivo CSV (campo \"file\"): %w", err))
			return
		}
		defer file.Close()

		inserted, skipped, errs := importRows(db, info, file)
		if strings.EqualFold(tableName, "ubicaciones") && inserted > 0 {
			_ = spatialStore.Refresh()
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"table":    tableName,
			"inserted": inserted,
			"skipped":  skipped,
			"errors":   errs,
		})
	}
}

func importRows(db *database, info *TableInfo, file multipart.File) (inserted, skipped int, errs []string) {
	const batchSize = 200

	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1 // reportamos nosotros el error, con mejor mensaje

	header, err := reader.Read()
	if err == io.EOF {
		return 0, 0, []string{"el CSV está vacío"}
	}
	if err != nil {
		return 0, 0, []string{fmt.Sprintf("no se pudo leer el encabezado: %v", err)}
	}
	if len(header) != len(info.Columns) {
		errs = append(errs, fmt.Sprintf(
			"el CSV tiene %d columnas y la tabla %q tiene %d (%s); se asume que el orden coincide",
			len(header), info.Name, len(info.Columns), columnNames(info),
		))
	}

	var batch []string
	flush := func() {
		if len(batch) == 0 {
			return
		}
		stmt := fmt.Sprintf("INSERT INTO %s VALUES %s;", info.Name, strings.Join(batch, ", "))
		if _, err := db.execute(stmt); err != nil {
			skipped += len(batch)
			errs = append(errs, err.Error())
		} else {
			inserted += len(batch)
		}
		batch = batch[:0]
	}

	rowNum := 1 // el encabezado es la fila 0
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		rowNum++
		if err != nil {
			skipped++
			errs = append(errs, fmt.Sprintf("fila %d: %v", rowNum, err))
			continue
		}
		tuple, err := rowToSQLTuple(info, record)
		if err != nil {
			skipped++
			errs = append(errs, fmt.Sprintf("fila %d: %v", rowNum, err))
			continue
		}
		batch = append(batch, tuple)
		if len(batch) >= batchSize {
			flush()
		}
	}
	flush()

	if len(errs) > 20 {
		errs = append(errs[:20], fmt.Sprintf("... y %d error(es) más", len(errs)-20))
	}
	return inserted, skipped, errs
}

func columnNames(info *TableInfo) string {
	names := make([]string, len(info.Columns))
	for i, c := range info.Columns {
		names[i] = c.Name
	}
	return strings.Join(names, ", ")
}

// rowToSQLTuple convierte una fila del CSV en un literal "(v1, v2, ...)"
// que el parser SQL del proyecto pueda leer, según el tipo de cada columna.
func rowToSQLTuple(info *TableInfo, record []string) (string, error) {
	n := len(info.Columns)
	if len(record) < n {
		return "", fmt.Errorf("faltan valores: se esperaban %d, llegaron %d", n, len(record))
	}
	vals := make([]string, n)
	for i, col := range info.Columns {
		raw := strings.TrimSpace(record[i])
		// Los tipos son los del storage del engine (storage.Type.String()).
		switch strings.ToLower(col.Type) {
		case "int32", "int64":
			if _, err := strconv.ParseInt(raw, 10, 64); err != nil {
				return "", fmt.Errorf("columna %q: %q no es un entero", col.Name, raw)
			}
			vals[i] = raw
		case "point":
			// Acepta "POINT(lat lon)" o "lat lon"; el motor valida el rango.
			clean := strings.NewReplacer(`"`, "", `'`, "").Replace(raw)
			if !strings.HasPrefix(strings.ToUpper(clean), "POINT") {
				clean = "POINT(" + clean + ")"
			}
			vals[i] = "'" + clean + "'"
		case "bool":
			switch strings.ToLower(raw) {
			case "true", "1":
				vals[i] = "true"
			case "false", "0":
				vals[i] = "false"
			default:
				return "", fmt.Errorf("columna %q: %q no es un booleano", col.Name, raw)
			}
		default: // string / bytes
			// El lexer del proyecto no soporta comillas escapadas dentro de
			// un literal, así que se quitan para no romper el parseo.
			clean := strings.NewReplacer(`"`, "", `'`, "").Replace(raw)
			vals[i] = "'" + clean + "'"
		}
	}
	return "(" + strings.Join(vals, ", ") + ")", nil
}

// POST /api/spatial/range
func handleSpatialRange(store *spatial.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		var req spatial.RangeRequest

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(
				w,
				http.StatusBadRequest,
				fmt.Errorf("body inválido: %w", err),
			)
			return
		}

		result, err := store.SearchRange(req)

		if err != nil {
			writeError(
				w,
				http.StatusBadRequest,
				err,
			)
			return
		}

		writeJSON(
			w,
			http.StatusOK,
			result,
		)
	}
}
