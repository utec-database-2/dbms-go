import { useEffect, useRef, useState } from "react";
import { AlertTriangle, CheckCircle2, CloudUpload, FileSpreadsheet, Upload } from "lucide-react";
import Modal from "./Modal";

const PREVIEW_ROWS = 5;
const FULL_COUNT_LIMIT = 5 * 1024 * 1024; // hasta 5 MB se cuentan todas las filas

// Lector de CSV mínimo (comillas dobles y "" escapadas), solo para la vista
// previa: la importación real la hace el servidor.
function parseCsvLine(line) {
  const out = [];
  let cell = "";
  let quoted = false;
  for (let i = 0; i < line.length; i++) {
    const ch = line[i];
    if (quoted) {
      if (ch === '"' && line[i + 1] === '"') {
        cell += '"';
        i++;
      } else if (ch === '"') {
        quoted = false;
      } else {
        cell += ch;
      }
    } else if (ch === '"') {
      quoted = true;
    } else if (ch === ",") {
      out.push(cell);
      cell = "";
    } else {
      cell += ch;
    }
  }
  out.push(cell);
  return out;
}

async function readPreview(file) {
  const small = file.size <= FULL_COUNT_LIMIT;
  const text = await (small ? file : file.slice(0, 64 * 1024)).text();
  const lines = text.split(/\r?\n/).filter((l) => l.trim() !== "");
  const [headerLine, ...body] = lines;
  return {
    header: headerLine ? parseCsvLine(headerLine) : [],
    rows: body.slice(0, PREVIEW_ROWS).map(parseCsvLine),
    total: small ? body.length : null,
  };
}

function formatSize(bytes) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

// Diálogo de importación: elegir tabla, soltar el archivo y revisar una vista
// previa antes de mandarlo al servidor.
function ImportDialog({ tables, defaultTable, initialFile, onImport, onClose }) {
  const [tableName, setTableName] = useState(defaultTable || tables[0]?.Name || "");
  const [file, setFile] = useState(initialFile || null);
  const [preview, setPreview] = useState(null);
  const [previewError, setPreviewError] = useState(null);
  const [dragging, setDragging] = useState(false);
  const inputRef = useRef(null);

  useEffect(() => {
    if (!file) return;
    let active = true;
    readPreview(file)
      .then((p) => {
        if (!active) return;
        setPreview(p);
        setPreviewError(null);
      })
      .catch((err) => active && setPreviewError(err.message));
    return () => {
      active = false;
    };
  }, [file]);

  const table = tables.find((t) => t.Name === tableName);
  const tableColumns = table?.Columns || [];
  const header = preview?.header || [];
  const sameCount = header.length === tableColumns.length;

  const chooseFile = (chosen) => {
    if (!chosen) return;
    setPreview(null);
    setFile(chosen);
  };

  const onDrop = (event) => {
    event.preventDefault();
    setDragging(false);
    chooseFile(event.dataTransfer.files?.[0]);
  };

  return (
    <Modal
      title="Importar CSV"
      icon={<FileSpreadsheet size={18} />}
      onClose={onClose}
      className="import-dialog"
      footer={
        <>
          <button className="btn-secondary" onClick={onClose}>
            Cancelar
          </button>
          <button
            className="btn-primary"
            disabled={!file || !tableName}
            onClick={() => {
              onImport(tableName, file);
              onClose();
            }}
          >
            <Upload size={15} />
            Importar a {tableName || "…"}
          </button>
        </>
      }
    >
      <label className="field">
        <span>Tabla de destino</span>
        <select value={tableName} onChange={(e) => setTableName(e.target.value)}>
          {tables.map((t) => (
            <option key={t.Name} value={t.Name}>
              {t.Name} ({t.Records} fila(s))
            </option>
          ))}
        </select>
      </label>

      <input
        ref={inputRef}
        type="file"
        accept=".csv,text/csv"
        hidden
        onChange={(e) => {
          chooseFile(e.target.files?.[0]);
          e.target.value = "";
        }}
      />

      <button
        className={`drop-zone${dragging ? " dragging" : ""}${file ? " has-file" : ""}`}
        onClick={() => inputRef.current?.click()}
        onDragOver={(e) => {
          e.preventDefault();
          setDragging(true);
        }}
        onDragLeave={() => setDragging(false)}
        onDrop={onDrop}
      >
        <CloudUpload size={28} />
        {file ? (
          <>
            <strong>{file.name}</strong>
            <span>
              {formatSize(file.size)}
              {preview?.total != null && ` · ${preview.total} fila(s) de datos`} · clic para
              cambiarlo
            </span>
          </>
        ) : (
          <>
            <strong>Arrastra aquí tu archivo .csv</strong>
            <span>o haz clic para elegirlo</span>
          </>
        )}
      </button>

      {previewError && <div className="dialog-alert error">{previewError}</div>}

      {preview && table && (
        <>
          <div className={`dialog-alert ${sameCount ? "ok" : "warn"}`}>
            {sameCount ? <CheckCircle2 size={16} /> : <AlertTriangle size={16} />}
            <span>
              {sameCount
                ? `El CSV tiene ${header.length} columnas, igual que "${table.Name}". El servidor las toma en este orden:`
                : `El CSV tiene ${header.length} columna(s) y "${table.Name}" tiene ${tableColumns.length}. El servidor asume que el orden coincide; revisa la vista previa.`}
            </span>
          </div>

          <div className="column-map">
            {tableColumns.map((col, i) => (
              <span key={col.Name} className={header[i] !== undefined ? "" : "missing"}>
                <em>{header[i] ?? "—"}</em> → <strong>{col.Name}</strong>
                <small>{col.Type}</small>
              </span>
            ))}
          </div>

          <div className="preview-table">
            <table>
              <thead>
                <tr>
                  {header.map((h, i) => (
                    <th key={i}>{h}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {preview.rows.map((row, r) => (
                  <tr key={r}>
                    {header.map((_, c) => (
                      <td key={c}>{row[c]}</td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
            <p>
              Vista previa de las primeras {Math.min(PREVIEW_ROWS, preview.rows.length)} fila(s).
            </p>
          </div>
        </>
      )}
    </Modal>
  );
}

export default ImportDialog;
