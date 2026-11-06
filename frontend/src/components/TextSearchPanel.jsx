import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  ArrowDown,
  ArrowUp,
  Code2,
  Database,
  GitBranch,
  ListOrdered,
  Minus,
  Search,
  Sparkles,
  TextSearch,
  X,
} from "lucide-react";
import { searchText } from "../api";
import {
  categoryIndex,
  fmtBytes,
  fmtMs,
  guessTextColumn,
  highlight,
  isTextType,
  loadDemo,
  tablesWith,
  textSearchSQL,
  titleIndex,
} from "../lib/search";

const MODES = [
  { id: "bm25", label: "BM25", methods: ["bm25"] },
  { id: "tfidf", label: "TF-IDF", methods: ["tfidf"] },
  { id: "compare", label: "Comparar", methods: ["tfidf", "bm25"] },
];

const SUGGESTIONS = ["búsqueda de imágenes similares", "índice para consultas por rango", "plato con pescado y limón", "historia de los incas"];

function Highlighted({ text, matched }) {
  return highlight(text, matched).map((part, i) =>
    part.hit ? <mark key={i}>{part.text}</mark> : <span key={i}>{part.text}</span>,
  );
}

function RankDelta({ rank, other }) {
  if (other == null) return <span className="rank-delta new" title="No aparece en el top-k de la otra técnica">nuevo</span>;
  const d = other - rank;
  if (d === 0) return <span className="rank-delta same" title="Misma posición en ambas técnicas"><Minus size={11} /></span>;
  return d > 0 ? (
    <span className="rank-delta up" title={`Sube ${d} puesto(s) respecto de la otra técnica`}><ArrowUp size={11} />{d}</span>
  ) : (
    <span className="rank-delta down" title={`Baja ${-d} puesto(s) respecto de la otra técnica`}><ArrowDown size={11} />{-d}</span>
  );
}

function HitCard({ hit, rank, max, columns, bodyCol, compareRank, showDelta }) {
  const [open, setOpen] = useState(false);
  const tIdx = titleIndex(columns, bodyCol);
  const cIdx = categoryIndex(columns);
  const title = tIdx >= 0 ? hit.row[tIdx] : `Documento ${hit.doc}`;
  const pct = max > 0 ? Math.max(4, (hit.score / max) * 100) : 0;
  return (
    <article className={open ? "text-hit open" : "text-hit"} onClick={() => setOpen((v) => !v)}>
      <div className="text-hit-rank">{rank}</div>
      <div className="text-hit-main">
        <div className="text-hit-head">
          <h3>{String(title)}</h3>
          {cIdx >= 0 && cIdx !== bodyCol && <span className="chip">{String(hit.row[cIdx])}</span>}
          {showDelta && <RankDelta rank={rank} other={compareRank} />}
        </div>
        <p className="text-hit-body">
          <Highlighted text={hit.row[bodyCol]} matched={hit.matched} />
        </p>
        <div className="text-hit-foot">
          <div className="score-bar" title={`score = ${hit.score}`}>
            <i style={{ width: `${pct}%` }} />
          </div>
          <span className="score-value">{hit.score.toFixed(4)}</span>
          <span className="text-hit-terms">
            {hit.matched?.length ? hit.matched.map((w) => <code key={w}>{w}</code>) : <em>sin coincidencias exactas</em>}
          </span>
        </div>
      </div>
    </article>
  );
}

function ResultList({ result, columns, bodyCol, other, showDelta }) {
  const hits = result?.hits || [];
  const max = hits.reduce((m, h) => Math.max(m, h.score), 0);
  const otherRank = useMemo(() => {
    const map = new Map();
    (other?.hits || []).forEach((h, i) => map.set(h.doc, i + 1));
    return map;
  }, [other]);
  if (result?.error) return <div className="search-error">{result.error}</div>;
  if (!hits.length) return <div className="search-empty small">Ningún documento contiene los términos de la consulta.</div>;
  return (
    <div className="text-hits">
      {hits.map((h, i) => (
        <HitCard
          key={h.doc}
          hit={h}
          rank={i + 1}
          max={max}
          columns={columns}
          bodyCol={bodyCol}
          compareRank={otherRank.get(h.doc)}
          showDelta={showDelta}
        />
      ))}
    </div>
  );
}

