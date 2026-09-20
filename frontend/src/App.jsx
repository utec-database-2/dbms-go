import { useCallback, useEffect, useState } from "react";
import Header from "./components/Header";
import Toolbar from "./components/Toolbar";
import FilePanel from "./components/FilePanel";
import QueryPanel from "./components/QueryPanel";
import ResultPanel from "./components/ResultPanel";
import ExecutionPlanPanel from "./components/ExecutionPlanPanel";
import { importCsv, listTables, runQuery } from "./api";
import "./App.css";

function App() {
  const [darkMode, setDarkMode] = useState(true);

  const [tables, setTables] = useState([]);
  const [tablesError, setTablesError] = useState(null);
  const [selectedTableName, setSelectedTableName] = useState(null);

  const [query, setQuery] = useState(
    "CREATE TABLE alumno (codigo INT, nombre STRING, ciclo INT);",
  );
  const [result, setResult] = useState(null);
  const [lastQuery, setLastQuery] = useState("");
  const [queryError, setQueryError] = useState(null);
  const [loading, setLoading] = useState(false);

  const [notice, setNotice] = useState(null);

  const toggleTheme = () => setDarkMode((v) => !v);

  const refreshTables = useCallback(async () => {
    try {
      const data = await listTables();
      const list = data || [];
      setTables(list);
      setTablesError(null);
      setSelectedTableName((current) => {
        if (current && list.some((t) => t.Name === current)) return current;
        return list[0]?.Name ?? null;
      });
    } catch (err) {
      setTablesError(err.message);
    }
  }, []);

  useEffect(() => {
    refreshTables();
  }, [refreshTables]);

  const handleExecute = useCallback(async () => {
    if (!query.trim()) return;
    setLoading(true);
    setQueryError(null);
    try {
      const res = await runQuery(query);
      setResult(res);
      setLastQuery(query);
      await refreshTables();
    } catch (err) {
      setQueryError(err.message);
      setResult(null);
    } finally {
      setLoading(false);
    }
  }, [query, refreshTables]);

  const handleImport = useCallback(
    async (tableName, file) => {
      if (!tableName) {
        setNotice({ type: "error", text: "No hay tabla seleccionada para importar." });
        return;
      }
      setLoading(true);
      try {
        const res = await importCsv(tableName, file);
        const parts = [`${res.inserted} fila(s) importadas a "${res.table}"`];
        if (res.skipped) parts.push(`${res.skipped} omitida(s)`);
        setNotice({
          type: res.errors?.length ? "warning" : "success",
          text: parts.join(", "),
          details: res.errors,
        });
        await refreshTables();
      } catch (err) {
        setNotice({ type: "error", text: err.message });
      } finally {
        setLoading(false);
      }
    },
    [refreshTables],
  );

  return (
    <div className={darkMode ? "app dark" : "app light"}>
      <Header darkMode={darkMode} toggleTheme={toggleTheme} apiOk={!tablesError} />

      <Toolbar
        tables={tables}
        selectedTableName={selectedTableName}
        onImport={handleImport}
        onExecute={handleExecute}
        loading={loading}
      />

      {notice && (
        <div className={`app-notice ${notice.type}`}>
          <span>{notice.text}</span>
          {notice.details?.length > 0 && (
            <ul>
              {notice.details.map((d, i) => (
                <li key={i}>{d}</li>
              ))}
            </ul>
          )}
          <button onClick={() => setNotice(null)}>×</button>
        </div>
      )}

      <main className="dashboard-grid">
        <FilePanel
          tables={tables}
          error={tablesError}
          selectedTableName={selectedTableName}
          onSelectTable={setSelectedTableName}
        />

        <QueryPanel
          query={query}
          onQueryChange={setQuery}
          onExecute={handleExecute}
          loading={loading}
        />

        <ResultPanel result={result} error={queryError} loading={loading} />

        <ExecutionPlanPanel result={result} query={lastQuery} />
      </main>
    </div>
  );
}

export default App;
