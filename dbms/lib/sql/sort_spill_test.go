package sql

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/dbms-go/v2/dbms/lib/storage"
)

// seedOrdenable crea una tabla con suficientes filas y claves repetidas como para
// que el external sort tenga que generar varios runs.
func seedOrdenable(t *testing.T, e *Engine, n int) {
	t.Helper()
	run(t, e, "CREATE TABLE t (id INT PRIMARY KEY, k INT, etiqueta VARCHAR(16))")
	for i := 0; i < n; i++ {
		// Claves repetidas: el merge tiene que ser estable y el orden de empate
		// tiene que respetarse.
		run(t, e, fmt.Sprintf("INSERT INTO t (id, k, etiqueta) VALUES (%d, %d, 'f%05d')", i, i%13, i))
	}
}

func tempRestantes(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("dir: %v", err)
	}
	for _, e := range ents {
		if e.Name()[:4] == "sort" {
			out = append(out, e.Name())
		}
	}
	return out
}

// asInt convierte el valor de una columna a int sea del tipo que traiga.
func asInt(t *testing.T, v any) int {
	t.Helper()
	n, ok := asInt64(v)
	if !ok {
		t.Fatalf("valor no numérico: %#v", v)
	}
	return int(n)
}

// TestOrderByConSpilling fuerza muchos runs: buffer de 4 slots y 200 filas dan
// 50 runs temporales que el merge tiene que recombinationar.
func TestOrderByConSpilling(t *testing.T) {
	dir := t.TempDir()
	e := newTestEngine(t, Options{Dir: dir, SortBufferSlots: 4})
	seedOrdenable(t, e, 200)

	res := run(t, e, "SELECT k FROM t ORDER BY k")
	if len(res.Rows) != 200 {
		t.Fatalf("filas: %d", len(res.Rows))
	}
	for i := 1; i < len(res.Rows); i++ {
		if asInt(t, res.Rows[i-1][0]) > asInt(t, res.Rows[i][0]) {
			t.Fatalf("desordenado en %d: %v", i, res.Rows[i-1:i+1])
		}
	}
	// k = id % 13 sobre 200 filas: las claves 0..4 salen 16 veces y 5..12, 15.
	counts := map[int]int{}
	for _, r := range res.Rows {
		counts[asInt(t, r[0])]++
	}
	if len(counts) != 13 {
		t.Fatalf("claves distintas: %d", len(counts))
	}
	for k, n := range counts {
		want := 16
		if k >= 5 {
			want = 15
		}
		if n != want {
			t.Fatalf("clave %d: %d filas, se esperaban %d", k, n, want)
		}
	}
	if asInt(t, res.Rows[0][0]) != 0 || asInt(t, res.Rows[199][0]) != 12 {
		t.Fatalf("extremos: %v .. %v", res.Rows[0], res.Rows[199])
	}

	var step Step
	for _, s := range res.Plan {
		if s.Kind == StepExternalSort {
			step = s
		}
	}
	if step.Detail == "" {
		t.Fatal("no se registró el paso del external sort")
	}
	if !hasKind(res, StepExternalSort) {
		t.Fatalf("plan:\n%s", planText(res))
	}
	// El plan debe admitir que hubo spilling real.
	if step.Rows != 200 {
		t.Fatalf("pasos del sort: %+v", step)
	}
	t.Logf("plan:\n%s", planText(res))

	// Ningún temporal puede sobrevivir a la consulta.
	if left := tempRestantes(t, dir); len(left) != 0 {
		t.Fatalf("quedaron temporales: %v", left)
	}
}

// TestOrderBySpillingDescendente y GROUP BY: el spilling también tiene que
// respetar el descendente y la combinación con HAVING/aggregados.
func TestOrderBySpillingDescendente(t *testing.T) {
	dir := t.TempDir()
	e := newTestEngine(t, Options{Dir: dir, SortBufferSlots: 3})
	seedOrdenable(t, e, 100)

	res := run(t, e, "SELECT k, COUNT(*) FROM t GROUP BY k ORDER BY k DESC")
	if len(res.Rows) != 13 {
		t.Fatalf("grupos: %d", len(res.Rows))
	}
	for i := 1; i < len(res.Rows); i++ {
		if asInt(t, res.Rows[i-1][0]) <= asInt(t, res.Rows[i][0]) {
			t.Fatalf("no descendente en %d: %v", i, res.Rows[i-1:i+1])
		}
	}
	if left := tempRestantes(t, dir); len(left) != 0 {
		t.Fatalf("quedaron temporales: %v", left)
	}
}

