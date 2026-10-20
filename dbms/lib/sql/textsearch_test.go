package sql

import (
	"fmt"
	"testing"
)

// seedDocs crea una tabla con textos para probar la búsqueda MATCH/TF-IDF.
func seedDocs(t *testing.T, e *Engine) {
	t.Helper()
	run(t, e, "CREATE TABLE docs (id INT PRIMARY KEY, cuerpo VARCHAR(64))")
	run(t, e, `INSERT INTO docs VALUES
		(1, 'base de datos'),
		(2, 'base de datos relacionales'),
		(3, 'grafos y redes'),
		(4, 'datos datos grandes')`)
}

func ids(res *Result) []string {
	out := make([]string, 0, len(res.Rows))
	for _, r := range res.Rows {
		out = append(out, fmt.Sprint(r[0]))
	}
	return out
}

func equalStrings(a, b []string) bool {
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

func TestMatchFiltraDocumentos(t *testing.T) {
	e := newTestEngine(t, Options{})
	seedDocs(t, e)

	res := run(t, e, "SELECT id FROM docs WHERE MATCH(cuerpo, 'datos') > 0")
	got := ids(res)
	// El orden depende del recorrido; aquí basta con el conjunto.
	want := map[string]bool{"1": true, "2": true, "4": true}
	if len(got) != len(want) {
		t.Fatalf("MATCH 'datos' devolvió %v, se esperaban 3 filas", got)
	}
	for _, id := range got {
		if !want[id] {
			t.Fatalf("MATCH 'datos' incluyó la fila %s que no debería", id)
		}
	}

	res = run(t, e, "SELECT id FROM docs WHERE MATCH(cuerpo, 'zzz') > 0")
	if len(res.Rows) != 0 {
		t.Fatalf("MATCH de un término inexistente devolvió %d fila(s)", len(res.Rows))
	}
}

func TestMatchOrdenaPorScore(t *testing.T) {
	e := newTestEngine(t, Options{})
	seedDocs(t, e)

	// 'datos' aparece 2 veces en la fila 4, 1 vez en la 1 y en la 2.
	res := run(t, e, `SELECT id FROM docs
		WHERE MATCH(cuerpo, 'datos') > 0
		ORDER BY MATCH(cuerpo, 'datos') DESC
		LIMIT 2`)
	if got := ids(res); !equalStrings(got, []string{"4", "1"}) {
		t.Fatalf("orden por score = %v, se esperaba [4 1]", got)
	}
}

func TestOrderByUsaAliasDeSalida(t *testing.T) {
	e := newTestEngine(t, Options{})
	seedDocs(t, e)

	res := run(t, e, `SELECT id, MATCH(cuerpo, 'datos') AS score FROM docs
		WHERE MATCH(cuerpo, 'datos') > 0
		ORDER BY score DESC
		LIMIT 2`)
	if got := ids(res); !equalStrings(got, []string{"4", "1"}) {
		t.Fatalf("ORDER BY score (alias) = %v, se esperaba [4 1]", got)
	}
	s0, ok0 := res.Rows[0][1].(float64)
	s1, ok1 := res.Rows[1][1].(float64)
	if !ok0 || !ok1 || s0 <= s1 {
		t.Fatalf("los scores deben ser float y decrecer: %v, %v", res.Rows[0][1], res.Rows[1][1])
	}
}

func TestMatchModelos(t *testing.T) {
	e := newTestEngine(t, Options{})
	seedDocs(t, e)

	tf := run(t, e, "SELECT MATCH(cuerpo, 'datos', 'tf') FROM docs WHERE id = 1")
	idf := run(t, e, "SELECT MATCH(cuerpo, 'datos') FROM docs WHERE id = 1")
	vtf, ok1 := tf.Rows[0][0].(float64)
	vidf, ok2 := idf.Rows[0][0].(float64)
	if !ok1 || !ok2 {
		t.Fatalf("MATCH debe devolver números: %v / %v", tf.Rows[0][0], idf.Rows[0][0])
	}
	if vidf <= vtf {
		t.Fatalf("el score TF-IDF (%v) debe superar al TF puro (%v)", vidf, vtf)
	}

	if _, err := e.Exec("SELECT MATCH(cuerpo, 'datos', 'br26') FROM docs"); err == nil {
		t.Fatalf("un modelo desconocido debe dar error")
	}
}

func TestMatchRefrescaTrasInsert(t *testing.T) {
	e := newTestEngine(t, Options{})
	seedDocs(t, e)

	// Se puntúa una vez para poblar el caché del corpus.
	run(t, e, "SELECT id FROM docs WHERE MATCH(cuerpo, 'datos') > 0")

	run(t, e, `INSERT INTO docs VALUES (5, 'datos datos datos datos')`)
	res := run(t, e, `SELECT id FROM docs
		WHERE MATCH(cuerpo, 'datos') > 0
		ORDER BY MATCH(cuerpo, 'datos') DESC
		LIMIT 1`)
	if got := ids(res); !equalStrings(got, []string{"5"}) {
		t.Fatalf("tras el INSERT el orden debe cambiar a [5], quedó %v", got)
	}
}
