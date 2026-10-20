import { useCallback, useEffect, useMemo, useState } from "react";
import {
  ClockArrowUp,
  Eraser,
  FileSpreadsheet,
  GitBranch,
  LayoutDashboard,
  Map as MapIcon,
  Moon,
  Play,
  Settings2,
  Sparkles,
  Sun,
  Table2,
  WandSparkles,
} from "lucide-react";
import Header from "./components/Header";
import ActivityBar from "./components/ActivityBar";
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
  // Punto/fila elegido en el mapa o en la tabla, para resaltarlo en el otro.
  const [linked, setLinked] = useState(null);
  // Diálogo abierto: "settings" | "palette" | "history" | { type: "import", file }.
  const [dialog, setDialog] = useState(null);
  const closeDialog = useCallback(() => setDialog(null), []);
  // La consulta espacial vive en una ventana emergente que se abre desde la
  // barra de la izquierda (queda montada para no perder la búsqueda).
  const [mapOpen, setMapOpen] = useState(false);

  const showPlan = settings.showPlan;
  const togglePlan = useCallback(
    () => updateSettings((s) => ({ showPlan: !s.showPlan })),
    [updateSettings],
  );

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
  // El servidor recibe el script completo en una sola petición y devuelve un
  // resultado por sentencia (Results); si una falla, Failed/Error la señalan
  // y Results trae las que sí corrieron.
  const handleExecute = useCallback(
    async (text) => {
      const source = typeof text === "string" ? text : query;
      if (!source.trim()) return;
      const statements = splitStatements(source);
      const label = (i) => statements[i] ?? `Sentencia ${i + 1}`;

      setLoading(true);
      setNotice(null);

      try {
        const res = await runQuery(source);
        const list = res.Results || [];
        const failed = typeof res.Failed === "number" ? res.Failed : -1;

        const next = list.map((r, i) => ({ sql: label(i), result: r }));
        if (failed >= 0) next.push({ sql: label(failed), error: res.Error });

        // Transacción abierta al terminar: cada petición usa su propia sesión
        // y el servidor la cierra al final, deshaciendo lo que no se confirmó.
        let openTxn = null;
        for (const r of list) {
          const t = transactionFromMessage(r.Message);
          if (t) openTxn = t.active ? t.id : null;
        }

        for (const run of next) {
          addHistory(
            run.result
              ? {
                  sql: run.sql,
                  ok: true,
                  ms: run.result.ElapsedMs,
                  rows: run.result.Rows?.length ?? 0,
                  affected: run.result.Affected,
                  access: accessKind(run.result),
                }
              : { sql: run.sql, ok: false, error: run.error, access: "none" },
          );
        }

        setRuns(next);
        setActiveRun(Math.max(0, next.length - 1));

        const total = failed >= 0 ? failed + 1 : list.length;
        const rolledBack =
          openTxn != null
            ? ` La transacción ${openTxn} quedó sin END TRANSACTION y el servidor la deshizo (ROLLBACK) al terminar.`
            : "";
        if (failed >= 0 && total > 1) {
          setNotice({
            type: "warning",
            text: `Se ejecutaron ${failed} sentencia(s); se detuvo en la ${failed + 1} por un error.${rolledBack}`,
          });
        } else if (rolledBack) {
          setNotice({ type: "warning", text: rolledBack.trim() });
        } else if (list.length > 1) {
          setNotice({
            type: "success",
            text: `${list.length} sentencias ejecutadas en ${res.ElapsedMs?.toFixed(2)} ms.`,
          });
        }
      } catch (err) {
        setRuns([{ sql: source, error: err.message }]);
        setActiveRun(0);
        addHistory({ sql: source, ok: false, error: err.message, access: "none" });
      }

      setLinked(null);
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

  // Atajos globales: Ctrl/⌘ + K abre la paleta; Esc cierra la consulta espacial.
  useEffect(() => {
    const onKey = (event) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        setDialog((d) => (d === "palette" ? null : "palette"));
      } else if (event.key === "Escape" && mapOpen && !document.querySelector(".modal-layer")) {
        setMapOpen(false);
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [mapOpen]);

  const commands = useMemo(
    () => [
      { id: "run", group: "Consulta", label: "Ejecutar consulta", hint: "Ctrl Enter", icon: <Play size={15} />, run: () => handleExecute() },
      { id: "format", group: "Consulta", label: "Formatear SQL", icon: <WandSparkles size={15} />, run: () => setQuery((q) => formatSql(q)) },
      { id: "clear", group: "Consulta", label: "Limpiar editor", icon: <Eraser size={15} />, run: () => setQuery("") },
      { id: "map", group: "Herramientas", label: "Abrir consulta espacial", icon: <MapIcon size={15} />, run: () => setMapOpen(true) },
      {
        id: "plan",
        group: "Herramientas",
        label: showPlan ? "Ocultar plan de ejecución" : "Mostrar plan de ejecución",
        icon: <GitBranch size={15} />,
        run: togglePlan,
      },
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
    [handleExecute, openImport, darkMode, toggleTheme, resetLayout, tables, showPlan, togglePlan],
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
        <Header apiOk={!tablesError} onImport={() => openImport()} />
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

      <div className="workbench">
        <ActivityBar
          mapOpen={mapOpen}
          planOpen={showPlan}
          historyOpen={dialog === "history"}
          paletteOpen={dialog === "palette"}
          settingsOpen={dialog === "settings"}
          onMap={() => setMapOpen((v) => !v)}
          onHistory={() => setDialog("history")}
          onPalette={() => setDialog("palette")}
          onPlan={togglePlan}
          onSettings={() => setDialog("settings")}
        />

        <Dashboard
          layout={settings.layout}
          onLayoutChange={(layout) => updateSettings({ layout })}
          onResetLayout={resetLayout}
          showPlan={showPlan}
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
              onShowOnMap={() => setMapOpen(true)}
            />
          }
          plan={
            <ExecutionPlanPanel
              result={result}
              query={current?.sql || ""}
              history={history}
              onClose={togglePlan}
            />
          }
        />
      </div>

      {/* Consulta espacial: ventana emergente que se abre desde la barra lateral. */}
      <div
        className={mapOpen ? "map-window-layer open" : "map-window-layer"}
        aria-hidden={!mapOpen}
        onMouseDown={(event) => event.target === event.currentTarget && setMapOpen(false)}
      >
        <div className="map-window">
          <MapPanel linked={linked} onLink={setLinked} onClose={() => setMapOpen(false)} />
        </div>
      </div>

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
