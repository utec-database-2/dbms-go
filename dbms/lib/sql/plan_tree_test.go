package sql

import (
	"strings"
	"testing"
)

// shape describe el árbol como texto: Kind(hijo, hijo).
func shape(n *PlanNode) string {
	if n == nil {
		return "nil"
	}
	if len(n.Children) == 0 {
		return string(n.Kind)
	}
	parts := make([]string, len(n.Children))
	for i, c := range n.Children {
		parts[i] = shape(c)
	}
	return string(n.Kind) + "(" + strings.Join(parts, ", ") + ")"
}

func TestPlanTreeConsultaSimple(t *testing.T) {
	e := newTestEngine(t, Options{})
	seedMatriculas(t, e)

	res := run(t, e, "SELECT nombre FROM alumno WHERE ciclo = 5 ORDER BY nombre LIMIT 1")
	tree := res.PlanTree()
	want := "project(limit(external-sort(filter(seq-scan))))"
	if got := shape(tree); got != want {
		t.Fatalf("árbol: %s, se esperaba %s", got, want)
	}
	if tree.Op != "π Proyección" {
		t.Fatalf("la raíz debe ser la proyección, es %q", tree.Op)
	}
}

func TestPlanTreeJoin(t *testing.T) {
	e := newTestEngine(t, Options{})
	seedMatriculas(t, e)

	// alumno ⋈ matricula (hash join) ⋈ curso (index nested loop).
	res := run(t, e, `SELECT a.nombre, c.titulo FROM alumno a
		JOIN matricula m ON a.id = m.alumno_id
		JOIN curso c ON m.curso_id = c.cid
		WHERE m.nota > 10`)
	want := "project(filter(join(join(seq-scan, seq-scan), index-seek)))"
	if got := shape(res.PlanTree()); got != want {
		t.Fatalf("árbol del join: %s, se esperaba %s", got, want)
	}
	// π → σ → ⋈ externo → ⋈ interno (alumno ⋈ matricula).
	inner := res.PlanTree().Children[0].Children[0].Children[0]
	if !strings.Contains(inner.Children[1].Detail, "build") {
		t.Fatalf("la derecha del hash join es el lado build: %q", inner.Children[1].Detail)
	}
}

func TestPlanTreeSinOperadores(t *testing.T) {
	e := newTestEngine(t, Options{})
	res := run(t, e, "BEGIN TRANSACTION")
	if got := shape(res.PlanTree()); got != "begin" {
		t.Fatalf("BEGIN: %s", got)
	}
	run(t, e, "COMMIT")
}
