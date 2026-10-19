import { useState } from "react";
import {
  Table2,
  Search,
  Download,
  Copy,
  ChevronLeft,
  ChevronRight,
  CheckCircle2,
  AlertCircle,
  MapPin,
} from "lucide-react";
import { ExpandBackdrop, ExpandButton } from "./Expandable";
import { useExpandable } from "./useExpandable";
import { parsePoint } from "../lib/sql";

// Primera palabra de la sentencia (SELECT, INSERT…), para las pestañas.
function verbOf(sql) {
  return /^\s*(?:--[^\n]*\n\s*)*([A-Za-z]+)/.exec(sql)?.[1]?.toUpperCase() ?? "SQL";
}

const rowKey = (row) => row.map(String).join("\u241f");

function ResultPanel({
  result,
  error,
  loading,
  runs = [],
  activeRun = 0,
  onSelectRun,
  rowsPerPage: baseRowsPerPage = 5,
  linked,
  onLink,
}) {
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(1);
  const [expanded, setExpanded] = useExpandable();

  const [shownResult, setShownResult] = useState(result);
  if (result !== shownResult) {
    setShownResult(result);
    setPage(1);
  }

  const columns = result?.Columns || [];
  const rows = result?.Rows || [];
  // En la ventana grande caben más filas por página.
  const rowsPerPage = expanded ? Math.max(20, baseRowsPerPage) : baseRowsPerPage;

  const filteredRows = rows.filter((row) =>
    row.some((value) => String(value).toLowerCase().includes(search.toLowerCase())),
  );

  // Enlace con el mapa: la fila del punto elegido en el mapa (o la fila que
  // se eligió aquí) se resalta.
  const lowerColumns = columns.map((c) => String(c).toLowerCase());
  const idCol = lowerColumns.indexOf("id");
  const nameCol = lowerColumns.findIndex((c) => c === "nombre" || c === "name");
  const isLinked = (row) => {
    if (!linked) return false;
    if (linked.source === "table") return linked.key === rowKey(row);
    return (
      (idCol >= 0 && String(row[idCol]) === String(linked.id)) ||
      (nameCol >= 0 && row[nameCol] === linked.name)
    );
  };

  // Si el punto se eligió en el mapa, se salta a la página donde está su fila.
  const [shownLinked, setShownLinked] = useState(linked);
  if (linked !== shownLinked) {
    setShownLinked(linked);
    if (linked?.source === "map") {
      const index = filteredRows.findIndex(isLinked);
      if (index >= 0) setPage(Math.floor(index / rowsPerPage) + 1);
    }
  }

  const handleRowClick = (row) => {
    if (!onLink) return;
    const key = rowKey(row);
    if (linked?.source === "table" && linked.key === key) {
      onLink(null);
      return;
    }
    const point = row.map(parsePoint).find(Boolean);
    const firstText = row.find((v) => typeof v === "string" && !parsePoint(v));
    onLink({
      source: "table",
      key,
      id: idCol >= 0 ? row[idCol] : undefined,
      name: String(nameCol >= 0 ? row[nameCol] : (firstText ?? row[0])),
      ...(point || {}),
    });
  };

  const totalPages = Math.max(1, Math.ceil(filteredRows.length / rowsPerPage));
  const currentPage = Math.min(page, totalPages);
  const startIndex = (currentPage - 1) * rowsPerPage;
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
    <>
    <ExpandBackdrop expanded={expanded} onClose={() => setExpanded(false)} />
    <section className={expanded ? "result-panel panel-expanded" : "result-panel"}>
      <div className="result-header">
        <div className="result-title">
          <Table2 size={19} />
          <div>
            <h2>Panel de Resultados</h2>
            <p>Resultado de la consulta</p>
          </div>
        </div>

        <div className={`result-status${error ? " error" : !result ? " idle" : ""}`}>
          {error ? <AlertCircle size={15} /> : <CheckCircle2 size={15} />}
          <span>{statusText}</span>
        </div>
        <ExpandButton expanded={expanded} onToggle={setExpanded} />
      </div>

      {runs.length > 1 && (
        <div className="run-tabs" role="tablist">
          {runs.map((run, i) => (
            <button
              key={i}
              role="tab"
              aria-selected={i === activeRun}
              className={`run-tab${i === activeRun ? " active" : ""}${run.error ? " failed" : ""}`}
              onClick={() => onSelectRun(i)}
              title={run.sql}
            >
              {run.error ? <AlertCircle size={13} /> : <CheckCircle2 size={13} />}
              <b>{i + 1}</b>
              {verbOf(run.sql)}
            </button>
          ))}
        </div>
      )}

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
            {/* La key cambia con la página o el resultado para repetir la animación de las filas. */}
            <tbody key={`${currentPage}-${rowsPerPage}-${search}-${rows.length}-${result?.ElapsedMs}`}>
              {currentRows.length === 0 ? (
                <tr>
                  <td colSpan={columns.length + 1} className="empty-results">
                    No se encontraron resultados.
                  </td>
                </tr>
              ) : (
                currentRows.map((row, rowIndex) => {
                  const hasPoint = row.some((v) => parsePoint(v));
                  return (
                  <tr
                    key={rowIndex}
                    className={[isLinked(row) && "linked", hasPoint && "has-point"]
                      .filter(Boolean)
                      .join(" ")}
                    onClick={() => handleRowClick(row)}
                    title={hasPoint ? "Clic para ubicar este punto en el mapa" : "Clic para resaltar la fila"}
                  >
                    {row.map((value, columnIndex) => (
                      <td key={columnIndex}>
                        {parsePoint(value) && <MapPin size={12} className="cell-pin" />}
                        {String(value)}
                      </td>
                    ))}
                    <td className="row-action-cell">
                      <button
                        className="copy-row-button"
                        title="Copiar fila"
                        onClick={(event) => {
                          event.stopPropagation();
                          handleCopy(row);
                        }}
                      >
                        <Copy size={14} />
                      </button>
                    </td>
                  </tr>
                  );
                })
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
          Página {currentPage} de {totalPages}
        </span>

        <div className="pagination">
          <button
            className="pagination-button"
            disabled={currentPage === 1}
            onClick={() => setPage(Math.max(1, currentPage - 1))}
          >
            <ChevronLeft size={15} />
          </button>

          <button
            className="pagination-button"
            disabled={currentPage === totalPages}
            onClick={() => setPage(Math.min(totalPages, currentPage + 1))}
          >
            <ChevronRight size={15} />
          </button>
        </div>
      </div>
    </section>
    </>
  );
}

export default ResultPanel;
