import { useEffect, useState } from "react";

// Estado "ampliado" de un panel: el mismo panel pasa a verse como ventana
// emergente grande (no es una copia, así conserva su contenido y estado).
// Se cierra con Esc, con clic en el fondo o con el mismo botón.
export function useExpandable() {
  const [expanded, setExpanded] = useState(false);

  useEffect(() => {
    if (!expanded) return;

    const onKey = (event) => {
      // Si hay un diálogo abierto encima, Esc cierra solo el diálogo.
      if (event.key === "Escape" && !document.querySelector(".modal-layer")) {
        setExpanded(false);
      }
    };
    document.addEventListener("keydown", onKey);
    document.body.style.overflow = "hidden";

    return () => {
      document.removeEventListener("keydown", onKey);
      document.body.style.overflow = "";
    };
  }, [expanded]);

  return [expanded, setExpanded];
}
