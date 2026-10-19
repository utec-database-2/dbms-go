import { useCallback, useEffect, useState } from "react";

// Preferencias e historial en el navegador (localStorage). Todo va envuelto en
// try/catch: si el navegador no deja guardar, la app sigue con los valores por
// defecto.

function readJSON(key, fallback) {
  try {
    const raw = localStorage.getItem(key);
    return raw ? JSON.parse(raw) : fallback;
  } catch {
    return fallback;
  }
}

function writeJSON(key, value) {
  try {
    localStorage.setItem(key, JSON.stringify(value));
  } catch {
    // sin almacenamiento disponible: no se recuerda, pero no falla
  }
}

// Fracciones de ancho/alto de los paneles (ver Dashboard.jsx).
export const DEFAULT_LAYOUT = {
  top: [3, 4, 5], // Archivos | Consultas | Resultados
  bottom: [5, 7], // Plan | Mapa
  rows: [1, 1], // fila de arriba | fila de abajo
};

export const DEFAULT_SETTINGS = {
  theme: "light",
  rowsPerPage: 5,
  editorFontSize: 13,
  darkMap: true,
  animations: true,
  layout: DEFAULT_LAYOUT,
};

const SETTINGS_KEY = "minigestor.settings.v1";
const HISTORY_KEY = "minigestor.history.v1";
const HISTORY_MAX = 50;

export function useSettings() {
  const [settings, setSettings] = useState(() => ({
    ...DEFAULT_SETTINGS,
    ...readJSON(SETTINGS_KEY, {}),
  }));

  useEffect(() => writeJSON(SETTINGS_KEY, settings), [settings]);

  const update = useCallback(
    (patch) =>
      setSettings((current) => ({
        ...current,
        ...(typeof patch === "function" ? patch(current) : patch),
      })),
    [],
  );

  return [settings, update];
}

// Historial de consultas ejecutadas, de la más antigua a la más reciente.
export function useHistory() {
  const [history, setHistory] = useState(() => readJSON(HISTORY_KEY, []));

  useEffect(() => writeJSON(HISTORY_KEY, history), [history]);

  const add = useCallback(
    (entry) =>
      setHistory((current) =>
        [
          ...current,
          { id: `${Date.now()}-${Math.random().toString(36).slice(2, 7)}`, at: Date.now(), ...entry },
        ].slice(-HISTORY_MAX),
      ),
    [],
  );

  const clear = useCallback(() => setHistory([]), []);

  return [history, add, clear];
}
