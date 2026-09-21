import {
  Database,
  Moon,
  Sun,
  Settings
} from "lucide-react";

function Header({ darkMode, toggleTheme, apiOk }) {
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

        {/* ESTADO */}
        <div className="connection-status">
          <span className={apiOk ? "status-dot" : "status-dot offline"}></span>
          <span>{apiOk ? "Conectado" : "Servidor no disponible"}</span>
        </div>

        {/* CAMBIO DE TEMA */}
        <button
          className="theme-button"
          onClick={toggleTheme}
          title="Cambiar tema"
        >
          {darkMode ? (
            <Sun size={18} />
          ) : (
            <Moon size={18} />
          )}
        </button>

        {/* CONFIGURACIÓN */}
        <button
          className="icon-button"
          title="Configuración"
        >
          <Settings size={18} />
        </button>

      </div>

    </header>
  );
}

export default Header;