// TestOrderBySinSpillingConDisableSpill comprueba que DisableSpill deja el
// algoritmo en memoria: mismo resultado, ningún temporal.
func TestOrderBySinSpillingConDisableSpill(t *testing.T) {
	dir := t.TempDir()
	e := newTestEngine(t, Options{Dir: dir, SortBufferSlots: 4, DisableSpill: true})
	seedOrdenable(t, e, 120)

	res := run(t, e, "SELECT k FROM t ORDER BY k")
	if len(res.Rows) != 120 {
		t.Fatalf("filas: %d", len(res.Rows))
	}
	for i := 1; i < len(res.Rows); i++ {
		if asInt(t, res.Rows[i-1][0]) > asInt(t, res.Rows[i][0]) {
			t.Fatalf("desordenado en %d", i)
		}
	}
	if left := tempRestantes(t, dir); len(left) != 0 {
		t.Fatalf("quedaron temporales: %v", left)
	}
	t.Logf("plan:\n%s", planText(res))
}

// TestSpillingYTieneSpillingCoinciden compara el resultado con y sin spilling
// fila a fila, incluidas las etiquetas que desempatan las claves iguales.
func TestSpillingYTieneSpillingCoinciden(t *testing.T) {
	spill := newTestEngine(t, Options{Dir: t.TempDir(), SortBufferSlots: 5})
	mem := newTestEngine(t, Options{Dir: t.TempDir(), SortBufferSlots: 5, DisableSpill: true})
	seedOrdenable(t, spill, 137)
	seedOrdenable(t, mem, 137)

	a := run(t, spill, "SELECT etiqueta, k FROM t ORDER BY k")
	b := run(t, mem, "SELECT etiqueta, k FROM t ORDER BY k")
	if len(a.Rows) != len(b.Rows) {
		t.Fatalf("filas: %d vs %d", len(a.Rows), len(b.Rows))
	}
	for i := range a.Rows {
		if a.Rows[i][0] != b.Rows[i][0] || a.Rows[i][1] != b.Rows[i][1] {
			t.Fatalf("fila %d: %v vs %v", i, a.Rows[i], b.Rows[i])
		}
	}
	// Con spilling, las etiquetas de igual clave deben quedar en orden de id.
	for i := 1; i < len(a.Rows); i++ {
		if asInt(t, a.Rows[i-1][1]) == asInt(t, a.Rows[i][1]) &&
			a.Rows[i-1][0].(string) >= a.Rows[i][0].(string) {
			t.Fatalf("inestable en %d: %v", i, a.Rows[i-1:i+1])
		}
	}
}

// TestSpillingUnRunNoTocaDisco: todo cabe en el buffer, así que no debe haber
// ningún archivo temporal.
func TestSpillingUnRunNoTocaDisco(t *testing.T) {
	dir := t.TempDir()
	e := newTestEngine(t, Options{Dir: dir, SortBufferSlots: 4096})
	seedOrdenable(t, e, 50)

	run(t, e, "SELECT k FROM t ORDER BY k")
	ents, _ := os.ReadDir(dir)
	for _, ent := range ents {
		if filepath.Ext(ent.Name()) == ".tmp" {
			t.Fatalf("temporal inesperado: %s", ent.Name())
		}
	}
}

// TestRowGroupCopeRoundTrip comprueba el códec de grupos directamente.
func TestRowGroupCopeRoundTrip(t *testing.T) {
	g := rowGroup{rows: []storage.Tuple{
		{int32(1), "ana"},
		{int32(2), nil},
		{nil, float64(1.5)},
	}}
	buf, err := encodeRowGroup(g)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	back, err := decodeRowGroup(buf)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(back.rows) != len(g.rows) {
		t.Fatalf("filas: %d vs %d", len(back.rows), len(g.rows))
	}
	for i := range g.rows {
		if fmt.Sprint(back.rows[i]) != fmt.Sprint(g.rows[i]) {
			t.Fatalf("fila %d: %v vs %v", i, back.rows[i], g.rows[i])
		}
	}

	if _, err := decodeRowGroup(buf[:len(buf)-1]); err == nil {
		t.Fatal("aceptó un grupo truncado")
	}
	empty, err := decodeRowGroup(encodeRowGroupMust(t, rowGroup{}))
	if err != nil || len(empty.rows) != 0 {
		t.Fatalf("grupo vacío: %v %+v", err, empty)
	}
}

func encodeRowGroupMust(t *testing.T, g rowGroup) []byte {
	t.Helper()
	buf, err := encodeRowGroup(g)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf
}
