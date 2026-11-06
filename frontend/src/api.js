// Cliente HTTP para el servidor Go (dbms/cmd/server). Habla contra el
// executor SQL real: nada de datos simulados a partir de aquí.
const BASE_URL = import.meta.env.VITE_API_URL || "http://localhost:8080";

async function parseJSON(response) {
  const text = await response.text();
  let data = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    // el server siempre responde JSON; si esto falla, probablemente no está levantado
  }
  if (!response.ok) {
    const message = data?.error || `HTTP ${response.status}`;
    throw new Error(message);
  }
  return data;
}

export async function listTables() {
  const res = await fetch(`${BASE_URL}/api/tables`);
  return parseJSON(res);
}

export async function runQuery(sql) {
  const res = await fetch(`${BASE_URL}/api/query`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ sql }),
  });
  return parseJSON(res);
}

export async function importCsv(tableName, file) {
  const form = new FormData();
  form.append("file", file);
  const res = await fetch(
    `${BASE_URL}/api/tables/${encodeURIComponent(tableName)}/import`,
    {
      method: "POST",
      body: form,
    },
  );
  return parseJSON(res);
}

export async function searchSpatialRange(data) {
  const res = await fetch(`${BASE_URL}/api/spatial/range`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify(data),
  });

  return parseJSON(res);
}

// k vecinos más cercanos: { latitude, longitude, k, metric }
export async function searchSpatialKNN(data) {
  const res = await fetch(`${BASE_URL}/api/spatial/knn`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify(data),
  });

  return parseJSON(res);
}

// Registros dentro de un polígono: { vertices: [{ lat, lon }, ...] }
export async function searchSpatialPolygon(data) {
  const res = await fetch(`${BASE_URL}/api/spatial/polygon`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify(data),
  });

  return parseJSON(res);
}

// Estructura del R-Tree (MBR de cada nodo) para dibujarla en el mapa.
export async function getRTree() {
  const res = await fetch(`${BASE_URL}/api/spatial/rtree`);
  return parseJSON(res);
}

async function postJSON(path, data) {
  const res = await fetch(`${BASE_URL}${path}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(data),
  });
  return parseJSON(res);
}

// Parte 3 — búsqueda textual sobre el índice invertido SPIMI.
// { table, column, query, methods: ["tfidf","bm25"], k, k1?, b? }
export function searchText(data) {
  return postJSON("/api/text/search", data);
}

// Parte 4 — búsqueda por similitud.
// { table, column, row? | vector?, methods: ["hnsw","ivf","seq"], metric, k, nlist?, nprobe?, efSearch? }
export function searchVector(data) {
  return postJSON("/api/vector/search", data);
}

// Filas que tienen vector (sin el vector), para la galería de la colección.
export function listVectorItems(table, column) {
  return postJSON("/api/vector/items", { table, column });
}

// Extrae el vector de una imagen subida (histograma de color en el servidor).
export async function extractVector(file, dim) {
  const form = new FormData();
  form.append("file", file);
  if (dim) form.append("dim", String(dim));
  const res = await fetch(`${BASE_URL}/api/vector/extract`, { method: "POST", body: form });
  return parseJSON(res);
}

// Archivos servidos por el servidor desde su directorio --media.
export function mediaUrl(path) {
  if (!path) return "";
  if (/^(https?:|data:|blob:)/i.test(path)) return path;
  return `${BASE_URL}/api/media/${String(path).replace(/^\.?\/+/, "").split("/").map(encodeURIComponent).join("/")}`;
}

// Descarga un archivo del directorio --media (p. ej. los CSV de la demo).
export async function fetchMediaFile(path, name) {
  const res = await fetch(mediaUrl(path));
  if (!res.ok) throw new Error(`No se pudo descargar ${path} (HTTP ${res.status})`);
  const blob = await res.blob();
  return new File([blob], name || path.split("/").pop(), { type: blob.type || "text/csv" });
}

export const API_URL = BASE_URL;
