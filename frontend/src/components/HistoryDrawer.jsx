import { useMemo, useState } from "react";
import { AlertCircle, CheckCircle2, ClockArrowUp, Play, Search, SquarePen, Trash2 } from "lucide-react";
import Modal from "./Modal";
import PerfChart from "./PerfChart";
import { ACCESS_LABEL } from "../lib/sql";

function timeAgo(at) {
  const s = Math.round((Date.now() - at) / 1000);
  if (s < 60) return "hace un momento";
  const m = Math.round(s / 60);
  if (m < 60) return `hace ${m} min`;
  const h = Math.round(m / 60);
  if (h < 24) return `hace ${h} h`;
  return new Date(at).toLocaleDateString();
}

// Historial de consultas (guardado en el navegador) y la gráfica de tiempos.
function HistoryDrawer({ history, onLoad, onRun, onClear, onClose }) {
  const [term, setTerm] = useState("");

  const items = useMemo(() => {
    const q = term.trim().toLowerCase();
    const list = [...history].reverse();
    return q ? list.filter((h) => h.sql.toLowerCase().includes(q)) : list;
  }, [history, term]);

  return (
    <Modal
      variant="drawer"
      title="Historial y rendimiento"
      icon={<ClockArrowUp size={18} />}
      onClose={onClose}
      footer={
        <>
          <span className="drawer-count">{history.length} consulta(s)</span>
          <button className="btn-secondary danger" onClick={onClear} disabled={!history.length}>
            <Trash2 size={15} />
            Borrar historial
          </button>
        </>
      }
    >
      <section className="drawer-section">
        <h3>Tiempo de las últimas consultas</h3>
        <PerfChart entries={history} />
      </section>

      <section className="drawer-section">
        <div className="history-search">
          <Search size={15} />
          <input
            value={term}
            onChange={(e) => setTerm(e.target.value)}
            placeholder="Buscar en el historial…"
          />
        </div>

        {items.length === 0 && (
          <p className="perf-empty">
            {history.length ? "Nada coincide con la búsqueda." : "Todavía no ejecutaste consultas."}
          </p>
        )}

        <ul className="history-list">
          {items.map((h) => (
            <li key={h.id} className={h.ok ? "" : "failed"}>
              <div className="history-meta">
                {h.ok ? <CheckCircle2 size={14} className="ok" /> : <AlertCircle size={14} className="bad" />}
                <span>{timeAgo(h.at)}</span>
                {h.ok && <span>{h.ms?.toFixed(2)} ms</span>}
                {h.ok && <span>{h.rows ?? 0} fila(s)</span>}
                {h.ok && <span className={`access-chip ${h.access}`}>{ACCESS_LABEL[h.access]}</span>}
              </div>
              <code className="history-sql">{h.sql}</code>
              {!h.ok && <p className="history-error">{h.error}</p>}
              <div className="history-actions">
                <button onClick={() => onLoad(h.sql)} title="Copiar al editor">
                  <SquarePen size={14} />
                  Al editor
                </button>
                <button onClick={() => onRun(h.sql)} title="Ejecutar otra vez">
                  <Play size={14} />
                  Ejecutar
                </button>
              </div>
            </li>
          ))}
        </ul>
      </section>
    </Modal>
  );
}

export default HistoryDrawer;
