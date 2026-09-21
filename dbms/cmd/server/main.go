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

	"github.com/dbms-go/v2/dbms/lib/sql"
)

func main() {
	addr := flag.String("addr", ":8080", "dirección donde escuchar")
	dir := flag.String("dir", "./data", "directorio donde persisten los .heap de cada tabla")
	flag.Parse()

	db, err := sql.Open(*dir)
	if err != nil {
		log.Fatalf("no se pudo abrir la base de datos en %q: %v", *dir, err)
	}
	defer db.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tables", handleListTables(db))
	mux.HandleFunc("POST /api/query", handleQuery(db))
	mux.HandleFunc("POST /api/tables/{name}/import", handleImportCSV(db))

	log.Printf("MinigestorBD escuchando en %s (datos en %s)", *addr, *dir)
	log.Fatal(http.ListenAndServe(*addr, withCORS(mux)))
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
func handleListTables(db *sql.Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, db.TablesInfo())
	}
}

type queryRequest struct {
	SQL string `json:"sql"`
}

// POST /api/query {"sql": "SELECT ..."}
func handleQuery(db *sql.Database) http.HandlerFunc {
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
		res, err := db.Execute(req.SQL)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

// POST /api/tables/{name}/import — multipart/form-data con un campo "file" (CSV).
// La tabla debe existir de antemano (CREATE TABLE) con el mismo número y
// orden de columnas que el CSV. La primera fila del CSV se asume encabezado
// y se descarta.
func handleImportCSV(db *sql.Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tableName := r.PathValue("name")
		info, ok := db.TableInfo(tableName)
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
		writeJSON(w, http.StatusOK, map[string]any{
			"table":    tableName,
			"inserted": inserted,
			"skipped":  skipped,
			"errors":   errs,
		})
	}
}

func importRows(db *sql.Database, info *sql.TableInfo, file multipart.File) (inserted, skipped int, errs []string) {
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
		if _, err := db.Execute(stmt); err != nil {
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

func columnNames(info *sql.TableInfo) string {
	names := make([]string, len(info.Columns))
	for i, c := range info.Columns {
		names[i] = c.Name
	}
	return strings.Join(names, ", ")
}

// rowToSQLTuple convierte una fila del CSV en un literal "(v1, v2, ...)"
// que el parser SQL del proyecto pueda leer, según el tipo de cada columna.
func rowToSQLTuple(info *sql.TableInfo, record []string) (string, error) {
	n := len(info.Columns)
	if len(record) < n {
		return "", fmt.Errorf("faltan valores: se esperaban %d, llegaron %d", n, len(record))
	}
	vals := make([]string, n)
	for i, col := range info.Columns {
		raw := strings.TrimSpace(record[i])
		switch strings.ToUpper(col.Type) {
		case "INT":
			if _, err := strconv.Atoi(raw); err != nil {
				return "", fmt.Errorf("columna %q: %q no es un entero", col.Name, raw)
			}
			vals[i] = raw
		case "DECIMAL":
			if _, err := strconv.ParseFloat(raw, 64); err != nil {
				return "", fmt.Errorf("columna %q: %q no es un decimal", col.Name, raw)
			}
			vals[i] = raw
		case "BOOL":
			switch strings.ToLower(raw) {
			case "true", "1":
				vals[i] = "true"
			case "false", "0":
				vals[i] = "false"
			default:
				return "", fmt.Errorf("columna %q: %q no es un booleano", col.Name, raw)
			}
		default: // STRING
			// El lexer del proyecto no soporta comillas escapadas dentro de
			// un literal, así que se quitan para no romper el parseo.
			clean := strings.NewReplacer(`"`, "", `'`, "").Replace(raw)
			vals[i] = `"` + clean + `"`
		}
	}
	return "(" + strings.Join(vals, ", ") + ")", nil
}
