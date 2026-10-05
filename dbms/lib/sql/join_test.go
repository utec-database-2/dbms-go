package sql

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

func seedMatriculas(t *testing.T, e *Engine) {
	t.Helper()
	run(t, e, "CREATE TABLE alumno (id INT PRIMARY KEY, nombre VARCHAR(20), ciclo INT)")
	run(t, e, "CREATE TABLE curso (cid INT PRIMARY KEY, titulo VARCHAR(20))")
	// matricula no tiene índice sobre alumno_id: obliga al hash join.
	run(t, e, "CREATE TABLE matricula (mid INT PRIMARY KEY, alumno_id INT, curso_id INT, nota INT)")
	run(t, e, "INSERT INTO alumno VALUES (1, 'Ana', 5), (2, 'Bob', 5), (3, 'Cid', 6), (4, 'Dan', 7)")
	run(t, e, "INSERT INTO curso VALUES (10, 'BD2'), (20, 'IA'), (30, 'SO')")
	run(t, e, `INSERT INTO matricula VALUES
		(100, 1, 10, 18), (101, 1, 20, 15), (102, 2, 10, 12), (103, 3, 30, 16), (104, 3, 10, 14)`)
}

func rowsText(res *Result) []string {
	out := make([]string, 0, len(res.Rows))
	for _, r := range res.Rows {
		parts := make([]string, len(r))
		for i, v := range r {
			parts[i] = fmt.Sprint(v)
		}
		out = append(out, strings.Join(parts, "|"))
	}
	return out
}

func TestJoinIndexNestedLoop(t *testing.T) {
	e := newTestEngine(t, Options{})
	seedMatriculas(t, e)

	// La derecha (alumno) tiene índice sobre id: index nested loop.
	res := run(t, e, `SELECT m.mid, a.nombre FROM matricula m JOIN alumno a ON m.alumno_id = a.id ORDER BY m.mid`)
	want := []string{"100|Ana", "101|Ana", "102|Bob", "103|Cid", "104|Cid"}
	if got := rowsText(res); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("join: %v\n%s", got, planText(res))
	}
	if !strings.Contains(planText(res), "index nested loop") {
		t.Fatalf("con índice en la derecha debe usar index nested loop:\n%s", planText(res))
	}
}

func TestJoinHashYMultiple(t *testing.T) {
	e := newTestEngine(t, Options{})
	seedMatriculas(t, e)

	// matricula no tiene índice sobre alumno_id: hash join.
	res := run(t, e, `SELECT a.nombre, c.titulo, m.nota
		FROM alumno a
		INNER JOIN matricula m ON a.id = m.alumno_id
		JOIN curso c ON m.curso_id = c.cid
		WHERE m.nota >= 14 AND a.ciclo = 5
		ORDER BY m.nota DESC`)
	want := []string{"Ana|BD2|18", "Ana|IA|15"}
	if got := rowsText(res); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("join múltiple: %v\n%s", got, planText(res))
	}
	plan := planText(res)
	if !strings.Contains(plan, "hash join") || !strings.Contains(plan, "index nested loop") {
		t.Fatalf("se esperaba hash join (matricula) e index nested loop (curso):\n%s", plan)
	}
}

func TestJoinLeftGroupByYAlias(t *testing.T) {
	e := newTestEngine(t, Options{})
	seedMatriculas(t, e)

	// LEFT JOIN conserva a Dan (sin matrículas) y GROUP BY cuenta por alumno.
	res := run(t, e, `SELECT a.nombre, COUNT(m.mid) AS cursos FROM alumno a
		LEFT JOIN matricula m ON a.id = m.alumno_id
		GROUP BY a.nombre ORDER BY a.nombre`)
	want := []string{"Ana|2", "Bob|1", "Cid|2", "Dan|0"}
	if got := rowsText(res); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("left join + group by: %v\n%s", got, planText(res))
	}

	// SELECT * devuelve las columnas calificadas de ambas tablas.
	res = run(t, e, "SELECT * FROM alumno a JOIN matricula m ON a.id = m.alumno_id WHERE a.id = 2")
	if len(res.Columns) != 7 || res.Columns[0] != "a.id" || len(res.Rows) != 1 {
		t.Fatalf("SELECT * del join: %v %v", res.Columns, res.Rows)
	}

	// Una columna sin calificar que está en ambas tablas es ambigua.
	run(t, e, "CREATE TABLE otro (id INT PRIMARY KEY, x INT)")
	run(t, e, "INSERT INTO otro VALUES (3, 30), (9, 90)")
	res = run(t, e, "SELECT nombre, x FROM alumno JOIN otro USING (id)")
	if got := strings.Join(rowsText(res), ","); got != "Cid|30" {
		t.Fatalf("JOIN USING: %s", got)
	}
	if _, err := e.Exec("SELECT id FROM alumno JOIN otro ON alumno.id = otro.id"); err == nil || !strings.Contains(err.Error(), "ambigua") {
		t.Fatalf("se esperaba error de ambigüedad, obtuve %v", err)
	}
}

// TestJoinHashVuelcaADisco fuerza un buffer chico para que el hash join y el
// group by repartan las particiones en disco, y compara con el resultado en
// memoria.
func TestJoinHashVuelcaADisco(t *testing.T) {
	dir := t.TempDir()
	e := newTestEngine(t, Options{Dir: dir, SortBufferSlots: 16})
	run(t, e, "CREATE TABLE a (id INT PRIMARY KEY, g INT)")
	run(t, e, "CREATE TABLE b (id INT PRIMARY KEY, a_id INT)")
	var av, bv []string
	for i := 1; i <= 200; i++ {
		av = append(av, fmt.Sprintf("(%d, %d)", i, i%7))
		bv = append(bv, fmt.Sprintf("(%d, %d)", i, (i*3)%200+1))
	}
	run(t, e, "INSERT INTO a VALUES "+strings.Join(av, ", "))
	run(t, e, "INSERT INTO b VALUES "+strings.Join(bv, ", "))

	res := run(t, e, "SELECT a.g, COUNT(*) FROM b JOIN a ON b.a_id = a.id GROUP BY a.g ORDER BY a.g")
	plan := planText(res)
	if !strings.Contains(plan, "volcadas a disco") {
		t.Fatalf("con buffer de 16 las particiones deberían ir a disco:\n%s", plan)
	}
	total := 0
	for _, r := range res.Rows {
		n, _ := asInt64(r[1])
		total += int(n)
	}
	if len(res.Rows) != 7 || total != 200 {
		t.Fatalf("group by tras el join: %v", res.Rows)
	}

	// Los temporales de las particiones se borran.
	entries, _ := os.ReadDir(dir)
	var tmp []string
	for _, en := range entries {
		if strings.HasSuffix(en.Name(), ".tmp") {
			tmp = append(tmp, en.Name())
		}
	}
	sort.Strings(tmp)
	if len(tmp) != 0 {
		t.Fatalf("quedaron temporales: %v", tmp)
	}
}
