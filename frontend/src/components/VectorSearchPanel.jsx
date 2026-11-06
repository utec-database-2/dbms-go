import { useEffect, useMemo, useRef, useState } from "react";
import {
  CheckCircle2,
  Code2,
  Database,
  GitBranch,
  ImageUp,
  Images,
  LayoutGrid,
  Music,
  Search,
  Sparkles,
  X,
  XCircle,
} from "lucide-react";
import { extractVector, listVectorItems, mediaUrl, searchVector } from "../api";
import {
  categoryIndex,
  fmtMs,
  isVectorType,
  loadDemo,
  mediaColumn,
  tablesWith,
  titleIndex,
  vectorDim,
  vectorSearchSQL,
} from "../lib/search";

const MODES = [
  { id: "hnsw", label: "HNSW", methods: ["hnsw"] },
  { id: "ivf", label: "IVF", methods: ["ivf"] },
  { id: "seq", label: "Secuencial", methods: ["seq"] },
  { id: "compare", label: "Comparar", methods: ["hnsw", "ivf", "seq"] },
];

const METRICS = [
  { id: "cosine", label: "Coseno" },
  { id: "euclidean", label: "Euclidiana" },
  { id: "dot", label: "Producto punto" },
];

// Mini gráfico de barras del vector (p. ej. el histograma de una imagen).
function VectorBars({ vector, height = 28 }) {
  if (!vector?.length) return null;
  const max = Math.max(...vector.map((v) => Math.abs(v)), 1e-9);
  const w = 100 / vector.length;
  return (
    <svg className="vector-bars" viewBox={`0 0 100 ${height}`} preserveAspectRatio="none" aria-hidden="true">
      {vector.map((v, i) => {
        const h = (Math.abs(v) / max) * (height - 1);
        return <rect key={i} x={i * w} y={height - h} width={Math.max(w - 0.3, 0.2)} height={h} />;
      })}
    </svg>
  );
}

// Miniatura del elemento: imagen, reproductor de audio o un marcador.
function Thumb({ values, media, label }) {
  const src = media ? values?.[media.index] : null;
  if (media?.kind === "image" && src) {
    return <img className="thumb-img" src={mediaUrl(src)} alt={String(label)} loading="lazy" draggable="false" />;
  }
  if (media?.kind === "audio" && src) {
    return (
      <div className="thumb-audio" onClick={(e) => e.stopPropagation()}>
        <Music size={26} />
        <audio controls preload="none" src={mediaUrl(src)} />
      </div>
    );
  }
  return (
    <div className="thumb-placeholder">
      <Sparkles size={22} />
      <span>{String(label)}</span>
    </div>
  );
}

