package sql

// Árbol del plan de ejecución.
//
// El plan se guarda como la lista de pasos en orden de ejecución (Result.Plan).
// PlanTree la convierte en el árbol de operadores de la consulta: cada paso
// consume el resultado del anterior, así que es su padre, y un JOIN tiene
// además como hijo el acceso a la tabla de la derecha. La raíz es el último
// operador (normalmente la proyección π) y las hojas son los accesos a las
// tablas: recorridos secuenciales o búsquedas en índices.

// PlanNode es un operador del árbol del plan.
type PlanNode struct {
	// Op es el nombre del operador en notación de álgebra relacional.
	Op string
	// Kind es el tipo de paso del motor (seq-scan, index-seek, join, ...).
	Kind StepKind
	// Detail explica la decisión del planificador.
	Detail string
	// Rows son las filas que produce el operador.
	Rows int
	// Cost es la estimación de E/S del operador.
	Cost int
	// Children son las entradas del operador. En un JOIN, la primera es la
	// izquierda y la segunda la derecha.
	Children []*PlanNode
}

// PlanTree devuelve el árbol de operadores de la consulta, o nil si el plan no
// tiene operadores (por ejemplo, un COMMIT). Los bloqueos no son operadores:
// quedan fuera del árbol y se ven en la lista de pasos.
func (r *Result) PlanTree() *PlanNode {
	if r == nil {
		return nil
	}
	return planTree(r.Plan)
}

func planTree(steps []Step) *PlanNode {
	var cur *PlanNode
	for _, st := range steps {
		if st.Kind == StepLock {
			continue
		}
		n := &PlanNode{Op: operatorName(st.Kind), Kind: st.Kind, Detail: st.Detail, Rows: st.Rows, Cost: st.Cost}
		if cur != nil {
			n.Children = append(n.Children, cur)
		}
		for _, in := range st.Inputs {
			if child := planTree([]Step{in}); child != nil {
				n.Children = append(n.Children, child)
			}
		}
		cur = n
	}
	return cur
}

// operatorName da el nombre del operador, con su símbolo del álgebra
// relacional cuando lo tiene.
func operatorName(k StepKind) string {
	switch k {
	case StepScan:
		return "Recorrido secuencial"
	case StepIndexSeek:
		return "Búsqueda en índice"
	case StepIndexScan:
		return "Recorrido de índice"
	case StepFilter:
		return "σ Selección"
	case StepProject:
		return "π Proyección"
	case StepExternalSort:
		return "τ Ordenamiento externo"
	case StepGroupBy:
		return "γ Agrupación"
	case StepJoin:
		return "⋈ Join"
	case StepLimit:
		return "Límite"
	case StepInsert:
		return "Inserción"
	case StepUpdate:
		return "Actualización"
	case StepDelete:
		return "Eliminación"
	case StepBegin:
		return "BEGIN"
	case StepCommit:
		return "COMMIT"
	case StepRollback:
		return "ROLLBACK"
	}
	return string(k)
}
