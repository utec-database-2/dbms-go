import { useEffect } from "react";
import { X } from "lucide-react";

// Diálogo genérico: fondo difuminado, se cierra con Esc, con clic afuera o con
// la X. "variant" = "dialog" (centrado) | "drawer" (lateral) | "palette".
function Modal({ title, icon, onClose, children, footer, variant = "dialog", className = "" }) {
  useEffect(() => {
    const onKey = (event) => {
      if (event.key === "Escape") {
        event.stopPropagation();
        onClose();
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div
      className={`modal-layer modal-layer-${variant}`}
      onMouseDown={(event) => event.target === event.currentTarget && onClose()}
    >
      <div
        className={`modal modal-${variant} ${className}`}
        role="dialog"
        aria-modal="true"
        aria-label={title}
      >
        {title && (
          <div className="modal-header">
            {icon}
            <h2>{title}</h2>
            <button className="modal-close" onClick={onClose} title="Cerrar (Esc)">
              <X size={17} />
            </button>
          </div>
        )}
        <div className="modal-body">{children}</div>
        {footer && <div className="modal-footer">{footer}</div>}
      </div>
    </div>
  );
}

export default Modal;