function VectorSearchPanel({ tables, onClose, onOpenSQL, onTablesChanged, active }) {
  const vecTables = useMemo(() => tablesWith(tables, isVectorType), [tables]);
  const [tableName, setTableName] = useState("");
  const [columnName, setColumnName] = useState("");
  const [mode, setMode] = useState("compare");
  const [metric, setMetric] = useState("cosine");
  const [k, setK] = useState(8);
  const [nlist, setNList] = useState("");
  const [nprobe, setNProbe] = useState("");
  const [efSearch, setEfSearch] = useState(64);
  const [tab, setTab] = useState("collection");
  const [items, setItems] = useState(null);
  const [itemsKey, setItemsKey] = useState("");
  const [query, setQuery] = useState(null); // { type: "row", row, values } | { type: "upload", name, preview, vector }
  const [data, setData] = useState(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState(null);
  const [demoBusy, setDemoBusy] = useState(false);
  const fileRef = useRef(null);

  const table =
    vecTables.find((t) => t.Name === tableName) ||
    vecTables.find((t) => t.Name.toLowerCase() === "imagenes") ||
    vecTables[0] ||
    null;
  const vecColumns = (table?.Columns || []).filter((c) => isVectorType(c.Type));
  const column = vecColumns.some((c) => c.Name === columnName) ? columnName : (vecColumns[0]?.Name ?? "");
  const dim = vectorDim(vecColumns.find((c) => c.Name === column)?.Type);
  const cube = Math.round(Math.cbrt(dim));
  const canUpload = dim > 0 && cube ** 3 === dim;
  const sourceKey = table ? `${table.Name}|${column}|${table.Records}` : "";

  // Carga la colección (filas con vector) cuando cambia la tabla o su tamaño.
  useEffect(() => {
    if (!active || !sourceKey || sourceKey === itemsKey) return;
    let alive = true;
    listVectorItems(table.Name, column)
      .then((res) => {
        if (!alive) return;
        setItems(res);
        setItemsKey(sourceKey);
      })
      .catch((err) => {
        if (!alive) return;
        setItems({ items: [], columns: [], error: err.message });
        setItemsKey(sourceKey);
      });
    return () => {
      alive = false;
    };
  }, [active, sourceKey, itemsKey, table, column]);

  const columns = data?.columns || items?.columns || [];
  const media = mediaColumn(columns, (items?.items || []).map((it) => it.values));
  const tIdx = titleIndex(columns, media?.index ?? -1);
  const cIdx = categoryIndex(columns);
  const labelOf = (values, row) => (tIdx >= 0 ? values?.[tIdx] : `Fila ${row + 1}`);

  const methods = MODES.find((m) => m.id === mode).methods;

  const run = async (q = query) => {
    if (!table || !column || !q) return;
    setLoading(true);
    setError(null);
    try {
      const res = await searchVector({
        table: table.Name,
        column,
        ...(q.type === "row" ? { row: q.row } : { vector: q.vector }),
        methods,
        metric,
        k: Number(k) || 8,
        nlist: Number(nlist) || 0,
        nprobe: Number(nprobe) || 0,
        efSearch: Number(efSearch) || 0,
      });
      setData(res);
      setTab("results");
    } catch (err) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
  };

  const pickRow = (item) => {
    const q = { type: "row", row: item.row, values: item.values };
    setQuery(q);
    run(q);
  };

  const handleUpload = async (file) => {
    if (!file) return;
    setError(null);
    try {
      const res = await extractVector(file, dim);
      const q = { type: "upload", name: file.name, preview: URL.createObjectURL(file), vector: res.vector, extractor: res.extractor };
      setQuery((old) => {
        if (old?.preview) URL.revokeObjectURL(old.preview);
        return q;
      });
      run(q);
    } catch (err) {
      setError(err.message);
    }
  };

  const handleDemo = async () => {
    setDemoBusy(true);
    setError(null);
    try {
      await loadDemo("vector", tables);
      await onTablesChanged?.();
      setTableName("imagenes");
      setColumnName("v");
    } catch (err) {
      setError(err.message);
    } finally {
      setDemoBusy(false);
    }
  };

  const results = data?.results || [];
  const compare = results.length > 1;
  const queryValues = query?.type === "row" ? query.values : null;
  const queryCategory = cIdx >= 0 && queryValues ? queryValues[cIdx] : null;
  const verdict = (hit) => {
    if (queryCategory == null) return null;
    return hit.values?.[cIdx] === queryCategory ? "ok" : "miss";
  };

  const renderHit = (hit, rank, metricName) => {
    const v = verdict(hit);
    const isQuery = query?.type === "row" && hit.row === query.row;
    return (
      <figure key={hit.row} className={["vector-hit", v, isQuery && "is-query"].filter(Boolean).join(" ")}>
        <div className="vector-hit-media">
          <Thumb values={hit.values} media={media} label={labelOf(hit.values, hit.row)} />
          <span className="vector-rank">#{rank}</span>
          {isQuery && <span className="vector-badge">consulta</span>}
          {v === "ok" && !isQuery && <CheckCircle2 className="vector-verdict ok" size={18} />}
          {v === "miss" && <XCircle className="vector-verdict miss" size={18} />}
        </div>
        <figcaption>
          <strong>{String(labelOf(hit.values, hit.row))}</strong>
          {cIdx >= 0 && <span className="chip">{String(hit.values?.[cIdx])}</span>}
          <div className="vector-score">
            <div className="score-bar">
              <i style={{ width: `${Math.max(3, Math.min(100, hit.score * 100))}%` }} />
            </div>
            <span title={`distancia ${metricName} = ${hit.distance}`}>
              {metricName === "dot_product" ? hit.score.toFixed(3) : `${(hit.score * 100).toFixed(1)}%`}
            </span>
          </div>
          <VectorBars vector={hit.vector} height={18} />
        </figcaption>
      </figure>
    );
  };

  const hitCount = results.reduce((m, r) => Math.max(m, r.hits?.length || 0), 0);

  return (
    <section className="search-panel vector-search-panel">
      <div className="search-panel-header">
        <div className="search-panel-title">
          <Images size={19} />
          <div>
            <h2>Búsqueda multimedia</h2>
            <p>Similitud entre vectores de características · HNSW, IVF y secuencial</p>
          </div>
        </div>
        <div className="panel-header-actions">
          <div className="map-result-count">{data ? `${hitCount} vecino(s)` : `${items?.count ?? 0} elemento(s)`}</div>
          <button className="expand-toggle" onClick={onClose} title="Cerrar (Esc)">
            <X size={16} />
          </button>
        </div>
      </div>

      {vecTables.length === 0 ? (
        <div className="search-empty">
          <Images size={34} />
          <h3>No hay tablas con columnas VECTOR</h3>
          <p>
            Crea una tabla con una columna <code>VECTOR(n)</code> o carga la galería de ejemplo: 48 imágenes de 8
            categorías con su histograma de color (64 dimensiones) ya calculado.
          </p>
          <button className="btn-primary" onClick={handleDemo} disabled={demoBusy}>
            <Database size={15} />
            {demoBusy ? "Cargando…" : "Cargar galería de ejemplo"}
          </button>
          {error && <div className="search-error">{error}</div>}
        </div>
      ) : (
        <>
          <form
            className="search-controls"
            onSubmit={(e) => {
              e.preventDefault();
              run();
            }}
          >
            <div className="search-field">
              <label>Tabla</label>
              <select
                value={table?.Name ?? ""}
                onChange={(e) => {
                  setTableName(e.target.value);
                  setColumnName("");
                  setQuery(null);
                  setData(null);
                  setTab("collection");
                }}
              >
                {vecTables.map((t) => (
                  <option key={t.Name} value={t.Name}>
                    {t.Name} ({t.Records})
                  </option>
                ))}
              </select>
            </div>
            <div className="search-field">
              <label>Vector</label>
              <select value={column} onChange={(e) => setColumnName(e.target.value)}>
                {vecColumns.map((c) => (
                  <option key={c.Name} value={c.Name}>
                    {c.Name} · {vectorDim(c.Type)} dim
                  </option>
                ))}
              </select>
            </div>
            <div className="search-field fixed">
              <label>Índice</label>
              <div className="segmented">
                {MODES.map((m) => (
                  <button type="button" key={m.id} className={mode === m.id ? "active" : ""} onClick={() => setMode(m.id)}>
                    {m.label}
                  </button>
                ))}
              </div>
            </div>
            <div className="search-field">
              <label>Métrica</label>
              <select value={metric} onChange={(e) => setMetric(e.target.value)}>
                {METRICS.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.label}
                  </option>
                ))}
              </select>
            </div>
            <div className="search-field narrow">
              <label>k</label>
              <input type="number" min="1" max="200" value={k} onChange={(e) => setK(e.target.value)} />
            </div>
            {methods.includes("ivf") && (
              <>
                <div className="search-field narrow" title="Número de centroides (listas). Vacío: √n">
                  <label>nlist</label>
                  <input type="number" min="1" placeholder="auto" value={nlist} onChange={(e) => setNList(e.target.value)} />
                </div>
                <div className="search-field narrow" title="Listas que se revisan en cada consulta. Vacío: nlist/4">
                  <label>nprobe</label>
                  <input type="number" min="1" placeholder="auto" value={nprobe} onChange={(e) => setNProbe(e.target.value)} />
                </div>
              </>
            )}
            {methods.includes("hnsw") && (
              <div className="search-field narrow" title="Candidatos explorados en la búsqueda del grafo">
                <label>efSearch</label>
                <input type="number" min="1" value={efSearch} onChange={(e) => setEfSearch(e.target.value)} />
              </div>
            )}
            <button className="spatial-search-button" type="submit" disabled={loading || !query}>
              <Search size={16} />
              {loading ? "Buscando…" : "Buscar"}
            </button>
          </form>

          {error && <div className="search-error bar">{error}</div>}

          <div className="vector-layout">
            <aside className="vector-query">
              <h4>Consulta</h4>
              {query ? (
                <div className="vector-query-card">
                  <div className="vector-hit-media">
                    {query.type === "upload" ? (
                      <img className="thumb-img" src={query.preview} alt={query.name} />
                    ) : (
                      <Thumb values={query.values} media={media} label={labelOf(query.values, query.row)} />
                    )}
                  </div>
                  <strong>{query.type === "upload" ? query.name : String(labelOf(query.values, query.row))}</strong>
                  {query.type === "row" && cIdx >= 0 && <span className="chip">{String(query.values[cIdx])}</span>}
                  {query.type === "upload" && <small>{query.extractor}</small>}
                  <VectorBars vector={data?.query || query.vector} />
                </div>
              ) : (
                <p className="muted">Elige un elemento de la colección o sube una imagen.</p>
              )}

              <button
                type="button"
                className="upload-drop"
                disabled={!canUpload}
                onClick={() => fileRef.current?.click()}
                onDragOver={(e) => e.preventDefault()}
                onDrop={(e) => {
                  e.preventDefault();
                  if (canUpload) handleUpload(e.dataTransfer.files?.[0]);
                }}
                title={
                  canUpload
                    ? "Se extrae su histograma de color en el servidor"
                    : `La columna tiene ${dim} dimensiones; el extractor de color produce b³ (8, 27, 64…). Para SIFT/MFCC hace falta su extractor.`
                }
              >
                <ImageUp size={20} />
                <span>{canUpload ? "Subir imagen (PNG, JPG)" : "Subir: requiere extractor"}</span>
              </button>
              <input
                ref={fileRef}
                type="file"
                accept="image/png,image/jpeg,image/gif"
                hidden
                onChange={(e) => {
                  handleUpload(e.target.files?.[0]);
                  e.target.value = "";
                }}
              />

              <button
                type="button"
                className="spatial-secondary-button"
                disabled={!data}
                onClick={() =>
                  onOpenSQL?.(
                    vectorSearchSQL({ table: table.Name, column, vector: data.query, metric, k: Number(k) || 8, columns: table.Columns.map((c) => c.Name) }),
                  )
                }
                title="Escribe en el editor el k-NN equivalente con vector_distance (usa el IVF si la columna tiene uno)"
              >
                <Code2 size={15} />
                Ver como SQL
              </button>
            </aside>

            <div className="vector-main">
              <div className="search-tabs">
                <div className="segmented">
                  <button className={tab === "results" ? "active" : ""} onClick={() => setTab("results")} disabled={!data}>
                    <Sparkles size={14} />
                    Resultados
                  </button>
                  <button className={tab === "collection" ? "active" : ""} onClick={() => setTab("collection")}>
                    <LayoutGrid size={14} />
                    Colección
                  </button>
                  <button className={tab === "plan" ? "active" : ""} onClick={() => setTab("plan")} disabled={!data}>
                    <GitBranch size={14} />
                    Plan de ejecución
                  </button>
                </div>
                {tab === "results" && queryCategory != null && (
                  <span className="search-tabs-note">
                    <CheckCircle2 size={13} className="ok" /> misma categoría que la consulta ·{" "}
                    <XCircle size={13} className="miss" /> distinta
                  </span>
                )}
              </div>

              <div className="search-body">
                {tab === "collection" && (
                  <>
                    {items?.error && <div className="search-error">{items.error}</div>}
                    <div className="vector-grid">
                      {(items?.items || []).map((it) => (
                        <button
                          type="button"
                          key={it.row}
                          className={query?.type === "row" && query.row === it.row ? "vector-item selected" : "vector-item"}
                          onClick={() => pickRow(it)}
                          title="Buscar elementos similares a este"
                        >
                          <Thumb values={it.values} media={media} label={labelOf(it.values, it.row)} />
                          <span>{String(labelOf(it.values, it.row))}</span>
                        </button>
                      ))}
                    </div>
                  </>
                )}

                {tab === "results" && data && (
                  <div className="vector-results">
                    {results.map((r) => (
                      <section className="vector-method" key={r.method}>
                        <header>
                          <strong>{r.label}</strong>
                          {!r.error && (
                            <>
                              <span className="pill">consulta {fmtMs(r.elapsedMs)}</span>
                              {r.method !== "seq" && <span className="pill">construcción {fmtMs(r.buildMs)}</span>}
                              <span className={r.recall >= 0.999 ? "pill good" : r.recall >= 0.7 ? "pill warn" : "pill bad"}>
                                Recall@{data.k} {(r.recall * 100).toFixed(0)}%
                              </span>
                            </>
                          )}
                        </header>
                        {r.error ? (
                          <div className="search-error">{r.error}</div>
                        ) : (
                          <div className={compare ? "vector-strip" : "vector-grid results"}>
                            {(r.hits || []).map((h, i) => renderHit(h, i + 1, data.metric))}
                          </div>
                        )}
                      </section>
                    ))}
                  </div>
                )}

                {tab === "plan" && data && (
                  <div className="search-plan">
                    {results.map((r) => (
                      <div className="plan-card" key={r.method}>
                        <h4>
                          {r.label} <span>{r.error ? "—" : fmtMs(r.elapsedMs)}</span>
                        </h4>
                        {r.error ? (
                          <div className="search-error">{r.error}</div>
                        ) : (
                          <ol>
                            {r.steps.map((s, i) => (
                              <li key={i}>{s}</li>
                            ))}
                          </ol>
                        )}
                      </div>
                    ))}
                    <div className="plan-card wide">
                      <h4>
                        Comparación · {data.count} vectores de {data.dim} dimensiones · {METRICS.find((m) => m.id === metric)?.label}
                      </h4>
                      <table className="plan-table">
                        <thead>
                          <tr>
                            <th>Técnica</th>
                            <th>Construcción</th>
                            <th>Consulta</th>
                            <th>Recall@{data.k}</th>
                          </tr>
                        </thead>
                        <tbody>
                          {results.map((r) => (
                            <tr key={r.method}>
                              <td>{r.label}</td>
                              <td>{r.error ? "—" : r.method === "seq" ? "no requiere" : fmtMs(r.buildMs)}</td>
                              <td>{r.error ? "—" : fmtMs(r.elapsedMs)}</td>
                              <td>{r.error ? "—" : `${(r.recall * 100).toFixed(0)}%`}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  </div>
                )}
              </div>
            </div>
          </div>
        </>
      )}
    </section>
  );
}

export default VectorSearchPanel;
