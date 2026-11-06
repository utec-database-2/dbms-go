import { useState } from "react";
import { ACCESS_LABEL } from "../lib/sql";

// Color por tipo de acceso (validado para daltonismo en claro y oscuro; ver
// --viz-* en App.css).
const ACCESS_ORDER = ["index", "scan", "none"];
const FILL = {
  index: "var(--viz-index)",
  scan: "var(--viz-scan)",
  none: "var(--viz-none)",
};

// Barra con las esquinas de arriba redondeadas y apoyada en la base.
function barPath(x, y, w, h, r) {
  const rr = Math.min(r, w / 2, h);
  return `M${x},${y + h} V${y + rr} Q${x},${y} ${x + rr},${y} H${x + w - rr} Q${x + w},${y} ${x + w},${y + rr} V${y + h} Z`;
}

function formatMs(ms) {
  if (ms == null) return "—";
  return ms < 10 ? `${ms.toFixed(2)} ms` : `${ms.toFixed(1)} ms`;
}

// Tiempo de las últimas consultas, una barra por consulta y coloreada según
// si el motor usó un índice, un recorrido secuencial o no leyó tablas.
// entries: historial (más antigua primero). compact = versión mini sin ejes.
function PerfChart({ entries, compact = false, limit = 20 }) {
  const [hover, setHover] = useState(null);
  const data = entries.filter((e) => e.ok && e.ms != null).slice(-limit);

  if (data.length === 0) {
    return compact ? null : (
      <p className="perf-empty">Ejecuta algunas consultas para comparar sus tiempos.</p>
    );
  }

  const W = compact ? 72 : 400;
  const H = compact ? 24 : 150;
  const pad = compact
    ? { top: 2, right: 0, bottom: 0, left: 0 }
    : { top: 14, right: 6, bottom: 20, left: 46 };
  const plotW = W - pad.left - pad.right;
  const plotH = H - pad.top - pad.bottom;
  const max = Math.max(...data.map((d) => d.ms), 0.001);
  const slot = plotW / (compact ? Math.max(data.length, 8) : Math.max(data.length, 10));
  const gap = 2;
  const barW = Math.max(2, slot - gap);

  const bars = data.map((d, i) => {
    const h = Math.max(2, (d.ms / max) * plotH);
    const x = pad.left + i * slot;
    return { d, i, x, y: pad.top + plotH - h, h };
  });

  if (compact) {
    return (
      <svg className="perf-spark" viewBox={`0 0 ${W} ${H}`} width={W} height={H} aria-hidden="true">
        {bars.map(({ d, i, x, y, h }) => (
          <path key={d.id ?? i} d={barPath(x, y, barW, h, 1.5)} style={{ fill: FILL[d.access] }}>
            <title>{`${formatMs(d.ms)} · ${ACCESS_LABEL[d.access]}`}</title>
          </path>
        ))}
      </svg>
    );
  }

  const counts = ACCESS_ORDER.map((a) => [a, data.filter((d) => d.access === a).length]);
  const active = hover != null ? bars[hover] : null;

  return (
    <div className="perf-chart">
      <div className="perf-legend">
        {counts.map(([access, n]) => (
          <span key={access}>
            <i style={{ background: FILL[access] }} />
            {ACCESS_LABEL[access]} <b>{n}</b>
          </span>
        ))}
      </div>

      <div className="perf-plot">
        <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label="Tiempo de las últimas consultas">
          <line
            className="perf-grid"
            x1={pad.left}
            x2={W - pad.right}
            y1={pad.top}
            y2={pad.top}
          />
          <line
            className="perf-axis"
            x1={pad.left}
            x2={W - pad.right}
            y1={pad.top + plotH}
            y2={pad.top + plotH}
          />
          <text className="perf-tick" x={pad.left - 6} y={pad.top + 4} textAnchor="end">
            {formatMs(max)}
          </text>
          <text className="perf-tick" x={pad.left - 6} y={pad.top + plotH} textAnchor="end">
            0
          </text>
          <text className="perf-tick" x={pad.left} y={H - 4}>
            más antigua
          </text>
          <text className="perf-tick" x={W - pad.right} y={H - 4} textAnchor="end">
            más reciente
          </text>

          {bars.map(({ d, i, x, y, h }) => (
            <path
              key={d.id ?? i}
              className={hover != null && hover !== i ? "perf-bar dim" : "perf-bar"}
              d={barPath(x, y, barW, h, 4)}
              style={{ fill: FILL[d.access] }}
            />
          ))}

          {/* Zonas de hover más anchas que la barra. */}
          {bars.map(({ d, i, x }) => (
            <rect
              key={`hit-${d.id ?? i}`}
              x={x - gap / 2}
              y={pad.top}
              width={slot}
              height={plotH}
              fill="transparent"
              onMouseEnter={() => setHover(i)}
              onMouseLeave={() => setHover(null)}
            />
          ))}
        </svg>

        {active && (
          <div
            className="perf-tooltip"
            style={{
              left: `${((active.x + barW / 2) / W) * 100}%`,
              top: `${(active.y / H) * 100}%`,
            }}
          >
            <strong>{formatMs(active.d.ms)}</strong>
            <span>
              <i style={{ background: FILL[active.d.access] }} />
              {ACCESS_LABEL[active.d.access]} · {active.d.rows ?? 0} fila(s)
            </span>
            <code>{active.d.sql}</code>
          </div>
        )}
      </div>
    </div>
  );
}

export default PerfChart;
