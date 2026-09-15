import { useState } from "react";
import Header from "./components/Header";
import Toolbar from "./components/Toolbar";
import FilePanel from "./components/FilePanel";
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

      <main className="main-content">
        <FilePanel />
      </main>
    </div>
  );
}

export default App;