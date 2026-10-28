import { useEffect, useMemo, useRef, useState } from "react";
import { CornerDownLeft, Search } from "lucide-react";
import Modal from "./Modal";

const normalize = (text) =>
  text
    .toLowerCase()
    .normalize("NFD")
    .replace(/[̀-ͯ]/g, "");

// Paleta de comandos (Ctrl/⌘ + K): busca y ejecuta cualquier acción.
// commands: [{ id, label, group, hint?, icon?, run }]
function CommandPalette({ commands, onClose }) {
  const [term, setTerm] = useState("");
  const [index, setIndex] = useState(0);
  const listRef = useRef(null);

  const filtered = useMemo(() => {
    const q = normalize(term.trim());
    if (!q) return commands;
    return commands.filter((c) => normalize(`${c.label} ${c.group}`).includes(q));
  }, [commands, term]);

  const current = Math.min(index, Math.max(0, filtered.length - 1));

  useEffect(() => {
    listRef.current
      ?.querySelector(`[data-index="${current}"]`)
      ?.scrollIntoView({ block: "nearest" });
  }, [current]);

  const run = (command) => {
    if (!command) return;
    onClose();
    command.run();
  };

  const onKeyDown = (event) => {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      setIndex((current + 1) % Math.max(1, filtered.length));
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      setIndex((current - 1 + filtered.length) % Math.max(1, filtered.length));
    } else if (event.key === "Enter") {
      event.preventDefault();
      run(filtered[current]);
    }
  };

  return (
    <Modal variant="palette" onClose={onClose} title="">
      <div className="palette-search">
        <Search size={17} />
        <input
          autoFocus
          value={term}
          placeholder="Escribe un comando: ejecutar, formatear, historial, tema…"
          onChange={(e) => {
            setTerm(e.target.value);
            setIndex(0);
          }}
          onKeyDown={onKeyDown}
        />
        <kbd>Esc</kbd>
      </div>

      <div className="palette-list" ref={listRef}>
        {filtered.length === 0 && <p className="palette-empty">Ningún comando coincide.</p>}
        {filtered.map((command, i) => {
          const header = command.group !== filtered[i - 1]?.group ? command.group : null;
          return (
            <div key={command.id}>
              {header && <div className="palette-group">{header}</div>}
              <button
                data-index={i}
                className={i === current ? "palette-item active" : "palette-item"}
                onMouseEnter={() => setIndex(i)}
                onClick={() => run(command)}
              >
                <span className="palette-icon">{command.icon}</span>
                <span className="palette-label">{command.label}</span>
                {command.hint && <kbd>{command.hint}</kbd>}
                {i === current && <CornerDownLeft size={14} className="palette-enter" />}
              </button>
            </div>
          );
        })}
      </div>
    </Modal>
  );
}

export default CommandPalette;
