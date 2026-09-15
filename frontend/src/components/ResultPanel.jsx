import { useState } from "react";
import {
  Table2,
  Search,
  Download,
  Copy,
  ChevronLeft,
  ChevronRight,
  CheckCircle2,
} from "lucide-react";

function ResultPanel() {
  // Datos simulados
  const columns = ["codigo", "nombre", "carrera", "ciclo", "mensualidad"];

  const initialRows = [
    ["001", "Jose", "Ciencia de la Computación", 5, "850.00"],
    ["002", "Ana", "Ingeniería de Software", 5, "900.00"],
    ["003", "Carlos", "Ciencia de la Computación", 5, "850.00"],
    ["004", "Maria", "Ingeniería de Sistemas", 5, "870.00"],
    ["005", "Luis", "Ciencia de la Computación", 5, "850.00"],
    ["006", "Sofia", "Ingeniería de Software", 5, "900.00"],
    ["007", "Diego", "Ciencia de la Computación", 5, "850.00"],
    ["008", "Lucia", "Ingeniería de Sistemas", 5, "870.00"],
  ];

  const [rows, setRows] = useState(initialRows);
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(1);

  const rowsPerPage = 5;

  // Filtrar resultados
  const filteredRows = rows.filter((row) =>
    row.some((value) =>
      String(value).toLowerCase().includes(search.toLowerCase()),
    ),
  );

  const totalPages = Math.max(1, Math.ceil(filteredRows.length / rowsPerPage));

  const startIndex = (page - 1) * rowsPerPage;

  const currentRows = filteredRows.slice(startIndex, startIndex + rowsPerPage);

  // Cuando cambia la búsqueda volvemos a la primera página
  const handleSearch = (value) => {
    setSearch(value);
    setPage(1);
  };

  // Copiar fila completa
  const handleCopy = async (row) => {
    try {
      await navigator.clipboard.writeText(row.join("\t"));

      console.log("Fila copiada");
    } catch (error) {
      console.error("No se pudo copiar la fila", error);
    }
  };

  // Exportar CSV
  const handleExport = () => {
    const csvHeader = columns.join(",");

    const csvRows = filteredRows.map((row) =>
      row.map((value) => `"${value}"`).join(","),
    );

    const csvContent = [csvHeader, ...csvRows].join("\n");

    const blob = new Blob([csvContent], { type: "text/csv;charset=utf-8;" });

    const url = URL.createObjectURL(blob);

    const link = document.createElement("a");

    link.href = url;
    link.download = "resultados.csv";

    link.click();

    URL.revokeObjectURL(url);
  };

  return (
    <section className="result-panel">
      {/* =========================
          CABECERA
      ========================== */}

      <div className="result-header">
        <div className="result-title">
          <Table2 size={19} />

          <div>
            <h2>Panel de Resultados</h2>

            <p>Resultado de la consulta</p>
          </div>
        </div>

        <div className="result-status">
          <CheckCircle2 size={15} />

          <span>Consulta ejecutada</span>
        </div>
      </div>

      {/* =========================
          BARRA DE RESULTADOS
      ========================== */}

      <div className="result-toolbar">
        <div className="result-search">
          <Search size={15} />

          <input
            type="text"
            placeholder="Buscar en resultados..."
            value={search}
            onChange={(event) => handleSearch(event.target.value)}
          />
        </div>

        <div className="result-actions">
          <button className="result-button" onClick={handleExport}>
            <Download size={15} />
            Exportar CSV
          </button>
        </div>
      </div>

      {/* =========================
          INFORMACIÓN
      ========================== */}

      <div className="result-info">
        <span>
          Registros encontrados:
          <strong> {filteredRows.length}</strong>
        </span>

        <span>
          Columnas:
          <strong> {columns.length}</strong>
        </span>

        <span>
          Tiempo:
          <strong> 12 ms</strong>
        </span>
      </div>

      {/* =========================
          TABLA
      ========================== */}

      <div className="result-table-wrapper">
        <table className="result-table">
          <thead>
            <tr>
              {columns.map((column) => (
                <th key={column}>{column}</th>
              ))}

              <th className="actions-column">Acción</th>
            </tr>
          </thead>

          <tbody>
            {currentRows.length === 0 ? (
              <tr>
                <td colSpan={columns.length + 1} className="empty-results">
                  No se encontraron resultados.
                </td>
              </tr>
            ) : (
              currentRows.map((row, rowIndex) => (
                <tr key={rowIndex}>
                  {row.map((value, columnIndex) => (
                    <td key={columnIndex}>{value}</td>
                  ))}

                  <td className="row-action-cell">
                    <button
                      className="copy-row-button"
                      title="Copiar fila"
                      onClick={() => handleCopy(row)}
                    >
                      <Copy size={14} />
                    </button>
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>

      {/* =========================
          PAGINACIÓN
      ========================== */}

      <div className="result-footer">
        <span className="page-info">
          Página {page} de {totalPages}
        </span>

        <div className="pagination">
          <button
            className="pagination-button"
            disabled={page === 1}
            onClick={() => setPage((previous) => Math.max(1, previous - 1))}
          >
            <ChevronLeft size={15} />
          </button>

          <button
            className="pagination-button"
            disabled={page === totalPages}
            onClick={() =>
              setPage((previous) => Math.min(totalPages, previous + 1))
            }
          >
            <ChevronRight size={15} />
          </button>
        </div>
      </div>
    </section>
  );
}

export default ResultPanel;
