import { useState } from "react";
import {
  Database,
  Table2,
  KeyRound,
  ChevronDown,
  ChevronRight,
  Layers3,
} from "lucide-react";

function FilePanel({ tables, error, selectedTableName, onSelectTable }) {
  const [tablesOpen, setTablesOpen] = useState(true);

  const selectedTable = tables.find((t) => t.Name === selectedTableName) || null;

  return (
    <section className="file-panel">
      <div className="panel-title">
        <div>
          <h2>Panel de Archivos</h2>
          <p>Tablas y estructura</p>
        </div>
        <Database size={20} />
      </div>

      <div className="file-content">
        <div className="file-tree">
          <div className="tree-database">
            <Database size={17} />
            <span>minigestor_db</span>
          </div>

          <div className="tree-section">
            <button
              className="tree-section-header"
              onClick={() => setTablesOpen(!tablesOpen)}
            >
              {tablesOpen ? <ChevronDown size={15} /> : <ChevronRight size={15} />}
              <span>Tablas ({tables.length})</span>
            </button>

            {tablesOpen && (
              <div className="tree-items">
                {error && <p className="no-indexes">No se pudo conectar al servidor: {error}</p>}
                {!error && tables.length === 0 && (
                  <p className="no-indexes">
                    Sin tablas todavía. Crea una con CREATE TABLE en el editor.
                  </p>
                )}
                {tables.map((table) => (
                  <button
                    key={table.Name}
                    className={
                      selectedTableName === table.Name ? "tree-item selected" : "tree-item"
                    }
                    onClick={() => onSelectTable(table.Name)}
                  >
                    <Table2 size={16} />
                    <span>{table.Name}</span>
                  </button>
                ))}
              </div>
            )}
          </div>
        </div>

        <div className="table-info">
          {selectedTable ? (
            <>
              <div className="table-info-header">
                <div>
                  <span className="info-label">Estructura de la tabla</span>
                  <h3>{selectedTable.Name}</h3>
                </div>
                <Table2 size={22} />
              </div>

              <div className="structure-table">
                <div className="structure-header">
                  <span>Campo</span>
                  <span>Tipo</span>
                  <span>Clave</span>
                </div>

                {selectedTable.Columns.map((column) => (
                  <div className="structure-row" key={column.Name}>
                    <span>{column.Name}</span>
                    <span className="column-type">{column.Type}</span>
                    <span>{column.Key && <KeyRound size={14} className="primary-key" />}</span>
                  </div>
                ))}
              </div>

              <div className="storage-info">
                <div className="storage-title">
                  <Layers3 size={16} />
                  Información de almacenamiento
                </div>

                <div className="storage-grid">
                  <div className="storage-card">
                    <span>Registros</span>
                    <strong>{selectedTable.Records.toLocaleString()}</strong>
                  </div>
                  <div className="storage-card">
                    <span>Páginas</span>
                    <strong>{selectedTable.Pages}</strong>
                  </div>
                  <div className="storage-card">
                    <span>Índice</span>
                    <strong>{selectedTable.IndexType}</strong>
                  </div>
                  <div className="storage-card">
                    <span>Archivo</span>
                    <strong>Heap File</strong>
                  </div>
                </div>
              </div>

              <div className="table-indexes">
                <div className="storage-title">
                  <KeyRound size={16} />
                  Índice de {selectedTable.Name}
                </div>
                <div className="selected-index-list">
                  <div className="selected-index">
                    <KeyRound size={14} />
                    <div>
                      <strong>{selectedTable.IndexName}</strong>
                      <span>{selectedTable.IndexType}</span>
                    </div>
                  </div>
                </div>
              </div>
            </>
          ) : (
            <p className="no-indexes">
              Selecciona una tabla, o créala primero con CREATE TABLE en el Panel de
              Consultas.
            </p>
          )}
        </div>
      </div>
    </section>
  );
}

export default FilePanel;
