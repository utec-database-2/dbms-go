import { useState } from "react";
import { Play, Trash2, Sparkles, Copy, Database } from "lucide-react";

function QueryPanel() {
  const [query, setQuery] = useState(
    `SELECT *
FROM alumno
WHERE ciclo = 5;`,
  );

  // Consultas de ejemplo
  const examples = [
    {
      name: "Seleccionar alumnos",
      sql: `SELECT *
FROM alumno
WHERE ciclo = 5;`,
    },
    {
      name: "Ordenar alumnos",
      sql: `SELECT *
FROM alumno
ORDER BY nombre;`,
    },
    {
      name: "Agrupar por carrera",
      sql: `SELECT carrera, COUNT(*)
FROM alumno
GROUP BY carrera;`,
    },
    {
      name: "Buscar por código",
      sql: `SELECT *
FROM alumno
WHERE codigo = 1001;`,
    },
  ];

  // Ejecutar consulta
  const handleExecute = () => {
    console.log("Consulta ejecutada:");
    console.log(query);
  };

  // Limpiar editor
  const handleClear = () => {
    setQuery("");
  };

  // Formato muy básico del SQL
  const handleFormat = () => {
    const formatted = query
      .replace(/\s+/g, " ")
      .replace(/\bFROM\b/gi, "\nFROM")
      .replace(/\bWHERE\b/gi, "\nWHERE")
      .replace(/\bORDER BY\b/gi, "\nORDER BY")
      .replace(/\bGROUP BY\b/gi, "\nGROUP BY")
      .replace(/\bJOIN\b/gi, "\nJOIN")
      .replace(/\bVALUES\b/gi, "\nVALUES");

    setQuery(formatted.trim());
  };

  // Copiar consulta
  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(query);

      console.log("Consulta copiada");
    } catch (error) {
      console.error("No se pudo copiar la consulta", error);
    }
  };

  return (
    <section className="query-panel">
      {/* ================================
          CABECERA DEL PANEL
      ================================= */}

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

      {/* ================================
          BARRA DE HERRAMIENTAS
      ================================= */}

      <div className="query-toolbar">
        <button className="query-button execute" onClick={handleExecute}>
          <Play size={15} />
          Ejecutar
        </button>

        {/* <button className="query-button" onClick={handleFormat}>
          <Sparkles size={15} />
          Formato
        </button> */}

        <button className="query-button" onClick={handleCopy}>
          <Copy size={15} />
          Copiar
        </button>

        <button className="query-button" onClick={handleClear}>
          <Trash2 size={15} />
          Limpiar
        </button>
      </div>

      {/* ================================
          EJEMPLOS
      ================================= */}

      <div className="query-examples">
        <span className="examples-label">Ejemplos:</span>

        {examples.map((example) => (
          <button
            key={example.name}
            className="example-button"
            onClick={() => setQuery(example.sql)}
          >
            {example.name}
          </button>
        ))}
      </div>

      {/* ================================
          EDITOR
      ================================= */}

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
          onChange={(event) => setQuery(event.target.value)}
          spellCheck="false"
          placeholder="Escribe aquí tu consulta SQL..."
        />
      </div>

      {/* ================================
          PIE DEL EDITOR
      ================================= */}

      <div className="query-footer">
        <span>Líneas: {query === "" ? 0 : query.split("\n").length}</span>

        <span>Caracteres: {query.length}</span>

        <span className="query-hint">Ctrl + Enter para ejecutar</span>
      </div>
    </section>
  );
}

export default QueryPanel;
