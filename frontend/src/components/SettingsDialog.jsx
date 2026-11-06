import { LayoutDashboard, Moon, Settings2, Sun, Trash2 } from "lucide-react";
import Modal from "./Modal";
import { API_URL } from "../api";

function Segmented({ value, options, onChange }) {
  return (
    <div className="segmented">
      {options.map((opt) => (
        <button
          key={String(opt.value)}
          className={opt.value === value ? "active" : ""}
          onClick={() => onChange(opt.value)}
        >
          {opt.icon}
          {opt.label}
        </button>
      ))}
    </div>
  );
}

function Switch({ checked, onChange }) {
  return (
    <button
      role="switch"
      aria-checked={checked}
      className={checked ? "switch on" : "switch"}
      onClick={() => onChange(!checked)}
    >
      <span />
    </button>
  );
}

// Ajustes de la interfaz. Se guardan en el navegador (ver lib/storage.js).
function SettingsDialog({ settings, onChange, onResetLayout, onClearHistory, historyCount, onClose }) {
  return (
    <Modal
      title="Ajustes"
      icon={<Settings2 size={18} />}
      onClose={onClose}
      className="settings-dialog"
      footer={
        <button className="btn-primary" onClick={onClose}>
          Listo
        </button>
      }
    >
      <div className="settings-group">
        <h3>Apariencia</h3>

        <div className="setting-row">
          <div>
            <strong>Tema</strong>
            <span>La interfaz arranca en claro; aquí la puedes dejar oscura.</span>
          </div>
          <Segmented
            value={settings.theme}
            onChange={(theme) => onChange({ theme })}
            options={[
              { value: "light", label: "Claro", icon: <Sun size={14} /> },
              { value: "dark", label: "Oscuro", icon: <Moon size={14} /> },
            ]}
          />
        </div>

        <div className="setting-row">
          <div>
            <strong>Mapa oscuro</strong>
            <span>En tema oscuro, oscurece también el mapa base.</span>
          </div>
          <Switch checked={settings.darkMap} onChange={(darkMap) => onChange({ darkMap })} />
        </div>

        <div className="setting-row">
          <div>
            <strong>Animaciones</strong>
            <span>Transiciones al abrir paneles, filas y ventanas.</span>
          </div>
          <Switch
            checked={settings.animations}
            onChange={(animations) => onChange({ animations })}
          />
        </div>
      </div>

      <div className="settings-group">
        <h3>Consultas y resultados</h3>

        <div className="setting-row">
          <div>
            <strong>Filas por página</strong>
            <span>En el Panel de Resultados (en la ventana grande se ven al menos 20).</span>
          </div>
          <Segmented
            value={settings.rowsPerPage}
            onChange={(rowsPerPage) => onChange({ rowsPerPage })}
            options={[5, 10, 20, 50].map((n) => ({ value: n, label: String(n) }))}
          />
        </div>

        <div className="setting-row">
          <div>
            <strong>Letra del editor</strong>
            <span>Tamaño del texto en el editor SQL.</span>
          </div>
          <Segmented
            value={settings.editorFontSize}
            onChange={(editorFontSize) => onChange({ editorFontSize })}
            options={[12, 13, 14, 16].map((n) => ({ value: n, label: `${n}px` }))}
          />
        </div>
      </div>

      <div className="settings-group">
        <h3>Datos de la interfaz</h3>

        <div className="setting-row">
          <div>
            <strong>Distribución de paneles</strong>
            <span>Vuelve a los tamaños originales (también: doble clic en un divisor).</span>
          </div>
          <button className="btn-secondary" onClick={onResetLayout}>
            <LayoutDashboard size={15} />
            Restablecer
          </button>
        </div>

        <div className="setting-row">
          <div>
            <strong>Historial de consultas</strong>
            <span>{historyCount} consulta(s) guardada(s) en este navegador.</span>
          </div>
          <button
            className="btn-secondary danger"
            onClick={onClearHistory}
            disabled={historyCount === 0}
          >
            <Trash2 size={15} />
            Borrar
          </button>
        </div>

        <div className="setting-row">
          <div>
            <strong>Servidor</strong>
            <span>Dirección del backend (se cambia con VITE_API_URL).</span>
          </div>
          <code className="setting-code">{API_URL}</code>
        </div>
      </div>
    </Modal>
  );
}

export default SettingsDialog;
