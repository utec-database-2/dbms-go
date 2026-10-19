import { useCallback, useEffect, useMemo, useState } from "react";
import {
  ClockArrowUp,
  Eraser,
  FileSpreadsheet,
  LayoutDashboard,
  Moon,
  Play,
  Settings2,
  Sparkles,
  Sun,
  Table2,
  WandSparkles,
} from "lucide-react";
import Header from "./components/Header";
import Toolbar from "./components/Toolbar";
import FilePanel from "./components/FilePanel";
import QueryPanel from "./components/QueryPanel";
import ResultPanel from "./components/ResultPanel";
import ExecutionPlanPanel from "./components/ExecutionPlanPanel";
import MapPanel from "./components/MapPanel";
import Dashboard from "./components/Dashboard";
import SettingsDialog from "./components/SettingsDialog";
import ImportDialog from "./components/ImportDialog";
import CommandPalette from "./components/CommandPalette";
import HistoryDrawer from "./components/HistoryDrawer";
import { importCsv, listTables, runQuery } from "./api";
import { EXAMPLES } from "./lib/examples";
import { DEFAULT_LAYOUT, useHistory, useSettings } from "./lib/storage";
import { accessKind, formatSql, splitStatements, transactionFromMessage } from "./lib/sql";
import "./App.css";

