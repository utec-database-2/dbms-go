import { useState } from "react";
import { createPortal } from "react-dom";
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
  X,
} from "lucide-react";
import { ExpandBackdrop, ExpandButton } from "./Expandable";
import { useExpandable } from "./useExpandable";
import PerfChart from "./PerfChart";
import { PLAN_INFO, kindOfStep } from "../lib/planInfo";

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
function PlanTreeNode({ node, role, onTip }) {
  const [open, setOpen] = useState(true);
  const children = node.Children || [];
  const hasChildren = children.length > 0;

  return (
    <div className="qt-node">
      <div
        className="plan-node qt-card"
        onMouseEnter={(event) => onTip(node.Kind, event.currentTarget, node)}
        onMouseLeave={() => onTip(null)}
      >
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
        <div className="plan-node-icon" data-kind={node.Kind}>
          {iconForKind(node.Kind)}
        </div>
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
              onTip={onTip}
            />
          ))}
        </div>
      )}
    </div>
  );
}

// Globo con la explicación del operador. Se dibuja fuera del panel (portal)
// para que no lo recorte el scroll del árbol.
function OperatorTip({ tip }) {
  if (!tip?.container) return null;
  return createPortal(
    <div
      className={tip.above ? "op-tooltip above" : "op-tooltip"}
      style={{ left: tip.left, top: tip.top }}
      role="tooltip"
    >
      <strong>{tip.info.title}</strong>
      <p>{tip.info.what}</p>
      <p className="op-why">
        <b>¿Por qué?</b> {tip.info.why}
      </p>
      {tip.node && (
        <div className="op-stats">
          <span>{tip.node.Rows} fila(s)</span>
          {tip.node.Cost > 0 && <span>≈ {tip.node.Cost} página(s) leídas</span>}
        </div>
      )}
    </div>,
    tip.container,
  );
}

function ExecutionPlanPanel({ result, query, history = [], onClose }) {
  const plan = result?.Plan || [];
  const hasPlan = plan.length > 0;
  const tree = result?.Tree || null;
  const [view, setView] = useState("tree");
  const showTree = view === "tree" && tree;
  const [expanded, setExpanded] = useExpandable();
  const [tip, setTip] = useState(null);

  const showTip = (kind, target, node) => {
    const info = kind && PLAN_INFO[kind];
    if (!info) {
      setTip(null);
      return;
    }
    const rect = target.getBoundingClientRect();
    const below = rect.bottom + 190 < window.innerHeight;
    setTip({
      info,
      node,
      container: target.closest(".app"),
      left: Math.max(8, Math.min(rect.left + 24, window.innerWidth - 348)),
      top: below ? rect.bottom + 6 : rect.top - 6,
      above: !below,
    });
  };

  return (
    <>
    <ExpandBackdrop expanded={expanded} onClose={() => setExpanded(false)} />
    <section className={expanded ? "execution-panel panel-expanded" : "execution-panel"}>
      <OperatorTip tip={tip} />
      <div className="execution-header">
        <div className="execution-title">
          <GitBranch size={19} />
          <div>
            <h2>Panel de Plan de Ejecución</h2>
            <p>Cómo se resolvió la última consulta</p>
          </div>
        </div>

        <div className="execution-status">
          <span className={hasPlan ? "execution-status-dot" : "execution-status-dot idle"}></span>
          {hasPlan ? "Plan disponible" : "Sin consulta ejecutada"}
        </div>
        <ExpandButton expanded={expanded} onToggle={setExpanded} />
        {onClose && !expanded && (
          <button className="expand-toggle" onClick={onClose} title="Cerrar el plan de ejecución">
            <X size={16} />
          </button>
        )}
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
          <div className="stat-with-spark">
            <strong>{result ? `${result.ElapsedMs?.toFixed(2)} ms` : "—"}</strong>
            <PerfChart entries={history} compact limit={10} />
          </div>
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

          <div className="tree-container" onScroll={() => setTip(null)}>
            {showTree ? (
              <PlanTreeNode node={tree} onTip={showTip} />
            ) : hasPlan ? (
              plan.map((step, i) => (
                <div className="plan-node-container" key={i}>
                  <div
                    className="plan-node"
                    onMouseEnter={(event) => showTip(kindOfStep(step), event.currentTarget)}
                    onMouseLeave={() => setTip(null)}
                  >
                    <div className="plan-expand">
                      <span className="expand-placeholder">{i + 1}</span>
                    </div>
                    <div className="plan-node-icon" data-kind={kindOfStep(step)}>
                      {iconFor(step)}
                    </div>
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
    </>
  );
}

export default ExecutionPlanPanel;
