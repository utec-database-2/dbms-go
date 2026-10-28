import { Maximize2, Minimize2 } from "lucide-react";

// Botón ⤢ de la cabecera de cada panel (ver useExpandable).
export function ExpandButton({ expanded, onToggle }) {
  return (
    <button
      className="expand-toggle"
      onClick={() => onToggle(!expanded)}
      title={expanded ? "Cerrar ventana (Esc)" : "Abrir en ventana grande"}
    >
      {expanded ? <Minimize2 size={16} /> : <Maximize2 size={16} />}
    </button>
  );
}

// Fondo oscuro detrás del panel ampliado, y un hueco que ocupa su lugar en la
// grilla para que los demás paneles no se muevan mientras está abierto.
export function ExpandBackdrop({ expanded, onClose }) {
  if (!expanded) return null;
  return (
    <>
      <div className="expand-slot" />
      <div className="expand-backdrop" onClick={onClose} />
    </>
  );
}
