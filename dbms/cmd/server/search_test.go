package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dbms-go/v2/dbms/lib/sql"
)

func newTestDB(t *testing.T) *database {
	t.Helper()
	eng, err := sql.Open("prueba", sql.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	cache, err := newSearchCache()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cache.close()
		eng.Close()
	})
	return &database{eng: eng, search: cache}
}

func mustExec(t *testing.T, db *database, script string) {
	t.Helper()
	s := db.eng.NewSession()
	defer s.Close()
	if _, err := s.ExecScript(script); err != nil {
		t.Fatalf("%s: %v", script, err)
	}
	db.search.invalidateTouched(db.eng, script)
}

func post(t *testing.T, h http.HandlerFunc, body any, out any) int {
	t.Helper()
	raw, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("respuesta no es JSON: %s", rec.Body.String())
		}
	}
	return rec.Code
}

func TestTextSearchRanksAndRefreshes(t *testing.T) {
	db := newTestDB(t)
	mustExec(t, db, `CREATE TABLE docs CLUSTERED BY (id) (id INT PRIMARY KEY, contenido VARCHAR(300));
		INSERT INTO docs VALUES
		(1, 'El árbol B+ acelera las búsquedas por rango en una base de datos'),
		(2, 'Receta de ceviche con pescado fresco y limón'),
		(3, 'Las búsquedas de imágenes similares usan vectores');`)

	var res struct {
		Tokens  []string
		Results []struct {
			Method string
			Hits   []struct {
				Doc     uint32
				Row     []any
				Matched []string
			}
		}
	}
	h := handleTextSearch(db)
	if code := post(t, h, map[string]any{"table": "docs", "column": "contenido", "query": "pescados frescos", "k": 3}, &res); code != 200 {
		t.Fatalf("código %d", code)
	}
	if len(res.Results) != 2 {
		t.Fatalf("se esperaban TF-IDF y BM25, llegaron %d", len(res.Results))
	}
	for _, r := range res.Results {
		if len(r.Hits) == 0 || r.Hits[0].Row[0].(float64) != 2 {
			t.Fatalf("%s: el primer resultado debía ser el documento 2: %+v", r.Method, r.Hits)
		}
		if len(r.Hits[0].Matched) == 0 {
			t.Fatalf("%s: faltan las palabras encontradas", r.Method)
		}
	}

	// Una inserción invalida el índice: el documento nuevo debe aparecer.
	mustExec(t, db, `INSERT INTO docs VALUES (4, 'Pescado a la plancha');`)
	if post(t, h, map[string]any{"table": "docs", "column": "contenido", "query": "plancha", "methods": []string{"bm25"}}, &res); len(res.Results[0].Hits) != 1 {
		t.Fatalf("el índice no se reconstruyó tras el INSERT: %+v", res.Results[0].Hits)
	}
}

func TestVectorSearchAllIndexes(t *testing.T) {
	db := newTestDB(t)
	mustExec(t, db, `CREATE TABLE vec CLUSTERED BY (id) (id INT PRIMARY KEY, v VECTOR(2));
		INSERT INTO vec VALUES (1, VECTOR(1, 0)), (2, VECTOR(0.9, 0.1)), (3, VECTOR(0, 1)), (4, VECTOR(0.1, 0.9)), (5, VECTOR(-1, 0));`)

	var res struct {
		K       int
		Results []struct {
			Method string
			Recall float64
			Error  string
			Hits   []struct{ Row int }
		}
	}
	row := 0
	h := handleVectorSearch(db)
	if code := post(t, h, map[string]any{"table": "vec", "column": "v", "row": row, "k": 2, "metric": "euclidean", "nlist": 2, "nprobe": 2}, &res); code != 200 {
		t.Fatalf("código %d", code)
	}
	if len(res.Results) != 3 {
		t.Fatalf("se esperaban 3 técnicas, llegaron %d", len(res.Results))
	}
	for _, r := range res.Results {
		if r.Error != "" {
			t.Fatalf("%s: %s", r.Method, r.Error)
		}
		if len(r.Hits) != 2 || r.Hits[0].Row != 0 || r.Hits[1].Row != 1 {
			t.Fatalf("%s: vecinos inesperados %+v", r.Method, r.Hits)
		}
		if r.Recall != 1 {
			t.Fatalf("%s: recall %v", r.Method, r.Recall)
		}
	}

	// IVF no se entrena con producto punto: error en su resultado, no en toda la respuesta.
	if code := post(t, h, map[string]any{"table": "vec", "column": "v", "vector": []float32{1, 0}, "k": 1, "metric": "dot", "methods": []string{"ivf", "seq"}}, &res); code != 200 {
		t.Fatalf("código %d", code)
	}
	if res.Results[0].Error == "" || res.Results[1].Error != "" {
		t.Fatalf("errores inesperados: %+v", res.Results)
	}
}
