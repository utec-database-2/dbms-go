import { useEffect, useState } from "react";
import {
  Table2,
  Search,
  Download,
  Copy,
  ChevronLeft,
  ChevronRight,
  CheckCircle2,
  AlertCircle,
} from "lucide-react";

function ResultPanel({ result, error, loading }) {
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(1);

  useEffect(() => {
    setPage(1);
  }, [result]);

  const columns = result?.Columns || [];
  const rows = result?.Rows || [];
  const rowsPerPage = 5;

  const filteredRows = rows.filter((row) =>
    row.some((value) => String(value).toLowerCase().includes(search.toLowerCase())),
  );

  const totalPages = Math.max(1, Math.ceil(filteredRows.length / rowsPerPage));
  const startIndex = (page - 1) * rowsPerPage;
  const currentRows = filteredRows.slice(startIndex, startIndex + rowsPerPage);

  const handleSearch = (value) => {
    setSearch(value);
    setPage(1);
  };

  const handleCopy = async (row) => {
    try {
      await navigator.clipboard.writeText(row.join("\t"));
    } catch (error) {
      console.error("No se pudo copiar la fila", error);
    }
  };

  const handleExport = () => {
    if (columns.length === 0) return;
    const csvHeader = columns.join(",");
    const csvRows = filteredRows.map((row) => row.map((v) => `"${v}"`).join(","));
    const csvContent = [csvHeader, ...csvRows].join("\n");
    const blob = new Blob([csvContent], { type: "text/csv;charset=utf-8;" });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = "resultados.csv";
    link.click();
    URL.revokeObjectURL(url);
  };

  const hasTable = columns.length > 0;
  const statusText = error
    ? "Error"
    : loading
      ? "Ejecutando..."
      : result
        ? result.Message || "Consulta ejecutada"
        : "Sin consultas aún";

  return (
    <section className="result-panel">
      <div className="result-header">
        <div className="result-title">
          <Table2 size={19} />
          <div>
            <h2>Panel de Resultados</h2>
            <p>Resultado de la consulta</p>
          </div>
        </div>

        <div className={`result-status${error ? " error" : ""}`}>
          {error ? <AlertCircle size={15} /> : <CheckCircle2 size={15} />}
          <span>{statusText}</span>
        </div>
      </div>

      {error && <div className="result-error">{error}</div>}

      <div className="result-toolbar">
        <div className="result-search">
          <Search size={15} />
          <input
            type="text"
            placeholder="Buscar en resultados..."
            value={search}
            onChange={(event) => handleSearch(event.target.value)}
            disabled={!hasTable}
          />
        </div>

        <div className="result-actions">
          <button className="result-button" onClick={handleExport} disabled={!hasTable}>
            <Download size={15} />
            Exportar CSV
          </button>
        </div>
      </div>

      <div className="result-info">
        <span>
          Registros encontrados:
          <strong> {hasTable ? filteredRows.length : 0}</strong>
        </span>
        <span>
          Columnas:
          <strong> {columns.length}</strong>
        </span>
        <span>
          Tiempo:
          <strong> {result ? `${result.ElapsedMs?.toFixed(2)} ms` : "—"}</strong>
        </span>
        {result?.Affected > 0 && (
          <span>
            Filas afectadas:
            <strong> {result.Affected}</strong>
          </span>
        )}
      </div>

      <div className="result-table-wrapper">
        {hasTable ? (
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
                      <td key={columnIndex}>{String(value)}</td>
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
        ) : (
          <div className="empty-results">
            {result?.Message
              ? result.Message
              : "Ejecuta un SELECT para ver resultados aquí."}
          </div>
        )}
      </div>

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
            onClick={() => setPage((previous) => Math.min(totalPages, previous + 1))}
          >
            <ChevronRight size={15} />
          </button>
        </div>
      </div>
    </section>
  );
}

export default ResultPanel;
