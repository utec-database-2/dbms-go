import { ClockArrowUp, Command, GitBranch, Images, Map as MapIcon, Settings, TextSearch } from "lucide-react";

const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform);

// Barra vertical de la izquierda (estilo pgAdmin / VS Code): abre las
// herramientas que no ocupan lugar fijo en la pantalla.
function ActivityBar({
  windowOpen,
  planOpen,
  historyOpen,
  paletteOpen,
  settingsOpen,
  onWindow,
  onHistory,
  onPalette,
  onPlan,
  onSettings,
}) {
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
      {item("Consulta espacial", <MapIcon size={21} />, windowOpen === "map", () => onWindow("map"))}
      {item("Búsqueda textual", <TextSearch size={21} />, windowOpen === "text", () => onWindow("text"))}
      {item("Búsqueda multimedia", <Images size={21} />, windowOpen === "vector", () => onWindow("vector"))}

      <div className="activity-separator" />

      {item("Historial y rendimiento", <ClockArrowUp size={21} />, historyOpen, onHistory)}
      {item("Comandos", <Command size={21} />, paletteOpen, onPalette, isMac ? "⌘K" : "Ctrl K")}
      {item("Plan de ejecución", <GitBranch size={21} />, planOpen, onPlan)}

      <div className="activity-spacer" />

      {item("Ajustes", <Settings size={21} />, settingsOpen, onSettings)}
    </nav>
  );
}

export default ActivityBar;
