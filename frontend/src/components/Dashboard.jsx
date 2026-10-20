import { useRef } from "react";

const MIN_FRACTION = 0.18; // ningún panel queda con menos del 18 % de su espacio
const SPLITTER = 10; // px, igual que el ancho de .splitter en App.css

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
        onEnd();
      }}
      onDoubleClick={onReset}
    >
      <span className="splitter-grip" />
    </div>
  );
}

const template = (sizes) => sizes.map((s) => `minmax(0, ${s}fr)`).join(` ${SPLITTER}px `);

// Distribución principal (estilo pgAdmin): Archivos a la izquierda; Consultas
// arriba y Resultados abajo a la derecha. El Plan de ejecución, cuando está
// abierto, se parte al lado de Resultados (como dos terminales en VS Code).
//
// layout = { side: [archivos, derecha], rows: [consultas, abajo],
//            bottom: [resultados, plan] } en fracciones (fr).
function Dashboard({ layout, onLayoutChange, onResetLayout, showPlan, files, query, results, plan }) {
  const mainRef = useRef(null);
  const rightRef = useRef(null);
  const bottomRef = useRef(null);
  const drag = useRef(null);

  // key: qué par de tamaños cambia; horizontal: si se arrastra en X o en Y.
  // El contenedor se busca recién al arrastrar (no durante el render).
  const start = (key, horizontal) => (event) => {
    const container = { side: mainRef, rows: rightRef, bottom: bottomRef }[key].current;
    const rect = container.getBoundingClientRect();
    const sizes = layout[key];
    drag.current = {
      key,
      horizontal,
      sizes: [...sizes],
      total: sizes[0] + sizes[1],
      length: (horizontal ? rect.width : rect.height) - SPLITTER,
      origin: horizontal ? event.clientX : event.clientY,
    };
  };

  const move = (event) => {
    const d = drag.current;
    if (!d) return;
    const deltaPx = (d.horizontal ? event.clientX : event.clientY) - d.origin;
    const delta = (deltaPx / d.length) * d.total;
    const min = d.total * MIN_FRACTION;
    const first = Math.min(d.total - min, Math.max(min, d.sizes[0] + delta));
    onLayoutChange({ ...layout, [d.key]: [first, d.total - first] });
  };

  const end = () => {
    drag.current = null;
  };

  const splitter = (key, horizontal) => (
    <Splitter
      direction={horizontal ? "col" : "row"}
      onStart={start(key, horizontal)}
      onMove={move}
      onEnd={end}
      onReset={onResetLayout}
    />
  );

  return (
    <main
      className="dashboard"
      ref={mainRef}
      style={{ gridTemplateColumns: template(layout.side) }}
    >
      <div className="dash-cell">{files}</div>

      {splitter("side", true)}

      <div className="dash-col" ref={rightRef} style={{ gridTemplateRows: template(layout.rows) }}>
        <div className="dash-cell">{query}</div>

        {splitter("rows", false)}

        <div
          className="dash-row"
          ref={bottomRef}
          style={{ gridTemplateColumns: showPlan ? template(layout.bottom) : "minmax(0, 1fr)" }}
        >
          <div className="dash-cell">{results}</div>
          {showPlan && splitter("bottom", true)}
          {showPlan && <div className="dash-cell plan-cell">{plan}</div>}
        </div>
      </div>
    </main>
  );
}

export default Dashboard;
