package sql

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dbms-go/v2/dbms/lib/index/text/ranking"
	textproc "github.com/dbms-go/v2/dbms/lib/text"
)

func TestRankedSQLBM25AndCosineRealSPIMI(t *testing.T) {
	eng := newTestEngine(t, Options{})
	run(t, eng, "CREATE TABLE ranked_docs (id INT PRIMARY KEY, cuerpo VARCHAR(120))")
	run(t, eng, `INSERT INTO ranked_docs VALUES
      (1,'datos grafos distribuidos'),
      (2,'datos datos datos base de datos'),
      (3,'gato perros animales'),
      (4,'base de datos distribuidos grafos')`)

	for _, method := range []string{"TF_IDF", "BM25"} {
		res, err := eng.Exec(fmt.Sprintf(`SELECT id, SCORE() AS relevancia FROM ranked_docs
            WHERE MATCH(cuerpo,'datos distribuidos') USING %s
            ORDER BY relevancia DESC LIMIT 3`, method))
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		if len(res.Rows) == 0 || len(res.Rows) > 3 {
			t.Fatalf("%s: resultado inesperado %#v", method, res.Rows)
		}
		if len(res.Columns) != 2 || res.Columns[1] != "relevancia" {
			t.Fatalf("%s: columnas %#v", method, res.Columns)
		}
		prev := 1e100
		for _, row := range res.Rows {
			score, ok := row[1].(float64)
			if !ok || score <= 0 || score > prev {
				t.Fatalf("%s: score incorrecto %#v", method, row)
			}
			prev = score
		}
		// En autocommit el plan registra primero el bloqueo y luego el acceso SPIMI.
		// No asumir que el indice debe aparecer en la posicion cero.
		foundSPIMI := false
		for _, step := range res.Plan {
			if step.Kind == StepIndexScan && strings.Contains(strings.ToLower(step.Detail), "spimi") {
				foundSPIMI = true
				break
			}
		}
		if !foundSPIMI {
			t.Fatalf("%s: plan no utilizó SPIMI: %#v", method, res.Plan)
		}
		star, err := eng.Exec(fmt.Sprintf("SELECT *, SCORE() AS relevancia FROM ranked_docs WHERE MATCH(cuerpo,'datos') USING %s LIMIT 2", method))
		if err != nil || len(star.Columns) != 3 || star.Columns[2] != "relevancia" || len(star.Rows) != 2 {
			t.Fatalf("SELECT * + SCORE %s: %#v %v", method, star, err)
		}
	}
}

func TestRankedSQLCacheInvalidation(t *testing.T) {
	eng := newTestEngine(t, Options{})
	run(t, eng, "CREATE TABLE ranked_updates (id INT PRIMARY KEY, body VARCHAR(80))")
	run(t, eng, "INSERT INTO ranked_updates VALUES (1,'gatos perros'), (2,'caballos')")
	q := "SELECT id, SCORE() AS s FROM ranked_updates WHERE MATCH(body,'gatos') USING BM25 ORDER BY s DESC"
	x, err := eng.Exec(q)
	if err != nil || len(x.Rows) != 1 {
		t.Fatalf("antes de INSERT: %#v %v", x, err)
	}
	// Segunda consulta debe reutilizar el mismo Reader y devolver los mismos datos.
	if _, err := eng.Exec(q); err != nil {
		t.Fatal(err)
	}
	run(t, eng, "INSERT INTO ranked_updates VALUES (3,'gatos gatos gatos')")
	x, err = eng.Exec(q)
	if err != nil || len(x.Rows) != 2 {
		t.Fatalf("despues de INSERT: %#v %v", x, err)
	}
	run(t, eng, "UPDATE ranked_updates SET body = 'gatos' WHERE id = 2")
	x, err = eng.Exec(q)
	if err != nil || len(x.Rows) != 3 {
		t.Fatalf("despues de UPDATE: %#v %v", x, err)
	}
	run(t, eng, "DELETE FROM ranked_updates WHERE id = 1")
	x, err = eng.Exec(q)
	if err != nil || len(x.Rows) != 2 {
		t.Fatalf("despues de DELETE: %#v %v", x, err)
	}
}

func TestRankedSQLRejectsUnsupportedWhere(t *testing.T) {
	eng := newTestEngine(t, Options{})
	run(t, eng, "CREATE TABLE ranked_reject (id INT PRIMARY KEY, body VARCHAR(50))")
	run(t, eng, "INSERT INTO ranked_reject VALUES (1,'datos')")
	_, err := eng.Exec("SELECT id FROM ranked_reject WHERE id = 1 USING BM25")
	if err == nil {
		t.Fatal("USING BM25 sin MATCH debe rechazarse")
	}
	_, err = eng.Exec("SELECT SCORE() FROM ranked_reject")
	if err == nil {
		t.Fatal("SCORE() fuera de MATCH USING debe rechazarse")
	}
}

// Integra con el algoritmo real: los resultados SQL deben coincidir con ranking.Search
// sobre el Reader de la misma columna, incluido el desempate por DocID.
func TestRankedSQLMatchesRankingOracle(t *testing.T) {
	eng := newTestEngine(t, Options{})
	run(t, eng, "CREATE TABLE ranked_oracle (id INT PRIMARY KEY, body VARCHAR(120))")
	run(t, eng, `INSERT INTO ranked_oracle VALUES
        (1,'gatos gatos perros'),
        (2,'gatos perros'),
        (3,'elefantes naranjas'),
        (4,'gatos gatos gatos')`)
	tbl, err := eng.requireTable("ranked_oracle")
	if err != nil {
		t.Fatal(err)
	}
	col, ok := tbl.ColumnIndex("body")
	if !ok {
		t.Fatal("columna body no encontrada")
	}
	idx, err := tbl.rankedIndex(col)
	if err != nil {
		t.Fatal(err)
	}
	terms, err := textproc.PreprocessText("gatos perros")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		sql    string
		method ranking.Method
	}{
		{"BM25", ranking.MethodBM25}, {"TF_IDF", ranking.MethodTFIDF},
	} {
		want, err := ranking.Search(idx.reader, tc.method, terms, ranking.DefaultSearchOptions(3))
		if err != nil {
			t.Fatal(err)
		}
		res, err := eng.Exec(fmt.Sprintf(`SELECT id, SCORE() AS relevancia FROM ranked_oracle
            WHERE MATCH(body,'gatos perros') USING %s ORDER BY relevancia DESC LIMIT 3`, tc.sql))
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Rows) != len(want) {
			t.Fatalf("%s: SQL=%d, oracle=%d", tc.sql, len(res.Rows), len(want))
		}
		for i, hit := range want {
			row := idx.rows[hit.DocID]
			if fmt.Sprint(res.Rows[i][0]) != fmt.Sprint(row[0]) || res.Rows[i][1] != hit.Score {
				t.Fatalf("%s: resultado SQL %v != oracle DocID=%d score=%f", tc.sql, res.Rows[i], hit.DocID, hit.Score)
			}
		}
	}
}
