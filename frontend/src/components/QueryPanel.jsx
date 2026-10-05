import { Play, Trash2, Copy, Database } from "lucide-react";

function QueryPanel({ query, onQueryChange, onExecute, loading }) {
  // Ejemplos rápidos para no tener que escribir todo a mano.
  const examples = [
    {
      name: "Crear tabla",
      sql: "CREATE TABLE alumno (codigo INT, nombre STRING, ciclo INT);",
    },
    {
      name: "Insertar alumno",
      sql: 'INSERT INTO alumno VALUES (1, "Ana", 5);',
    },
    {
      name: "Ver todos",
      sql: "SELECT * FROM alumno;",
    },
    {
      name: "Buscar por código",
      sql: "SELECT * FROM alumno WHERE codigo = 1;",
    },
    {
      name: "Ordenar por ciclo",
      sql: "SELECT * FROM alumno ORDER BY ciclo DESC;",
    },
  ];

  const handleClear = () => onQueryChange("");

  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(query);
    } catch (error) {
      console.error("No se pudo copiar la consulta", error);
    }
  };

  const handleKeyDown = (event) => {
    if ((event.ctrlKey || event.metaKey) && event.key === "Enter") {
      event.preventDefault();
      onExecute();
    }
  };

  return (
    <section className="query-panel">
      <div className="query-header">
        <div className="query-title">
          <Database size={19} />
          <div>
            <h2>Panel de Consultas</h2>
            <p>Editor SQL</p>
          </div>
        </div>

        <div className="query-info">SQL básico</div>
      </div>

      <div className="query-toolbar">
        <button
          className="query-button execute"
          onClick={onExecute}
          disabled={loading}
        >
          <Play size={15} />
          {loading ? "Ejecutando..." : "Ejecutar"}
        </button>

        <button className="query-button" onClick={handleCopy}>
          <Copy size={15} />
          Copiar
        </button>

        <button className="query-button" onClick={handleClear}>
          <Trash2 size={15} />
          Limpiar
        </button>
      </div>

      <div className="query-examples">
        <span className="examples-label">Ejemplos:</span>

        {examples.map((example) => (
          <button
            key={example.name}
            className="example-button"
            onClick={() => onQueryChange(example.sql)}
          >
            {example.name}
          </button>
        ))}
      </div>

      <div className="sql-editor-wrapper">
        <div className="line-numbers">
          {query === ""
            ? "1"
            : query
                .split("\n")
                .map((_, index) => <span key={index}>{index + 1}</span>)}
        </div>

        <textarea
          className="sql-editor"
          value={query}
          onChange={(event) => onQueryChange(event.target.value)}
          onKeyDown={handleKeyDown}
          spellCheck="false"
          placeholder="Escribe aquí tu consulta SQL..."
        />
      </div>

      <div className="query-footer">
        <span>Líneas: {query === "" ? 0 : query.split("\n").length}</span>
        <span>Caracteres: {query.length}</span>
        <span className="query-hint">Ctrl + Enter para ejecutar</span>
      </div>
    </section>
  );
}

export default QueryPanel;