function App() {
  // Preferencias guardadas en el navegador. La interfaz arranca en tema claro.
  const [settings, updateSettings] = useSettings();
  const darkMode = settings.theme === "dark";
  const [history, addHistory, clearHistory] = useHistory();

  const [tables, setTables] = useState([]);
  const [tablesError, setTablesError] = useState(null);
  const [selectedTableName, setSelectedTableName] = useState(null);

  const [query, setQuery] = useState(
    "CREATE TABLE alumno (codigo INT PRIMARY KEY, nombre VARCHAR(40), ciclo INT);",
  );
  // Una entrada por sentencia de la última ejecución: { sql, result?, error? }.
  const [runs, setRuns] = useState([]);
  const [activeRun, setActiveRun] = useState(0);
  const [loading, setLoading] = useState(false);

  const [notice, setNotice] = useState(null);
  // Transacción abierta (entre BEGIN y END/COMMIT/ROLLBACK).
  const [txn, setTxn] = useState({ active: false });
  // Punto/fila elegido en el mapa o en la tabla, para resaltarlo en el otro.
  const [linked, setLinked] = useState(null);
  // Diálogo abierto: "settings" | "palette" | "history" | { type: "import", file }.
  const [dialog, setDialog] = useState(null);
  const closeDialog = useCallback(() => setDialog(null), []);

  const current = runs[activeRun] || null;
  const result = current?.result || null;
  const queryError = current?.error || null;

  const toggleTheme = useCallback(
    () => updateSettings((s) => ({ theme: s.theme === "dark" ? "light" : "dark" })),
    [updateSettings],
  );
  const resetLayout = useCallback(
    () => updateSettings({ layout: DEFAULT_LAYOUT }),
    [updateSettings],
  );

  const applyTables = useCallback((data) => {
    const list = data || [];
    setTables(list);
    setTablesError(null);
    setSelectedTableName((current) => {
      if (current && list.some((t) => t.Name === current)) return current;
      return list[0]?.Name ?? null;
    });
  }, []);

  const refreshTables = useCallback(async () => {
    try {
      applyTables(await listTables());
    } catch (err) {
      setTablesError(err.message);
    }
  }, [applyTables]);

  useEffect(() => {
    let active = true;

    listTables()
      .then((data) => active && applyTables(data))
      .catch((err) => active && setTablesError(err.message));

    return () => {
      active = false;
    };
  }, [applyTables]);

  // Los avisos de éxito se cierran solos.
  useEffect(() => {
    if (notice?.type !== "success") return;
    const timer = setTimeout(() => setNotice(null), 4500);
    return () => clearTimeout(timer);
  }, [notice]);

  // Ejecuta el texto del editor (o el texto que se pase, p. ej. la selección).
  // El servidor recibe una sentencia por petición: el script se divide por ";"
  // y se manda en orden. Si una falla, se detiene ahí.
  const handleExecute = useCallback(
    async (text) => {
      const source = typeof text === "string" ? text : query;
      const statements = splitStatements(source);
      if (statements.length === 0) return;

      setLoading(true);
      setNotice(null);
      const done = [];
      let totalMs = 0;
      // Estado de la transacción según lo que va respondiendo el motor.
      let txnState;

      for (let i = 0; i < statements.length; i++) {
        const sql = statements[i];
        try {
          const res = await runQuery(sql);
          totalMs += res.ElapsedMs || 0;
          done.push({ sql, result: res });
          const t = transactionFromMessage(res.Message);
          if (t) {
            txnState = t;
            setTxn(t);
          }
          addHistory({
            sql,
            ok: true,
            ms: res.ElapsedMs,
            rows: res.Rows?.length ?? 0,
            affected: res.Affected,
            access: accessKind(res),
          });
        } catch (err) {
          const message =
            statements.length > 1
              ? `Sentencia ${i + 1} de ${statements.length}: ${err.message}`
              : err.message;
          done.push({ sql, error: message });
          addHistory({ sql, ok: false, error: err.message, access: "none" });
          break;
        }
        setRuns([...done]);
        setActiveRun(done.length - 1);
      }

      setRuns(done);
      setActiveRun(done.length - 1);
      setLinked(null);

      if (statements.length > 1) {
        const failed = done.some((r) => r.error);
        setNotice(
          failed
            ? {
                type: "warning",
                text:
                  `Se ejecutaron ${done.length - 1} de ${statements.length} sentencias; se detuvo en la ${done.length} por un error.` +
                  (txnState?.active
                    ? " La transacción sigue abierta: ciérrala con ROLLBACK o END TRANSACTION."
                    : ""),
              }
            : {
                type: "success",
                text: `${statements.length} sentencias ejecutadas en ${totalMs.toFixed(2)} ms.`,
              },
        );
      }

      await refreshTables();
      setLoading(false);
    },
    [query, refreshTables, addHistory],
  );

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

  const openImport = useCallback(
    (file) => {
      if (!tables.length) {
        setNotice({
          type: "error",
          text: "Primero crea una tabla con CREATE TABLE en el Panel de Consultas.",
        });
        return;
      }
      setDialog({ type: "import", file: file instanceof File ? file : null });
    },
    [tables.length],
  );

  // Atajo global: Ctrl/⌘ + K abre la paleta de comandos.
  useEffect(() => {
    const onKey = (event) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        setDialog((d) => (d === "palette" ? null : "palette"));
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  const commands = useMemo(
    () => [
      { id: "run", group: "Consulta", label: "Ejecutar consulta", hint: "Ctrl Enter", icon: <Play size={15} />, run: () => handleExecute() },
      { id: "format", group: "Consulta", label: "Formatear SQL", icon: <WandSparkles size={15} />, run: () => setQuery((q) => formatSql(q)) },
      { id: "clear", group: "Consulta", label: "Limpiar editor", icon: <Eraser size={15} />, run: () => setQuery("") },
      { id: "history", group: "Herramientas", label: "Historial y rendimiento", icon: <ClockArrowUp size={15} />, run: () => setDialog("history") },
      { id: "import", group: "Herramientas", label: "Importar CSV", icon: <FileSpreadsheet size={15} />, run: () => openImport() },
      { id: "settings", group: "Herramientas", label: "Ajustes", icon: <Settings2 size={15} />, run: () => setDialog("settings") },
      {
        id: "theme",
        group: "Interfaz",
        label: darkMode ? "Cambiar a tema claro" : "Cambiar a tema oscuro",
        icon: darkMode ? <Sun size={15} /> : <Moon size={15} />,
        run: toggleTheme,
      },
      { id: "layout", group: "Interfaz", label: "Restablecer distribución de paneles", icon: <LayoutDashboard size={15} />, run: resetLayout },
      ...tables.map((t) => ({
        id: `table-${t.Name}`,
        group: "Tablas",
        label: `Ver tabla ${t.Name}`,
        icon: <Table2 size={15} />,
        run: () => {
          setSelectedTableName(t.Name);
          const sql = `SELECT * FROM ${t.Name};`;
          setQuery(sql);
          handleExecute(sql);
        },
      })),
      ...EXAMPLES.map((ex) => ({
        id: `ex-${ex.name}`,
        group: "Ejemplos",
        label: `Ejemplo: ${ex.name}`,
        icon: <Sparkles size={15} />,
        run: () => setQuery(ex.sql),
      })),
    ],
    [handleExecute, openImport, darkMode, toggleTheme, resetLayout, tables],
  );

  const appClass = [
    "app",
    darkMode ? "dark" : "light",
    settings.darkMap ? "" : "map-light",
    settings.animations ? "" : "reduce-motion",
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <div className={appClass}>
      {loading && <div className="top-progress" />}

      <div className="topbar">
        <Header
          darkMode={darkMode}
          toggleTheme={toggleTheme}
          apiOk={!tablesError}
          txn={txn}
          onOpenSettings={() => setDialog("settings")}
          onOpenPalette={() => setDialog("palette")}
        />

        <Toolbar
          onOpenImport={() => openImport()}
          onOpenHistory={() => setDialog("history")}
          onExecute={handleExecute}
          loading={loading}
        />
      </div>

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

      <Dashboard
        layout={settings.layout}
        onLayoutChange={(layout) => updateSettings({ layout })}
        onResetLayout={resetLayout}
        files={
          <FilePanel
            tables={tables}
            error={tablesError}
            selectedTableName={selectedTableName}
            onSelectTable={setSelectedTableName}
            onDropFile={openImport}
          />
        }
        query={
          <QueryPanel
            query={query}
            onQueryChange={setQuery}
            onExecute={handleExecute}
            loading={loading}
            tables={tables}
            fontSize={settings.editorFontSize}
          />
        }
        results={
          <ResultPanel
            result={result}
            error={queryError}
            loading={loading}
            runs={runs}
            activeRun={activeRun}
            onSelectRun={setActiveRun}
            rowsPerPage={settings.rowsPerPage}
            linked={linked}
            onLink={setLinked}
          />
        }
        plan={<ExecutionPlanPanel result={result} query={current?.sql || ""} history={history} />}
        map={<MapPanel linked={linked} onLink={setLinked} />}
      />

      {dialog === "settings" && (
        <SettingsDialog
          settings={settings}
          onChange={updateSettings}
          onResetLayout={resetLayout}
          onClearHistory={clearHistory}
          historyCount={history.length}
          onClose={closeDialog}
        />
      )}

      {dialog === "palette" && <CommandPalette commands={commands} onClose={closeDialog} />}

      {dialog === "history" && (
        <HistoryDrawer
          history={history}
          onClear={clearHistory}
          onClose={closeDialog}
          onLoad={(sql) => {
            setQuery(sql);
            closeDialog();
          }}
          onRun={(sql) => {
            setQuery(sql);
            closeDialog();
            handleExecute(sql);
          }}
        />
      )}

      {dialog?.type === "import" && (
        <ImportDialog
          tables={tables}
          defaultTable={selectedTableName}
          initialFile={dialog.file}
          onImport={handleImport}
          onClose={closeDialog}
        />
      )}
    </div>
  );
}

export default App;
