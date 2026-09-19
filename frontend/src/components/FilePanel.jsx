import { useState } from "react";
import {
  Database,
  Table2,
  KeyRound,
  ChevronDown,
  ChevronRight,
  HardDrive,
  Layers3,
} from "lucide-react";

function FilePanel() {
  // Tablas de ejemplo
  const tables = [
    {
      name: "alumno",
      columns: [
        { name: "codigo", type: "INT", key: true },
        { name: "nombre", type: "VARCHAR(100)", key: false },
        { name: "carrera", type: "VARCHAR(50)", key: false },
        { name: "ciclo", type: "INT", key: false },
        { name: "mensualidad", type: "DECIMAL(10,2)", key: false },
      ],
      records: 15432,
      pages: 256,
      size: "2.4 MB",
      storage: "Heap File",
      indexes: [
        { name: "idx_alumno_codigo", type: "B+ Tree" },
        { name: "idx_alumno_carrera", type: "Hash" },
      ],
    },

    {
      name: "curso",
      columns: [
        { name: "codigo", type: "INT", key: true },
        { name: "nombre", type: "VARCHAR(100)", key: false },
        { name: "creditos", type: "INT", key: false },
      ],
      records: 350,
      pages: 12,
      size: "120 KB",
      storage: "Sequential File",
      indexes: [{ name: "idx_curso_codigo", type: "B+ Tree" }],
    },

    {
      name: "carrera",
      columns: [
        { name: "codigo", type: "INT", key: true },
        { name: "nombre", type: "VARCHAR(100)", key: false },
      ],
      records: 25,
      pages: 2,
      size: "8 KB",
      storage: "Heap File",
      indexes: [],
    },

    {
      name: "matricula",
      columns: [
        { name: "codigo", type: "INT", key: true },
        { name: "codigo_alumno", type: "INT", key: false },
        { name: "codigo_curso", type: "INT", key: false },
        { name: "ciclo", type: "INT", key: false },
      ],
      records: 45210,
      pages: 620,
      size: "5.1 MB",
      storage: "Heap File",
      indexes: [{ name: "idx_matricula_alumno", type: "B+ Tree" }],
    },
  ];

  const [selectedTable, setSelectedTable] = useState(tables[0]);
  const [tablesOpen, setTablesOpen] = useState(true);
  const [indexesOpen, setIndexesOpen] = useState(true);

  return (
    <section className="file-panel">
      {/* CABECERA */}
      <div className="panel-title">
        <div>
          <h2>Panel de Archivos</h2>
          <p>Tablas y estructura</p>
        </div>

        <Database size={20} />
      </div>

      <div className="file-content">
        {/* =========================
            COLUMNA IZQUIERDA
        ========================== */}
        <div className="file-tree">
          {/* BASE DE DATOS */}
          <div className="tree-database">
            <Database size={17} />

            <span>universidad_db</span>
          </div>

          {/* TABLAS */}
          <div className="tree-section">
            <button
              className="tree-section-header"
              onClick={() => setTablesOpen(!tablesOpen)}
            >
              {tablesOpen ? (
                <ChevronDown size={15} />
              ) : (
                <ChevronRight size={15} />
              )}

              <span>Tablas ({tables.length})</span>
            </button>

            {tablesOpen && (
              <div className="tree-items">
                {tables.map((table) => (
                  <button
                    key={table.name}
                    className={
                      selectedTable.name === table.name
                        ? "tree-item selected"
                        : "tree-item"
                    }
                    onClick={() => setSelectedTable(table)}
                  >
                    <Table2 size={16} />

                    <span>{table.name}</span>
                  </button>
                ))}
              </div>
            )}
          </div>

          {/* ÍNDICES */}
          <div className="tree-section">
            <button
              className="tree-section-header"
              onClick={() => setIndexesOpen(!indexesOpen)}
            >
              {indexesOpen ? (
                <ChevronDown size={15} />
              ) : (
                <ChevronRight size={15} />
              )}

              <span>Índices</span>
            </button>

            {indexesOpen && (
              <div className="tree-items">
                {tables.flatMap((table) =>
                  table.indexes.map((index) => (
                    <div
                      key={`${table.name}-${index.name}`}
                      className="tree-item index-item"
                    >
                      <KeyRound size={14} />

                      <span>{index.name}</span>
                    </div>
                  )),
                )}
              </div>
            )}
          </div>
        </div>

        {/* =========================
            COLUMNA DERECHA
        ========================== */}
        <div className="table-info">
          <div className="table-info-header">
            <div>
              <span className="info-label">Estructura de la tabla</span>

              <h3>{selectedTable.name}</h3>
            </div>

            <Table2 size={22} />
          </div>

          {/* ESTRUCTURA */}
          <div className="structure-table">
            <div className="structure-header">
              <span>Campo</span>
              <span>Tipo</span>
              <span>Clave</span>
            </div>

            {selectedTable.columns.map((column) => (
              <div className="structure-row" key={column.name}>
                <span>{column.name}</span>

                <span className="column-type">{column.type}</span>

                <span>
                  {column.key && <KeyRound size={14} className="primary-key" />}
                </span>
              </div>
            ))}
          </div>

          {/* INFORMACIÓN DE LA TABLA */}
          <div className="storage-info">
            <div className="storage-title">
              <Layers3 size={16} />
              Información de almacenamiento
            </div>

            <div className="storage-grid">
              <div className="storage-card">
                <span>Registros</span>
                <strong>{selectedTable.records.toLocaleString()}</strong>
              </div>

              <div className="storage-card">
                <span>Páginas</span>
                <strong>{selectedTable.pages}</strong>
              </div>

              <div className="storage-card">
                <span>Tamaño</span>
                <strong>{selectedTable.size}</strong>
              </div>

              <div className="storage-card">
                <span>Archivo</span>
                <strong>{selectedTable.storage}</strong>
              </div>
            </div>
          </div>

          {/* ÍNDICES DE LA TABLA SELECCIONADA */}
          <div className="table-indexes">
            <div className="storage-title">
              <HardDrive size={16} />
              Índices de {selectedTable.name}
            </div>

            {selectedTable.indexes.length === 0 ? (
              <p className="no-indexes">Esta tabla no tiene índices.</p>
            ) : (
              <div className="selected-index-list">
                {selectedTable.indexes.map((index) => (
                  <div className="selected-index" key={index.name}>
                    <KeyRound size={14} />

                    <div>
                      <strong>{index.name}</strong>

                      <span>{index.type}</span>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      </div>
    </section>
  );
}

export default FilePanel;
