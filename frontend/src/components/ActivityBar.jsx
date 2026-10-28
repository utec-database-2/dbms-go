import { ClockArrowUp, Command, GitBranch, Map as MapIcon, Settings } from "lucide-react";

const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform);

// Barra vertical de la izquierda (estilo pgAdmin / VS Code): abre las
// herramientas que no ocupan lugar fijo en la pantalla.
function ActivityBar({ mapOpen, planOpen, historyOpen, paletteOpen, settingsOpen, onMap, onHistory, onPalette, onPlan, onSettings }) {
  const item = (label, icon, active, onClick, hint) => (
    <button
      className={active ? "activity-item active" : "activity-item"}
      onClick={onClick}
      aria-label={label}
      aria-pressed={active}
      data-label={hint ? `${label} · ${hint}` : label}
    >
      {icon}
    </button>
  );

  return (
    <nav className="activity-bar" aria-label="Herramientas">
      {item("Consulta espacial", <MapIcon size={21} />, mapOpen, onMap)}
      {item("Historial y rendimiento", <ClockArrowUp size={21} />, historyOpen, onHistory)}
      {item("Comandos", <Command size={21} />, paletteOpen, onPalette, isMac ? "⌘K" : "Ctrl K")}
      {item("Plan de ejecución", <GitBranch size={21} />, planOpen, onPlan)}

      <div className="activity-spacer" />

      {item("Ajustes", <Settings size={21} />, settingsOpen, onSettings)}
    </nav>
  );
}

export default ActivityBar;
