import { useState } from "react";
import {
  GitBranch,
  Search,
  Database,
  ArrowDownUp,
  Layers3,
  Clock3,
  HardDrive,
  Table2,
  ListChecks,
  GitMerge,
  Boxes,
  Lock,
  Filter,
  ChevronDown,
  ChevronRight,
  ListTree,
  List,
  Scissors,
  Columns3,
} from "lucide-react";

// Elige un ícono según palabras clave del paso real que devolvió el
// executor (dbms/lib/sql), en vez de un árbol inventado.
function iconFor(step) {
  const s = step.toLowerCase();
  if (s.startsWith("[lock]")) return <Lock size={17} />;
  if (s.startsWith("[join]")) return <GitMerge size={17} />;
  if (s.startsWith("[group-by]")) return <Boxes size={17} />;
  if (s.includes("order by") || s.includes("sort")) return <ArrowDownUp size={17} />;
  if (
    s.includes("búsqueda") ||
    s.includes("scan") ||
    s.includes("r-tree") ||
    s.includes("k-nn")
  )
    return <Search size={17} />;
  if (s.includes("heap") || s.includes("recuperados")) return <Table2 size={17} />;
  return <Database size={17} />;
}

// Ícono de un operador del árbol según su tipo (Kind del motor).
function iconForKind(kind) {
  switch (kind) {
    case "seq-scan":
      return <Table2 size={17} />;
    case "index-seek":
    case "index-scan":
      return <Search size={17} />;
    case "filter":
      return <Filter size={17} />;
    case "project":
      return <Columns3 size={17} />;
    case "external-sort":
      return <ArrowDownUp size={17} />;
    case "group-by":
      return <Boxes size={17} />;
    case "join":
      return <GitMerge size={17} />;
    case "limit":
      return <Scissors size={17} />;
    default:
      return <Database size={17} />;
  }
}

// Rol de cada entrada de un JOIN, para leer el árbol de abajo hacia arriba.
function inputRole(parentKind, index) {
  if (parentKind !== "join") return null;
  return index === 0 ? "entrada izquierda" : "entrada derecha";
}

// Un operador del árbol de la consulta y, debajo, sus entradas.
function PlanTreeNode({ node, role }) {
  const [open, setOpen] = useState(true);
  const children = node.Children || [];
  const hasChildren = children.length > 0;

  return (
    <div className="qt-node">
      <div className="plan-node qt-card">
        <div className="plan-expand">
          {hasChildren ? (
            <button
              className="expand-button"
              onClick={() => setOpen(!open)}
              title={open ? "Contraer" : "Expandir"}
            >
              {open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
            </button>
          ) : (
            <span className="expand-placeholder"></span>
          )}
        </div>
        <div className="plan-node-icon">{iconForKind(node.Kind)}</div>
        <div className="plan-node-content qt-content">
          <strong>
            {node.Op}
            {role && <em className="qt-role">{role}</em>}
          </strong>
          <span>{node.Detail}</span>
        </div>
        <div className="qt-badges">
          <span>{node.Rows} fila(s)</span>
          {node.Cost > 0 && <span>costo ≈ {node.Cost}</span>}
        </div>
      </div>
      {hasChildren && open && (
        <div className="plan-children qt-children">
          {children.map((child, i) => (
            <PlanTreeNode
              key={i}
              node={child}
              role={inputRole(node.Kind, i)}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function ExecutionPlanPanel({ result, query }) {
  const plan = result?.Plan || [];
  const hasPlan = plan.length > 0;
  const tree = result?.Tree || null;
  const [view, setView] = useState("tree");
  const showTree = view === "tree" && tree;

  return (
    <section className="execution-panel">
      <div className="execution-header">
        <div className="execution-title">
          <GitBranch size={19} />
          <div>
            <h2>Panel de Plan de Ejecución</h2>
            <p>Cómo se resolvió la última consulta</p>
          </div>
        </div>

        <div className="execution-status">
          <span className="execution-status-dot"></span>
          {hasPlan ? "Plan disponible" : "Sin consulta ejecutada"}
        </div>
      </div>

      <div className="execution-query">
        <div className="execution-query-label">
          <span>Consulta ejecutada</span>
          <Clock3 size={14} />
        </div>
        <pre>{query || "—"}</pre>
      </div>

      <div className="execution-summary">
        <div className="execution-stat">
          <span>Tiempo</span>
          <strong>{result ? `${result.ElapsedMs?.toFixed(2)} ms` : "—"}</strong>
        </div>

        <div className="execution-stat">
          <span>Filas devueltas</span>
          <strong>{result?.Rows?.length ?? 0}</strong>
        </div>

        <div className="execution-stat">
          <span>Filas afectadas</span>
          <strong>{result?.Affected ?? 0}</strong>
        </div>

        <div className="execution-stat">
          <span>Pasos</span>
          <strong>{plan.length}</strong>
        </div>
      </div>

      <div className="execution-body qt-body">
        <div className="plan-tree" style={{ flex: 1 }}>
          <div className="section-title qt-title">
            <Layers3 size={15} />
            <span>
              {showTree
                ? "Árbol de la consulta (raíz = resultado, hojas = acceso a tablas)"
                : "Pasos reales de ejecución"}
            </span>
            <div className="qt-toggle">
              <button
                className={view === "tree" ? "active" : ""}
                onClick={() => setView("tree")}
                disabled={!tree}
              >
                <ListTree size={13} /> Árbol
              </button>
              <button
                className={view === "list" ? "active" : ""}
                onClick={() => setView("list")}
              >
                <List size={13} /> Pasos
              </button>
            </div>
          </div>

          <div className="tree-container">
            {showTree ? (
              <PlanTreeNode node={tree} />
            ) : hasPlan ? (
              plan.map((step, i) => (
                <div className="plan-node-container" key={i}>
                  <div className="plan-node">
                    <div className="plan-expand">
                      <span className="expand-placeholder">{i + 1}</span>
                    </div>
                    <div className="plan-node-icon">{iconFor(step)}</div>
                    <div className="plan-node-content">
                      <span>{step}</span>
                    </div>
                  </div>
                </div>
              ))
            ) : (
              <div className="plan-node-container">
                <div className="plan-node-content">
                  <span>
                    <ListChecks size={14} /> Ejecuta una consulta para ver aquí qué
                    índice o estrategia usó el motor.
                  </span>
                </div>
              </div>
            )}
          </div>
        </div>
      </div>

      <div className="execution-footer">
        <span>
          <HardDrive size={13} />
          Motor de ejecución: MiniGestor DB
        </span>
        <span>{plan.length} paso(s) ejecutados</span>
      </div>
    </section>
  );
}

export default ExecutionPlanPanel;
