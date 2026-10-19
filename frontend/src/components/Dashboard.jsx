import { useRef } from "react";

const MIN_COL = 0.16; // ningún panel queda más angosto que el 16 % de su fila
const MIN_ROW = 0.28;
const SPLITTER = 12; // px, igual que --splitter en App.css

// Divisor entre dos paneles: se arrastra para repartir el espacio y con doble
// clic vuelve a la distribución original.
function Splitter({ direction, onStart, onMove, onEnd, onReset }) {
  const dragging = useRef(false);

  return (
    <div
      className={`splitter splitter-${direction}`}
      role="separator"
      aria-orientation={direction === "col" ? "vertical" : "horizontal"}
      title="Arrastra para cambiar el tamaño · doble clic para restablecer"
      onPointerDown={(event) => {
        event.preventDefault();
        event.currentTarget.setPointerCapture(event.pointerId);
        dragging.current = true;
        document.body.classList.add(`resizing-${direction}`);
        onStart(event);
      }}
      onPointerMove={(event) => dragging.current && onMove(event)}
      onPointerUp={(event) => {
        if (!dragging.current) return;
        dragging.current = false;
        event.currentTarget.releasePointerCapture(event.pointerId);
        document.body.classList.remove(`resizing-${direction}`);
        onEnd?.();
      }}
      onDoubleClick={onReset}
    >
      <span className="splitter-grip" />
    </div>
  );
}

const template = (sizes) => sizes.map((s) => `minmax(0, ${s}fr)`).join(` ${SPLITTER}px `);

// Distribución de los 5 paneles: dos filas y divisores arrastrables. Los
// tamaños son fracciones guardadas en los ajustes (layout).
function Dashboard({ layout, onLayoutChange, onResetLayout, files, query, results, plan, map }) {
  const mainRef = useRef(null);
  const topRef = useRef(null);
  const bottomRef = useRef(null);
  const drag = useRef(null);

  const start = (key, index) => (event) => {
    const horizontal = key !== "rows";
    const container = { top: topRef, bottom: bottomRef, rows: mainRef }[key].current;
    const rect = container.getBoundingClientRect();
    const sizes = layout[key];
    const total = sizes.reduce((a, b) => a + b, 0);
    const length = (horizontal ? rect.width : rect.height) - SPLITTER * (sizes.length - 1);
    drag.current = {
      key,
      index,
      sizes: [...sizes],
      total,
      length,
      origin: horizontal ? event.clientX : event.clientY,
      horizontal,
    };
  };

  const move = (event) => {
    const d = drag.current;
    if (!d) return;
    const deltaPx = (d.horizontal ? event.clientX : event.clientY) - d.origin;
    const delta = (deltaPx / d.length) * d.total;
    const min = d.total * (d.horizontal ? MIN_COL : MIN_ROW);
    const pair = d.sizes[d.index] + d.sizes[d.index + 1];
    const a = Math.min(pair - min, Math.max(min, d.sizes[d.index] + delta));
    const next = [...d.sizes];
    next[d.index] = a;
    next[d.index + 1] = pair - a;
    onLayoutChange({ ...layout, [d.key]: next });
  };

  const end = () => {
    drag.current = null;
  };

  const splitter = (key, index) => (
    <Splitter
      direction={key === "rows" ? "row" : "col"}
      onStart={start(key, index)}
      onMove={move}
      onEnd={end}
      onReset={onResetLayout}
    />
  );

  return (
    <main
      className="dashboard"
      ref={mainRef}
      style={{ gridTemplateRows: template(layout.rows) }}
    >
      <div className="dash-row" ref={topRef} style={{ gridTemplateColumns: template(layout.top) }}>
        <div className="dash-cell">{files}</div>
        {splitter("top", 0)}
        <div className="dash-cell">{query}</div>
        {splitter("top", 1)}
        <div className="dash-cell">{results}</div>
      </div>

      {splitter("rows", 0)}

      <div
        className="dash-row"
        ref={bottomRef}
        style={{ gridTemplateColumns: template(layout.bottom) }}
      >
        <div className="dash-cell">{plan}</div>
        {splitter("bottom", 0)}
        <div className="dash-cell">{map}</div>
      </div>
    </main>
  );
}

export default Dashboard;
