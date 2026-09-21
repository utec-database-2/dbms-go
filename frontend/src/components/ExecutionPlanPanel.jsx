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
} from "lucide-react";

// Elige un ícono según palabras clave del paso real que devolvió el
// executor (dbms/lib/sql), en vez de un árbol inventado.
function iconFor(step) {
  const s = step.toLowerCase();
  if (s.includes("order by") || s.includes("sort")) return <ArrowDownUp size={17} />;
  if (s.includes("búsqueda") || s.includes("scan")) return <Search size={17} />;
  if (s.includes("heap") || s.includes("recuperados")) return <Table2 size={17} />;
  return <Database size={17} />;
}

function ExecutionPlanPanel({ result, query }) {
  const plan = result?.Plan || [];
  const hasPlan = plan.length > 0;

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

      <div className="execution-body">
        <div className="plan-tree" style={{ flex: 1 }}>
          <div className="section-title">
            <Layers3 size={15} />
            <span>Pasos reales de ejecución</span>
          </div>

          <div className="tree-container">
            {hasPlan ? (
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
