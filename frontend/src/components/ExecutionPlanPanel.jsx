import { useState } from "react";
import {
  GitBranch,
  Search,
  Database,
  Filter,
  ArrowDownUp,
  Layers3,
  ChevronDown,
  ChevronRight,
  Clock3,
  HardDrive,
  Table2,
  Hash,
} from "lucide-react";

function ExecutionPlanPanel() {
  /*
    Datos simulados.
  */

  const plan = {
    query: `SELECT *
FROM alumno
WHERE ciclo = 5
ORDER BY nombre;`,

    executionTime: "12 ms",
    rowsReturned: 5,
    pagesRead: 18,

    root: {
      id: 1,
      type: "Sort",
      label: "SORT",
      description: "Ordenar por nombre",
      icon: "sort",
      details: {
        algorithm: "External K-Way Merge",
        field: "nombre",
        order: "ASC",
        rows: 5,
      },
      children: [
        {
          id: 2,
          type: "Filter",
          label: "FILTER",
          description: "ciclo = 5",
          icon: "filter",
          details: {
            condition: "ciclo = 5",
            rowsInput: 15432,
            rowsOutput: 5,
          },
          children: [
            {
              id: 3,
              type: "Index Scan",
              label: "B+ TREE INDEX SCAN",
              description: "idx_alumno_ciclo",
              icon: "index",
              details: {
                index: "idx_alumno_ciclo",
                key: "5",
                pagesRead: 18,
                rowsFound: 5,
              },
              children: [],
            },
          ],
        },
      ],
    },
  };

  const [expandedNodes, setExpandedNodes] = useState(new Set([1, 2, 3]));

  const [selectedNode, setSelectedNode] = useState(plan.root);

  const toggleNode = (nodeId) => {
    setExpandedNodes((previous) => {
      const next = new Set(previous);

      if (next.has(nodeId)) {
        next.delete(nodeId);
      } else {
        next.add(nodeId);
      }

      return next;
    });
  };

  const getIcon = (type) => {
    switch (type) {
      case "Sort":
        return <ArrowDownUp size={17} />;

      case "Filter":
        return <Filter size={17} />;

      case "Index Scan":
        return <Search size={17} />;

      case "Table Scan":
        return <Table2 size={17} />;

      case "Hash":
        return <Hash size={17} />;

      default:
        return <Database size={17} />;
    }
  };

  /*
    Componente recursivo para dibujar
    el árbol del plan.
  */

  const PlanNode = ({ node, level = 0 }) => {
    const isExpanded = expandedNodes.has(node.id);

    const hasChildren = node.children && node.children.length > 0;

    return (
      <div className="plan-node-container">
        <div
          className={
            selectedNode.id === node.id ? "plan-node selected" : "plan-node"
          }
          style={{
            marginLeft: `${level * 28}px`,
          }}
          onClick={() => setSelectedNode(node)}
        >
          {/* EXPANDIR / CONTRAER */}

          <div className="plan-expand">
            {hasChildren ? (
              <button
                className="expand-button"
                onClick={(event) => {
                  event.stopPropagation();
                  toggleNode(node.id);
                }}
              >
                {isExpanded ? (
                  <ChevronDown size={14} />
                ) : (
                  <ChevronRight size={14} />
                )}
              </button>
            ) : (
              <span className="expand-placeholder"></span>
            )}
          </div>

          {/* ICONO */}

          <div className="plan-node-icon">{getIcon(node.type)}</div>

          {/* INFORMACIÓN */}

          <div className="plan-node-content">
            <strong>{node.label}</strong>

            <span>{node.description}</span>
          </div>
        </div>

        {/* HIJOS */}

        {hasChildren && isExpanded && (
          <div className="plan-children">
            {node.children.map((child) => (
              <PlanNode key={child.id} node={child} level={level + 1} />
            ))}
          </div>
        )}
      </div>
    );
  };

  return (
    <section className="execution-panel">
      {/* =================================
          CABECERA
      ================================= */}

      <div className="execution-header">
        <div className="execution-title">
          <GitBranch size={19} />

          <div>
            <h2>Panel de Plan de Ejecución</h2>

            <p>Cómo se ejecutó la consulta</p>
          </div>
        </div>

        <div className="execution-status">
          <span className="execution-status-dot"></span>
          Plan disponible
        </div>
      </div>

      {/* =================================
          CONSULTA EJECUTADA
      ================================= */}

      <div className="execution-query">
        <div className="execution-query-label">
          <span>Consulta ejecutada</span>

          <Clock3 size={14} />
        </div>

        <pre>{plan.query}</pre>
      </div>

      {/* =================================
          RESUMEN
      ================================= */}

      <div className="execution-summary">
        <div className="execution-stat">
          <span>Tiempo</span>

          <strong>{plan.executionTime}</strong>
        </div>

        <div className="execution-stat">
          <span>Registros</span>

          <strong>{plan.rowsReturned}</strong>
        </div>

        <div className="execution-stat">
          <span>Páginas leídas</span>

          <strong>{plan.pagesRead}</strong>
        </div>

        <div className="execution-stat">
          <span>Operaciones</span>

          <strong>3</strong>
        </div>
      </div>

      {/* =================================
          CUERPO DEL PLAN
      ================================= */}

      <div className="execution-body">
        {/* ÁRBOL */}

        <div className="plan-tree">
          <div className="section-title">
            <Layers3 size={15} />

            <span>Operaciones</span>
          </div>

          <div className="tree-container">
            <PlanNode node={plan.root} />
          </div>
        </div>

        {/* DETALLES */}

        <div className="plan-details">
          <div className="section-title">
            <Database size={15} />

            <span>Detalles del operador</span>
          </div>

          <div className="operator-card">
            <div className="operator-header">
              <div className="operator-icon">{getIcon(selectedNode.type)}</div>

              <div>
                <span className="operator-label">Operador</span>

                <h3>{selectedNode.label}</h3>
              </div>
            </div>

            <div className="operator-description">
              {selectedNode.description}
            </div>

            {/* DETALLES DINÁMICOS */}

            <div className="operator-details">
              {Object.entries(selectedNode.details).map(([key, value]) => (
                <div className="detail-row" key={key}>
                  <span>{formatDetailName(key)}</span>

                  <strong>{String(value)}</strong>
                </div>
              ))}
            </div>

            {/* TIPO */}

            <div className="operator-type">
              <span>Tipo de operador</span>

              <strong>{selectedNode.type}</strong>
            </div>
          </div>
        </div>
      </div>

      {/* =================================
          PIE
      ================================= */}

      <div className="execution-footer">
        <span>
          <HardDrive size={13} />
          Motor de ejecución: MiniGestor DB
        </span>

        <span>3 operaciones ejecutadas</span>
      </div>
    </section>
  );
}



function formatDetailName(name) {
  const names = {
    algorithm: "Algoritmo",

    field: "Campo",

    order: "Orden",

    rows: "Registros",

    condition: "Condición",

    rowsInput: "Registros entrada",

    rowsOutput: "Registros salida",

    index: "Índice utilizado",

    key: "Clave buscada",

    pagesRead: "Páginas leídas",

    rowsFound: "Registros encontrados",
  };

  return names[name] || name;
}

export default ExecutionPlanPanel;
