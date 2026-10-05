import { Play, Trash2, Copy, Database } from "lucide-react";

function QueryPanel({ query, onQueryChange, onExecute, loading }) {
  // Ejemplos rápidos para no tener que escribir todo a mano.
  const examples = [
    {
      name: "Crear tabla",
      sql: "CREATE TABLE alumno (codigo INT PRIMARY KEY, nombre VARCHAR(40), ciclo INT);",
    },
    {
      name: "Insertar alumnos",
      sql: "INSERT INTO alumno VALUES (1, 'Ana', 5), (2, 'Bob', 5), (3, 'Cid', 6);",
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
      name: "Rango (B+)",
      sql: "SELECT * FROM alumno WHERE codigo >= 1 AND codigo <= 2;",
    },
    {
      name: "Ordenar por ciclo",
      sql: "SELECT * FROM alumno ORDER BY ciclo DESC;",
    },
    {
      name: "GROUP BY",
      sql: "SELECT ciclo, COUNT(*) AS alumnos FROM alumno GROUP BY ciclo;",
    },
    {
      name: "Tabla matrícula",
      sql: "CREATE TABLE matricula (id INT PRIMARY KEY, codigo INT, curso VARCHAR(30));",
    },
    {
      name: "Insertar matrículas",
      sql: "INSERT INTO matricula VALUES (1, 1, 'BD2'), (2, 1, 'IA'), (3, 3, 'SO');",
    },
    {
      name: "JOIN",
      sql: "SELECT a.nombre, m.curso FROM alumno a JOIN matricula m ON a.codigo = m.codigo;",
    },
    {
      name: "Tabla espacial",
      sql: "CREATE TABLE ubicaciones (id INT PRIMARY KEY, nombre VARCHAR(60), tipo VARCHAR(30), ubicacion POINT);",
    },
    {
      name: "Insertar ubicaciones",
      sql: "INSERT INTO ubicaciones VALUES (1, 'Plaza de Armas', 'plaza', POINT(-12.0464, -77.0302)), (2, 'Parque Kennedy', 'parque', POINT(-12.1211, -77.0297)), (3, 'Estadio Nacional', 'estadio', POINT(-12.0670, -77.0336));",
    },
    {
      name: "Radio 5 km",
      sql: "SELECT nombre, distancia(ubicacion, POINT(-12.05, -77.03)) AS metros FROM ubicaciones WHERE distancia(ubicacion, POINT(-12.05, -77.03)) < 5000;",
    },
    {
      name: "k-NN",
      sql: "SELECT nombre FROM ubicaciones ORDER BY distancia(ubicacion, POINT(-12.05, -77.03)) LIMIT 2;",
    },
    {
      name: "Polígono",
      sql: "SELECT nombre FROM ubicaciones WHERE dentro(ubicacion, POLYGON(POINT(-12.00, -77.10), POINT(-12.00, -77.00), POINT(-12.10, -77.00), POINT(-12.10, -77.10))) = true;",
    },
    {
      name: "BEGIN",
      sql: "BEGIN TRANSACTION;",
    },
    {
      name: "END",
      sql: "END TRANSACTION;",
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

        <div className="query-info">SQL + espacial</div>
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
