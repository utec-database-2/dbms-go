import {
  Table2,
  Upload,
  Download,
  FilePlus,
  Play,
  ListTree,
  Trash2,
  History
} from "lucide-react";

function Toolbar() {
  
  const handleClick = (action) => {
    console.log(`Botón presionado: ${action}`);
  };

  return (
    <div className="toolbar">

      {/* NUEVA TABLA */}
      {/* <button
        className="toolbar-button"
        onClick={() => handleClick("Nueva Tabla")}
      >
        <Table2 size={17} />
        <span>Nueva Tabla</span>
      </button> */}

      {/* IMPORTAR */}
      {/* <button
        className="toolbar-button"
        onClick={() => handleClick("Importar Datos")}
      >
        <Upload size={17} />
        <span>Importar Datos</span>
      </button> */}

      {/* EXPORTAR */}
      {/* <button
        className="toolbar-button"
        onClick={() => handleClick("Exportar Datos")}
      >
        <Download size={17} />
        <span>Exportar Datos</span>
      </button> */}

      {/* NUEVA CONSULTA */}
      <button
        className="toolbar-button"
        onClick={() => handleClick("Nueva Consulta")}
      >
        <FilePlus size={17} />
        <span>Nueva Consulta</span>
      </button>

      {/* EJECUTAR */}
      <button
        className="toolbar-button execute"
        onClick={() => handleClick("Ejecutar")}
      >
        <Play size={17} />
        <span>Ejecutar (F5)</span>
      </button>

      {/* EXPLICAR PLAN */}
      {/* <button
        className="toolbar-button"
        onClick={() => handleClick("Explicar Plan")}
      >
        <ListTree size={17} />
        <span>Explicar Plan</span>
      </button> */}

      {/* LIMPIAR */}
      <button
        className="toolbar-button"
        onClick={() => handleClick("Limpiar")}
      >
        <Trash2 size={17} />
        <span>Limpiar</span>
      </button>

      {/* HISTORIAL */}
      <button
        className="toolbar-button"
        onClick={() => handleClick("Historial")}
      >
        <History size={17} />
        <span>Historial</span>
      </button>

    </div>
  );
}

export default Toolbar;