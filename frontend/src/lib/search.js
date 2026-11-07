// Utilidades compartidas por las ventanas de búsqueda textual (Parte 3) y
// multimedia (Parte 4).
import { fetchMediaFile, importCsv, runQuery } from "../api";

const lower = (s) => String(s ?? "").toLowerCase();

export const isTextType = (type) => lower(type) === "string";
export const isVectorType = (type) => lower(type).startsWith("vector");
export const vectorDim = (type) => Number(/vector\((\d+)\)/i.exec(type || "")?.[1] || 0);

// Tablas con al menos una columna del tipo pedido.
export function tablesWith(tables, pred) {
  return (tables || []).filter((t) => t.Columns?.some((c) => pred(c.Type)));
}

// Columna de texto "principal": la más probable de contener el documento.
const BODY_NAMES = ["contenido", "texto", "cuerpo", "content", "body", "text", "descripcion", "abstract", "resumen", "letra"];
export function guessTextColumn(table) {
  const cols = (table?.Columns || []).filter((c) => isTextType(c.Type));
  return (cols.find((c) => BODY_NAMES.includes(lower(c.Name))) || cols[cols.length - 1])?.Name ?? "";
}

// Columna para el título de una tarjeta de resultado.
const TITLE_NAMES = ["titulo", "título", "title", "nombre", "name", "cancion", "canción"];
export function titleIndex(columns, exclude = -1) {
  const lc = (columns || []).map(lower);
  const i = lc.findIndex((c, j) => j !== exclude && TITLE_NAMES.includes(c));
  if (i >= 0) return i;
  return lc.findIndex((c) => c === "id");
}

const CATEGORY_NAMES = ["categoria", "categoría", "category", "tipo", "genero", "género", "artista", "autor"];
export function categoryIndex(columns) {
  return (columns || []).map(lower).findIndex((c) => CATEGORY_NAMES.includes(c));
}

const IMAGE_RE = /\.(png|jpe?g|gif|webp|svg|bmp)(\?.*)?$/i;
const AUDIO_RE = /\.(mp3|wav|ogg|flac|m4a|aac)(\?.*)?$/i;

// Busca en las filas una columna que tenga rutas de imagen o audio.
export function mediaColumn(columns, rows) {
  const sample = (rows || []).slice(0, 20);
  for (let i = 0; i < (columns || []).length; i++) {
    const values = sample.map((r) => r?.[i]).filter((v) => typeof v === "string");
    if (!values.length) continue;
    if (values.every((v) => IMAGE_RE.test(v))) return { index: i, kind: "image" };
    if (values.every((v) => AUDIO_RE.test(v))) return { index: i, kind: "audio" };
  }
  return null;
}

// Parte el texto en trozos marcando las palabras que coincidieron con la
// consulta (el servidor las devuelve ya en minúsculas).
export function highlight(text, matched) {
  const s = String(text ?? "");
  if (!matched?.length) return [{ text: s, hit: false }];
  const set = new Set(matched.map(lower));
  const parts = [];
  const re = /[\p{L}\p{N}]+/gu;
  let last = 0;
  for (const m of s.matchAll(re)) {
    if (!set.has(m[0].toLowerCase())) continue;
    if (m.index > last) parts.push({ text: s.slice(last, m.index), hit: false });
    parts.push({ text: m[0], hit: true });
    last = m.index + m[0].length;
  }
  if (last < s.length) parts.push({ text: s.slice(last), hit: false });
  return parts;
}

export const fmtMs = (v) => {
  if (v == null || Number.isNaN(v)) return "—";
  if (v < 1) return `${(v * 1000).toFixed(0)} µs`;
  if (v < 1000) return `${v.toFixed(v < 10 ? 2 : 1)} ms`;
  return `${(v / 1000).toFixed(2)} s`;
};

export const fmtBytes = (b) => {
  if (!b) return "0 B";
  if (b < 1024) return `${b} B`;
  if (b < 1024 * 1024) return `${(b / 1024).toFixed(1)} KB`;
  return `${(b / 1024 / 1024).toFixed(2)} MB`;
};

// El lexer SQL del proyecto no admite comillas escapadas dentro de literales.
const sqlString = (s) => `'${String(s).replace(/'/g, "")}'`;

// SQL equivalente que el motor ya ejecuta (MATCH con TF-IDF sobre la columna).
export function textSearchSQL({ table, column, query, k, columns }) {
  const title = columns?.[titleIndex(columns)] ?? "id";
  const cols = ["id", title].filter((c, i, a) => c && a.indexOf(c) === i && columns?.includes(c));
  const pick = cols.length ? cols.join(", ") : "*";
  return `SELECT ${pick}, MATCH(${column}, ${sqlString(query)}) AS score\nFROM ${table}\nWHERE MATCH(${column}, ${sqlString(query)}) > 0\nORDER BY score DESC\nLIMIT ${k};`;
}

// SQL equivalente del k-NN vectorial (usa el IVF si la columna tiene uno).
export function vectorSearchSQL({ table, column, vector, metric, k, columns }) {
  const title = columns?.[titleIndex(columns)] ?? "id";
  const cols = ["id", title].filter((c, i, a) => c && a.indexOf(c) === i && columns?.includes(c));
  const lit = `VECTOR(${(vector || []).map((x) => Number(x).toFixed(5)).join(", ")})`;
  const m = metric === "dot" ? "dot" : metric;
  return `SELECT ${cols.length ? cols.join(", ") : "*"}, vector_distance(${column}, ${lit}, '${m}') AS distancia\nFROM ${table}\nORDER BY vector_distance(${column}, ${lit}, '${m}')\nLIMIT ${k};`;
}

// Datos de demostración servidos por el servidor desde datos_demo/ (--media).
export const DEMOS = {
  text: {
    table: "documentos",
    csv: "documentos.csv",
    create:
      "CREATE TABLE documentos CLUSTERED BY (id) (id INT PRIMARY KEY, titulo VARCHAR(80), categoria VARCHAR(40), contenido VARCHAR(600));",
  },
  vector: {
    table: "imagenes",
    csv: "imagenes.csv",
    create:
      "CREATE TABLE imagenes CLUSTERED BY (id) (id INT PRIMARY KEY, nombre VARCHAR(60), categoria VARCHAR(30), archivo VARCHAR(120), v VECTOR(64));",
  },
};

// Crea la tabla de la demo (si no existe) e importa su CSV.
export async function loadDemo(kind, tables) {
  const demo = DEMOS[kind];
  const existing = (tables || []).find((t) => lower(t.Name) === demo.table);
  if (existing?.Records > 0) return { table: demo.table, inserted: 0, already: true };
  if (!existing) {
    const res = await runQuery(demo.create);
    if (res.Failed >= 0) throw new Error(res.Error);
  }
  const file = await fetchMediaFile(demo.csv);
  const out = await importCsv(demo.table, file);
  if (!out.inserted && out.errors?.length) throw new Error(out.errors[0]);
  return { table: demo.table, ...out };
}