function TextSearchPanel({ tables, onClose, onOpenSQL, onTablesChanged, active }) {
  const textTables = useMemo(() => tablesWith(tables, isTextType), [tables]);
  const [tableName, setTableName] = useState("");
  const [columnName, setColumn] = useState("");
  const [query, setQuery] = useState("búsqueda de imágenes similares");
  const [mode, setMode] = useState("compare");
  const [k, setK] = useState(10);
  const [k1, setK1] = useState(1.2);
  const [b, setB] = useState(0.75);
  const [tab, setTab] = useState("results");
  const [data, setData] = useState(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState(null);
  const [demoBusy, setDemoBusy] = useState(false);
  const inputRef = useRef(null);

  // Tabla elegida o, si no, una con texto (la de los documentos si existe).
  const table =
    textTables.find((t) => t.Name === tableName) ||
    textTables.find((t) => t.Name.toLowerCase() === "documentos") ||
    textTables[0] ||
    null;
  const column = table?.Columns.some((c) => c.Name === columnName && isTextType(c.Type))
    ? columnName
    : guessTextColumn(table);

  useEffect(() => {
    if (active) setTimeout(() => inputRef.current?.focus(), 150);
  }, [active]);

  const methods = MODES.find((m) => m.id === mode).methods;
  const usesBM25 = methods.includes("bm25");

  const run = useCallback(
    async (q = query) => {
      if (!table || !column || !q.trim()) return;
      setLoading(true);
      setError(null);
      try {
        const res = await searchText({
          table: table.Name,
          column,
          query: q,
          methods,
          k: Number(k) || 10,
          k1: Number(k1),
          b: Number(b),
        });
        setData(res);
      } catch (err) {
        setError(err.message);
      } finally {
        setLoading(false);
      }
    },
    [table, column, query, methods, k, k1, b],
  );

  const handleDemo = async () => {
    setDemoBusy(true);
    setError(null);
    try {
      await loadDemo("text", tables);
      await onTablesChanged?.();
      setTableName("documentos");
      setColumn("contenido");
    } catch (err) {
      setError(err.message);
    } finally {
      setDemoBusy(false);
    }
  };

  const columns = data?.columns || [];
  const bodyCol = columns.findIndex((c) => c.toLowerCase() === (data?.column || "").toLowerCase());
  const results = data?.results || [];
  const byMethod = Object.fromEntries(results.map((r) => [r.method, r]));
  const compare = results.length > 1;
  const overlap = useMemo(() => {
    if (!compare) return null;
    const a = new Set((byMethod.tfidf?.hits || []).map((h) => h.doc));
    const both = (byMethod.bm25?.hits || []).filter((h) => a.has(h.doc)).length;
    return { both, k: Math.max(byMethod.tfidf?.hits?.length || 0, byMethod.bm25?.hits?.length || 0) };
  }, [compare, byMethod.tfidf, byMethod.bm25]);

  const count = results.reduce((m, r) => Math.max(m, r.hits?.length || 0), 0);

  return (
    <section className="search-panel text-search-panel">
      <div className="search-panel-header">
        <div className="search-panel-title">
          <TextSearch size={19} />
          <div>
            <h2>Búsqueda textual</h2>
            <p>Índice invertido SPIMI · ranking TF-IDF + coseno y BM25</p>
          </div>
        </div>
        <div className="panel-header-actions">
          <div className="map-result-count">{data ? `${count} documento(s)` : "Sin consulta"}</div>
          <button className="expand-toggle" onClick={onClose} title="Cerrar (Esc)">
            <X size={16} />
          </button>
        </div>
      </div>

      {textTables.length === 0 ? (
        <div className="search-empty">
          <TextSearch size={34} />
          <h3>No hay tablas con columnas de texto</h3>
          <p>
            Crea una tabla con una columna <code>VARCHAR</code> (por ejemplo, con <code>CLUSTERED BY</code> para
            documentos largos) o carga la colección de ejemplo: 42 documentos en español de bases de datos, IA,
            cocina, deportes, historia y más.
          </p>
          <button className="btn-primary" onClick={handleDemo} disabled={demoBusy}>
            <Database size={15} />
            {demoBusy ? "Cargando…" : "Cargar documentos de ejemplo"}
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
                  setColumn("");
                  setData(null);
                }}
              >
                {textTables.map((t) => (
                  <option key={t.Name} value={t.Name}>
                    {t.Name} ({t.Records})
                  </option>
                ))}
              </select>
            </div>
            <div className="search-field">
              <label>Columna</label>
              <select value={column} onChange={(e) => setColumn(e.target.value)}>
                {(table?.Columns || [])
                  .filter((c) => isTextType(c.Type))
                  .map((c) => (
                    <option key={c.Name} value={c.Name}>
                      {c.Name}
                    </option>
                  ))}
              </select>
            </div>
            <div className="search-field grow">
              <label>Consulta</label>
              <input
                ref={inputRef}
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder="Escribe palabras clave…"
                list="text-search-suggestions"
              />
              <datalist id="text-search-suggestions">
                {SUGGESTIONS.map((s) => (
                  <option key={s} value={s} />
                ))}
              </datalist>
            </div>
            <div className="search-field fixed">
              <label>Técnica</label>
              <div className="segmented">
                {MODES.map((m) => (
                  <button type="button" key={m.id} className={mode === m.id ? "active" : ""} onClick={() => setMode(m.id)}>
                    {m.label}
                  </button>
                ))}
              </div>
            </div>
            <div className="search-field narrow">
              <label>Top-k</label>
              <input type="number" min="1" max="200" value={k} onChange={(e) => setK(e.target.value)} />
            </div>
            {usesBM25 && (
              <>
                <div className="search-field narrow" title="Saturación de la frecuencia del término">
                  <label>k1</label>
                  <input type="number" min="0" step="0.1" value={k1} onChange={(e) => setK1(e.target.value)} />
                </div>
                <div className="search-field narrow" title="Normalización por longitud del documento (0 a 1)">
                  <label>b</label>
                  <input type="number" min="0" max="1" step="0.05" value={b} onChange={(e) => setB(e.target.value)} />
                </div>
              </>
            )}
            <button className="spatial-search-button" type="submit" disabled={loading || !query.trim()}>
              <Search size={16} />
              {loading ? "Buscando…" : "Buscar"}
            </button>
          </form>

          {error && <div className="search-error bar">{error}</div>}

          {data && (
            <div className="search-meta">
              <div className="search-meta-group">
                <span className="meta-label">Términos</span>
                {data.tokens.length ? (
                  data.tokens.map((t, i) => (
                    <span key={`${t}-${i}`} className="token-chip" title={`Aparece en ${data.df[t]} documento(s)`}>
                      {t}
                      <small>df {data.df[t]}</small>
                    </span>
                  ))
                ) : (
                  <em>la consulta quedó vacía tras quitar stopwords</em>
                )}
              </div>
              <div className="search-meta-group stats">
                <span title="Documentos indexados">
                  <b>{data.index.docs}</b> docs
                </span>
                <span title="Términos distintos en el índice">
                  <b>{data.index.terms}</b> términos
                </span>
                <span title="Tamaño del índice invertido en disco">
                  <b>{fmtBytes(data.index.bytes)}</b>
                </span>
                <span title="Preprocesamiento + construcción SPIMI">
                  índice {data.index.cached ? "en caché" : `construido en ${fmtMs(data.index.prepMs + data.index.buildMs)}`}
                </span>
              </div>
            </div>
          )}

          <div className="search-tabs">
            <div className="segmented">
              <button className={tab === "results" ? "active" : ""} onClick={() => setTab("results")}>
                <ListOrdered size={14} />
                Resultados
              </button>
              <button className={tab === "plan" ? "active" : ""} onClick={() => setTab("plan")}>
                <GitBranch size={14} />
                Plan de ejecución
              </button>
            </div>
            {data && overlap && (
              <span className="search-tabs-note">
                Coinciden <b>{overlap.both}</b> de {overlap.k} documentos en el top-k de ambas técnicas
              </span>
            )}
            <button
              className="spatial-secondary-button"
              disabled={!table || !column || !query.trim()}
              onClick={() =>
                onOpenSQL?.(textSearchSQL({ table: table.Name, column, query, k: Number(k) || 10, columns: table.Columns.map((c) => c.Name) }))
              }
              title="Escribe en el editor la consulta SQL equivalente con MATCH (TF-IDF del motor)"
            >
              <Code2 size={15} />
              Ver como SQL
            </button>
          </div>

          <div className="search-body">
            {!data && !loading && (
              <div className="search-empty">
                <Sparkles size={30} />
                <h3>Busca en {table?.Name}.{column}</h3>
                <p>La consulta se tokeniza, se quitan stopwords y se aplica stemming antes de buscar en el índice invertido.</p>
                <div className="suggestion-row">
                  {SUGGESTIONS.map((s) => (
                    <button
                      key={s}
                      className="suggestion"
                      onClick={() => {
                        setQuery(s);
                        run(s);
                      }}
                    >
                      {s}
                    </button>
                  ))}
                </div>
              </div>
            )}

            {data && tab === "results" && (
              <div className={compare ? "text-compare" : "text-single"}>
                {results.map((r) => (
                  <div className="text-column" key={r.method}>
                    {compare && (
                      <div className="text-column-head">
                        <strong>{r.label}</strong>
                        <span>{fmtMs(r.elapsedMs)}</span>
                      </div>
                    )}
                    <ResultList
                      result={r}
                      columns={columns}
                      bodyCol={bodyCol}
                      other={compare ? results.find((x) => x.method !== r.method) : null}
                      showDelta={compare}
                    />
                  </div>
                ))}
              </div>
            )}

            {data && tab === "plan" && (
              <div className="search-plan">
                <div className="plan-card">
                  <h4>Índice invertido (SPIMI)</h4>
                  <dl>
                    <dt>Documentos</dt>
                    <dd>{data.index.docs}</dd>
                    <dt>Términos</dt>
                    <dd>{data.index.terms}</dd>
                    <dt>Posteos</dt>
                    <dd>{data.index.postings}</dd>
                    <dt>Largo promedio</dt>
                    <dd>{data.index.avgDocLen.toFixed(1)} términos</dd>
                    <dt>Bloques / merges</dt>
                    <dd>
                      {data.index.blocks} / {data.index.mergeRounds}
                    </dd>
                    <dt>Tamaño en disco</dt>
                    <dd>{fmtBytes(data.index.bytes)}</dd>
                    <dt>Preprocesamiento</dt>
                    <dd>{fmtMs(data.index.prepMs)}</dd>
                    <dt>Construcción</dt>
                    <dd>{fmtMs(data.index.buildMs)}</dd>
                  </dl>
                </div>
                {results.map((r) => (
                  <div className="plan-card" key={r.method}>
                    <h4>
                      {r.label} <span>{fmtMs(r.elapsedMs)}</span>
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
                {compare && (
                  <div className="plan-card wide">
                    <h4>Tiempo de consulta</h4>
                    <div className="bars">
                      {results.map((r) => {
                        const max = Math.max(...results.map((x) => x.elapsedMs || 0), 0.001);
                        return (
                          <div className="bar-row" key={r.method}>
                            <span>{r.label}</span>
                            <div className="bar-track">
                              <i className={`m-${r.method}`} style={{ width: `${Math.max(3, (r.elapsedMs / max) * 100)}%` }} />
                            </div>
                            <b>{fmtMs(r.elapsedMs)}</b>
                          </div>
                        );
                      })}
                    </div>
                  </div>
                )}
              </div>
            )}
          </div>
        </>
      )}
    </section>
  );
}

export default TextSearchPanel;
