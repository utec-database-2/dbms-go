package sorting

import (
	"math/rand"
	"sort"
	"testing"
)

type row struct {
	key int
	id  int
}

func keys(rows []row) []int {
	out := make([]int, len(rows))
	for i, r := range rows {
		out[i] = r.key
	}
	return out
}

func sortedAsc(t *testing.T, got []row) {
	t.Helper()
	if !sort.SliceIsSorted(got, func(i, j int) bool { return got[i].key < got[j].key }) {
		t.Fatalf("no quedó ordenado: %v", keys(got))
	}
}

// TestExternalSortUnRun solo verifica que un conjunto que cabe en el buffer se
// resuelve sin generar runs.
func TestExternalSortUnRun(t *testing.T) {
	rows := []row{{3, 0}, {1, 1}, {2, 2}}
	out, runs, err := ExternalSort(rows, func(r row) (any, error) { return r.key, nil }, false, 64)
	if err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("runs = %d, se esperaba 1", runs)
	}
	sortedAsc(t, out)
	if len(out) != 3 || out[0].key != 1 || out[2].key != 3 {
		t.Fatalf("resultado: %v", keys(out))
	}
}

// TestExternalSortVariosRuns fuerza el k-way merge con buffers de 2 y 3 slots.
func TestExternalSortVariosRuns(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	rows := make([]row, 40)
	for i := range rows {
		rows[i] = row{key: rng.Intn(15), id: i}
	}
	for _, buf := range []int{1, 2, 3, 7, 100} {
		out, runs, err := ExternalSort(rows, func(r row) (any, error) { return r.key, nil }, false, buf)
		if err != nil {
			t.Fatalf("buffer=%d: %v", buf, err)
		}
		if len(out) != len(rows) {
			t.Fatalf("buffer=%d: %d filas", buf, len(out))
		}
		sortedAsc(t, out)
		want := (len(rows) + buf - 1) / buf
		if runs != want {
			t.Fatalf("buffer=%d: runs = %d, se esperaba %d", buf, runs, want)
		}
	}
}

// TestExternalSortDescendente comprueba que el merge respeta el orden inverso.
func TestExternalSortDescendente(t *testing.T) {
	rows := []row{{1, 0}, {5, 1}, {3, 2}, {4, 3}, {2, 4}}
	out, _, err := ExternalSort(rows, func(r row) (any, error) { return r.key, nil }, true, 2)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(out); i++ {
		if out[i-1].key < out[i].key {
			t.Fatalf("orden descendente roto: %v", keys(out))
		}
	}
	if len(out) != len(rows) {
		t.Fatalf("se perdieron filas: %v", keys(out))
	}
}

// TestExternalSortEstable mantiene el orden de llegada entre claves iguales.
func TestExternalSortEstable(t *testing.T) {
	rows := []row{{1, 0}, {1, 1}, {0, 2}, {1, 3}}
	out, _, err := ExternalSort(rows, func(r row) (any, error) { return r.key, nil }, false, 2)
	if err != nil {
		t.Fatal(err)
	}
	got := []int{out[1].id, out[2].id, out[3].id}
	want := []int{0, 1, 3}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("estabilidad: %v, se esperaba %v", got, want)
		}
	}
}

// TestExternalSortTiposMixtos comprueba la comparación entre tipos distintos.
func TestExternalSortTiposMixtos(t *testing.T) {
	rows := [][]any{{"b"}, {"a"}, {"c"}}
	out, _, err := ExternalSort(rows, func(r []any) (any, error) { return r[0], nil }, false, 2)
	if err != nil {
		t.Fatal(err)
	}
	if out[0][0] != "a" || out[2][0] != "c" {
		t.Fatalf("resultado: %v", out)
	}
}

// TestExternalSortErrorPropaga un error de la función de clave.
func TestExternalSortErrorPropaga(t *testing.T) {
	rows := []row{{1, 0}, {2, 1}}
	_, _, err := ExternalSort(rows, func(r row) (any, error) {
		if r.key == 2 {
			return nil, errClave
		}
		return r.key, nil
	}, false, 8)
	if err == nil {
		t.Fatal("se esperaba un error")
	}
}

var errClave = errorString("clave inválida")

type errorString string

func (e errorString) Error() string { return string(e) }
