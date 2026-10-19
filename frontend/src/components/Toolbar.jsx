import { ClockArrowUp, Play, Upload } from "lucide-react";

function Toolbar({ onOpenImport, onOpenHistory, onExecute, loading }) {
  return (
    <div className="toolbar">
      <button className="toolbar-button" onClick={onOpenHistory} title="Historial y rendimiento">
        <ClockArrowUp size={17} />
        <span>Historial</span>
      </button>

      <button className="toolbar-button" onClick={onOpenImport}>
        <Upload size={17} />
        <span>Importar CSV</span>
      </button>

      <button className="toolbar-button execute" onClick={() => onExecute()} disabled={loading}>
        <Play size={17} />
        <span>{loading ? "Ejecutando..." : "Ejecutar (Ctrl+Enter)"}</span>
      </button>
    </div>
  );
}

export default Toolbar;
