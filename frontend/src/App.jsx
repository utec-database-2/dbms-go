import { useState } from "react";
import Header from "./components/Header";
import Toolbar from "./components/Toolbar";
import "./App.css";

function App() {
  const [darkMode, setDarkMode] = useState(true);

  const toggleTheme = () => {
    setDarkMode(!darkMode);
  };

  return (
    <div className={darkMode ? "app dark" : "app light"}>
      <Header darkMode={darkMode} toggleTheme={toggleTheme} />

      <Toolbar />

      {/* 
        Aquí posteriormente colocaremos:
        1. Panel de archivos
        2. Panel de consultas
        3. Panel de resultados
        4. Panel de plan de ejecución
      */}

      <main className="main-content">
        <div className="welcome">
          <h2>MiniGestor BD</h2>
          <p>
            Los paneles del gestor se agregarán aquí posteriormente.
          </p>
        </div>
      </main>
    </div>
  );
}

export default App;