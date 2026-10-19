import { Command, Database, Lock, Moon, Settings, Sun } from "lucide-react";

const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform);

function Header({ darkMode, toggleTheme, apiOk, txn, onOpenSettings, onOpenPalette }) {
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

        {/* TRANSACCIÓN ABIERTA (entre BEGIN y END/COMMIT/ROLLBACK) */}
        {txn?.active && (
          <div className="txn-badge" title="Los cambios quedan pendientes hasta END TRANSACTION (COMMIT) o ROLLBACK">
            <Lock size={14} />
            <span>Transacción {txn.id} abierta</span>
          </div>
        )}

      </div>

      {/* PARTE DERECHA */}
      <div className="header-actions">

        {/* PALETA DE COMANDOS */}
        <button className="palette-button" onClick={onOpenPalette} title="Buscar comandos">
          <Command size={15} />
          <span>Comandos</span>
          <kbd>{isMac ? "⌘K" : "Ctrl K"}</kbd>
        </button>

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
          title="Ajustes"
          onClick={onOpenSettings}
        >
          <Settings size={18} />
        </button>

      </div>

    </header>
  );
}

export default Header;
