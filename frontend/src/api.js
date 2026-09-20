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
  const res = await fetch(`${BASE_URL}/api/tables/${encodeURIComponent(tableName)}/import`, {
    method: "POST",
    body: form,
  });
  return parseJSON(res);
}
