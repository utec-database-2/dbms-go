import { Database, Upload } from "lucide-react";

// Barra superior: nombre a la izquierda; importar CSV y estado del servidor a
// la derecha. El resto de herramientas está en la barra de la izquierda.
function Header({ apiOk, onImport }) {
  return (
    <header className="header">

      {/* LOGO Y NOMBRE */}
      <div className="brand">

        <div className="logo">
          <Database size={25} />
        </div>

        <div>
          <h1>MiniGestor BD</h1>
          <span>Gestor de Base de Datos Multimodal</span>
        </div>

      </div>

      {/* PARTE DERECHA */}
      <div className="header-actions">

        <button className="toolbar-button" onClick={onImport}>
          <Upload size={17} />
          <span>Importar CSV</span>
        </button>

        {/* ESTADO */}
        <div className="connection-status">
          <span className={apiOk ? "status-dot" : "status-dot offline"}></span>
          <span>{apiOk ? "Conectado" : "Servidor no disponible"}</span>
        </div>

      </div>

    </header>
  );
}

export default Header;
