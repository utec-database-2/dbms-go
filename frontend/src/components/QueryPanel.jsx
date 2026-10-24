import { useMemo, useRef, useState } from "react";
import {
  Braces,
  Columns3,
  Copy,
  Database,
  KeyRound,
  Play,
  Table2,
  Trash2,
  WandSparkles,
} from "lucide-react";
import { ExpandBackdrop, ExpandButton } from "./Expandable";
import { useExpandable } from "./useExpandable";
import { EXAMPLES } from "../lib/examples";
import {
  SQL_FUNCTIONS,
  SQL_KEYWORDS,
  formatSql,
  splitStatements,
  tokenClasses,
} from "../lib/sql";

const MAX_SUGGESTIONS = 8;
const KIND_ICON = {
  tabla: <Table2 size={13} />,
  columna: <Columns3 size={13} />,
  función: <Braces size={13} />,
  clave: <KeyRound size={13} />,
};

// Sugerencias para la palabra que se está escribiendo. Si la palabra anterior
// es FROM/JOIN/INTO/TABLE/UPDATE primero van las tablas; después de "alias."
// solo columnas.
function buildSuggestions(text, caret, tables, force) {
  const before = text.slice(0, caret);
  if ((before.match(/'/g) || []).length % 2 === 1) return null; // dentro de un texto

  const word = /[A-Za-z_][A-Za-z0-9_]*$/.exec(before)?.[0] ?? "";
  if (!word && !force) return null;

  const start = caret - word.length;
  const head = before.slice(0, start);
  const afterDot = head.endsWith(".");
  const prevWord = /([A-Za-z_]+)\s+$/.exec(head)?.[1]?.toUpperCase();
  const wantsTable = ["FROM", "JOIN", "INTO", "TABLE", "UPDATE"].includes(prevWord);

  const columnTables = new Map();
  for (const t of tables) {
    for (const c of t.Columns) {
      columnTables.set(c.Name, [...(columnTables.get(c.Name) || []), t.Name]);
    }
  }

  const tableItems = tables.map((t) => ({
    label: t.Name,
    insert: t.Name,
    kind: "tabla",
    detail: `${t.Records} fila(s)`,
  }));
  const columnItems = [...columnTables].map(([name, owners]) => ({
    label: name,
    insert: name,
    kind: "columna",
    detail: owners.join(", "),
  }));
  const functionItems = SQL_FUNCTIONS.map((f) => ({
    label: `${f}()`,
    insert: `${f}(`,
    kind: "función",
    detail: "función",
  }));
  const keywordItems = [...SQL_KEYWORDS].map((k) => ({
    label: k,
    insert: k,
    kind: "clave",
    detail: "palabra clave",
  }));

  const groups = afterDot
    ? [columnItems]
    : wantsTable
      ? [tableItems, columnItems, keywordItems]
      : [columnItems, functionItems, tableItems, keywordItems];

  const lower = word.toLowerCase();
  const seen = new Set();
  const items = [];
  for (const group of groups) {
    for (const item of group) {
      const name = item.insert.replace(/\($/, "").toLowerCase();
      if (!name.startsWith(lower) || name === lower || seen.has(item.label)) continue;
      seen.add(item.label);
      items.push(item);
      if (items.length >= MAX_SUGGESTIONS) break;
    }
    if (items.length >= MAX_SUGGESTIONS) break;
  }

  return items.length ? { items, start } : null;
}

function QueryPanel({ query, onQueryChange, onExecute, loading, tables, fontSize = 13 }) {
  const highlightRef = useRef(null);
  const linesRef = useRef(null);
  const mirrorRef = useRef(null);
  const textareaRef = useRef(null);
  const areaRef = useRef(null);
  const [expanded, setExpanded] = useExpandable();
  const [selection, setSelection] = useState("");
  const [ac, setAc] = useState(null); // { items, start, index, top, left, flip }

  const lineHeight = Math.round(fontSize * 1.7);
  const statementCount = useMemo(() => splitStatements(query).length, [query]);

  const handleClear = () => onQueryChange("");

  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(query);
    } catch (error) {
      console.error("No se pudo copiar la consulta", error);
    }
  };

  // Ejecuta lo seleccionado si hay selección; si no, todo el editor.
  const execute = () => onExecute(selection.trim() ? selection : undefined);

  // Posición del cursor dentro del editor, con un "espejo" invisible que tiene
  // la misma letra y el mismo ancho que el textarea.
  const caretPosition = (textarea, pos) => {
    const mirror = mirrorRef.current;
    mirror.textContent = textarea.value.slice(0, pos);
    const marker = document.createElement("span");
    marker.textContent = "​";
    mirror.appendChild(marker);
    return {
      top: marker.offsetTop - textarea.scrollTop,
      left: marker.offsetLeft - textarea.scrollLeft,
    };
  };

  const openSuggestions = (textarea, force = false) => {
    const caret = textarea.selectionStart;
    const found = buildSuggestions(textarea.value, caret, tables, force);
    if (!found) {
      setAc(null);
      return;
    }
    const { top, left } = caretPosition(textarea, found.start);
    const area = areaRef.current.getBoundingClientRect();
    const flip = top + lineHeight + 230 > area.height && top > 120;
    setAc({
      ...found,
      index: 0,
      top: flip ? top - 4 : top + lineHeight + 2,
      left: Math.max(4, Math.min(left, area.width - 280)),
      flip,
    });
  };

  const accept = (item) => {
    const textarea = textareaRef.current;
    const end = textarea.selectionEnd;
    const next = query.slice(0, ac.start) + item.insert + query.slice(end);
    const pos = ac.start + item.insert.length;
    onQueryChange(next);
    setAc(null);
    requestAnimationFrame(() => {
      textarea.focus();
      textarea.setSelectionRange(pos, pos);
    });
  };

  const handleKeyDown = (event) => {
    if (ac) {
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        const step = event.key === "ArrowDown" ? 1 : -1;
        setAc({ ...ac, index: (ac.index + step + ac.items.length) % ac.items.length });
        return;
      }
      if (event.key === "Enter" || event.key === "Tab") {
        if (!(event.ctrlKey || event.metaKey)) {
          event.preventDefault();
          accept(ac.items[ac.index]);
          return;
        }
      }
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        setAc(null);
        return;
      }
    }

    if ((event.ctrlKey || event.metaKey) && event.key === "Enter") {
      event.preventDefault();
      setAc(null);
      execute();
    } else if (event.ctrlKey && event.code === "Space") {
      event.preventDefault();
      openSuggestions(event.currentTarget, true);
    }
  };

  const handleChange = (event) => {
    onQueryChange(event.target.value);
    const inputType = event.nativeEvent?.inputType || "";
    const typed = event.nativeEvent?.data || "";
    if (inputType.startsWith("insert") && /^[A-Za-z0-9_.]$/.test(typed)) {
      openSuggestions(event.target);
    } else {
      setAc(null);
    }
  };

  return (
    <>
    <ExpandBackdrop expanded={expanded} onClose={() => setExpanded(false)} />
    <section className={expanded ? "query-panel panel-expanded" : "query-panel"}>
      <div className="query-header">
        <div className="query-title">
          <Database size={19} />
          <div>
            <h2>Panel de Consultas</h2>
            <p>Editor SQL</p>
          </div>
        </div>

        <div className="query-info">SQL + espacial</div>
        <ExpandButton expanded={expanded} onToggle={setExpanded} />
      </div>

      <div className="query-toolbar">
        <button
          className="query-button execute"
          onClick={execute}
          disabled={loading}
          title="Ctrl + Enter"
        >
          <Play size={15} />
          {loading ? "Ejecutando..." : selection.trim() ? "Ejecutar selección" : "Ejecutar"}
        </button>

        <button
          className="query-button"
          onClick={() => onQueryChange(formatSql(query))}
          disabled={!query.trim()}
          title="Ordena el SQL: palabras clave en mayúsculas y una cláusula por línea"
        >
          <WandSparkles size={15} />
          Formatear
        </button>

        <button className="query-button" onClick={handleCopy} title="Copiar">
          <Copy size={15} />
          <span className="query-button-label">Copiar</span>
        </button>

        <button className="query-button" onClick={handleClear} title="Limpiar">
          <Trash2 size={15} />
          <span className="query-button-label">Limpiar</span>
        </button>
      </div>

      <div className="query-examples">
        <span className="examples-label">Ejemplos:</span>

        {EXAMPLES.map((example) => (
          <button
            key={example.name}
            className="example-button"
            onClick={() => onQueryChange(example.sql)}
          >
            {example.name}
          </button>
        ))}
      </div>

      <div
        className="sql-editor-wrapper"
        style={{ "--editor-font": `${fontSize}px`, "--editor-line": `${lineHeight}px` }}
      >
        <div className="line-numbers" ref={linesRef}>
          {query === ""
            ? "1"
            : query
                .split("\n")
                .map((_, index) => <span key={index}>{index + 1}</span>)}
        </div>

        <div className="sql-editor-area" ref={areaRef}>
          <pre className="sql-highlight" ref={highlightRef} aria-hidden="true">
            {tokenClasses(query).map((tok, i) =>
              tok.cls ? (
                <span key={i} className={tok.cls}>
                  {tok.text}
                </span>
              ) : (
                tok.text
              ),
            )}
            {"\n"}
          </pre>

          {/* Copia invisible del texto para ubicar el cursor (autocompletado). */}
          <pre className="sql-highlight sql-mirror" ref={mirrorRef} aria-hidden="true" />

          <textarea
            ref={textareaRef}
            className="sql-editor"
            value={query}
            onChange={handleChange}
            onKeyDown={handleKeyDown}
            onSelect={(event) => {
              const { selectionStart, selectionEnd, value } = event.target;
              setSelection(value.slice(selectionStart, selectionEnd));
            }}
            onBlur={() => setAc(null)}
            onScroll={(event) => {
              const { scrollTop, scrollLeft } = event.target;
              if (highlightRef.current) {
                highlightRef.current.scrollTop = scrollTop;
                highlightRef.current.scrollLeft = scrollLeft;
              }
              if (linesRef.current) linesRef.current.scrollTop = scrollTop;
              setAc(null);
            }}
            spellCheck="false"
            placeholder="Escribe aquí tu consulta SQL… (puedes escribir varias separadas por ;)"
          />

          {ac && (
            <ul
              className={ac.flip ? "autocomplete flip" : "autocomplete"}
              style={{ top: ac.top, left: ac.left }}
              role="listbox"
            >
              {ac.items.map((item, i) => (
                <li
                  key={item.label}
                  role="option"
                  aria-selected={i === ac.index}
                  className={i === ac.index ? "active" : ""}
                  onMouseDown={(event) => {
                    event.preventDefault();
                    accept(item);
                  }}
                  onMouseEnter={() => setAc({ ...ac, index: i })}
                >
                  <span className={`ac-icon ac-${item.kind === "función" ? "fn" : item.kind}`}>
                    {KIND_ICON[item.kind]}
                  </span>
                  <strong>{item.label}</strong>
                  <small>{item.detail}</small>
                </li>
              ))}
              <li className="ac-hint" aria-hidden="true">
                ↑↓ elegir · Enter/Tab insertar · Esc cerrar
              </li>
            </ul>
          )}
        </div>
      </div>

      <div className="query-footer">
        <span>Sentencias: {statementCount}</span>
        <span>Líneas: {query === "" ? 0 : query.split("\n").length}</span>
        <span>Caracteres: {query.length}</span>
        <span className="query-hint">Ctrl+Enter ejecutar · Ctrl+Espacio sugerencias</span>
      </div>
    </section>
    </>
  );
}

export default QueryPanel;
