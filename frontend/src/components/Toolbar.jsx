import { useRef } from "react";
import { Upload, Play } from "lucide-react";

function Toolbar({ tables, selectedTableName, onImport, onExecute, loading }) {
  const fileInputRef = useRef(null);

  const handleImportClick = () => {
    if (!tables?.length) {
      alert("Primero crea una tabla con CREATE TABLE en el Panel de Consultas.");
      return;
    }
    fileInputRef.current?.click();
  };

  const handleFileChosen = (event) => {
    const file = event.target.files?.[0];
    event.target.value = ""; // permite re-seleccionar el mismo archivo después
    if (!file) return;

    const names = tables.map((t) => t.Name).join(", ");
    const target = window.prompt(
      `¿A qué tabla quieres importar "${file.name}"?\nTablas disponibles: ${names}`,
      selectedTableName || tables[0]?.Name || "",
    );
    if (!target) return;
    onImport(target, file);
  };

  return (
    <div className="toolbar">
      <input
        ref={fileInputRef}
        type="file"
        accept=".csv,text/csv"
        style={{ display: "none" }}
        onChange={handleFileChosen}
      />

      <button className="toolbar-button" onClick={handleImportClick}>
        <Upload size={17} />
        <span>Importar CSV</span>
      </button>

      <button className="toolbar-button execute" onClick={onExecute} disabled={loading}>
        <Play size={17} />
        <span>{loading ? "Ejecutando..." : "Ejecutar (Ctrl+Enter)"}</span>
      </button>
    </div>
  );
}

export default Toolbar;
