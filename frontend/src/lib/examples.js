// Ejemplos rápidos para no tener que escribir todo a mano. Los usan el Panel
// de Consultas y la paleta de comandos.
export const EXAMPLES = [
  {
    name: "Crear tabla",
    sql: "CREATE TABLE alumno (codigo INT PRIMARY KEY, nombre VARCHAR(40), ciclo INT);",
  },
  {
    name: "Insertar alumnos",
    sql: "INSERT INTO alumno VALUES (1, 'Ana', 5), (2, 'Bob', 5), (3, 'Cid', 6);",
  },
  {
    name: "Ver todos",
    sql: "SELECT * FROM alumno;",
  },
  {
    name: "Buscar por código",
    sql: "SELECT * FROM alumno WHERE codigo = 1;",
  },
  {
    name: "Rango (B+)",
    sql: "SELECT * FROM alumno WHERE codigo >= 1 AND codigo <= 2;",
  },
  {
    name: "Ordenar por ciclo",
    sql: "SELECT * FROM alumno ORDER BY ciclo DESC;",
  },
  {
    name: "GROUP BY",
    sql: "SELECT ciclo, COUNT(*) AS alumnos FROM alumno GROUP BY ciclo;",
  },
  {
    name: "Tabla matrícula",
    sql: "CREATE TABLE matricula (id INT PRIMARY KEY, codigo INT, curso VARCHAR(30));",
  },
  {
    name: "Insertar matrículas",
    sql: "INSERT INTO matricula VALUES (1, 1, 'BD2'), (2, 1, 'IA'), (3, 3, 'SO');",
  },
  {
    name: "JOIN",
    sql: "SELECT a.nombre, m.curso FROM alumno a JOIN matricula m ON a.codigo = m.codigo;",
  },
  {
    name: "Tabla espacial",
    sql: "CREATE TABLE ubicaciones (id INT PRIMARY KEY, nombre VARCHAR(60), tipo VARCHAR(30), ubicacion POINT);",
  },
  {
    name: "Insertar ubicaciones",
    sql: "INSERT INTO ubicaciones VALUES (1, 'Plaza de Armas', 'plaza', POINT(-12.0464, -77.0302)), (2, 'Parque Kennedy', 'parque', POINT(-12.1211, -77.0297)), (3, 'Estadio Nacional', 'estadio', POINT(-12.0670, -77.0336));",
  },
  {
    name: "Radio 5 km",
    sql: "SELECT nombre, distancia(ubicacion, POINT(-12.05, -77.03)) AS metros FROM ubicaciones WHERE distancia(ubicacion, POINT(-12.05, -77.03)) < 5000;",
  },
  {
    name: "k-NN",
    sql: "SELECT nombre FROM ubicaciones ORDER BY distancia(ubicacion, POINT(-12.05, -77.03)) LIMIT 2;",
  },
  {
    name: "Polígono",
    sql: "SELECT nombre FROM ubicaciones WHERE dentro(ubicacion, POLYGON(POINT(-12.00, -77.10), POINT(-12.00, -77.00), POINT(-12.10, -77.00), POINT(-12.10, -77.10))) = true;",
  },
  {
    name: "Tabla de documentos",
    sql: "CREATE TABLE documentos CLUSTERED BY (id) (id INT PRIMARY KEY, titulo VARCHAR(80), categoria VARCHAR(40), contenido VARCHAR(600));",
  },
  {
    name: "Búsqueda de texto (MATCH)",
    sql: "SELECT id, titulo, MATCH(contenido, 'indice arbol consultas rango') AS score\nFROM documentos\nWHERE MATCH(contenido, 'indice arbol consultas rango') > 0\nORDER BY score DESC\nLIMIT 5;",
  },
  {
    name: "Tabla de vectores",
    sql: "CREATE TABLE embeddings CLUSTERED BY (id) (id INT PRIMARY KEY, nombre VARCHAR(40), v VECTOR(3));",
  },
  {
    name: "Insertar vectores",
    sql: "INSERT INTO embeddings VALUES (1, 'rojo', VECTOR(1, 0, 0)), (2, 'naranja', VECTOR(0.9, 0.4, 0)), (3, 'verde', VECTOR(0, 1, 0)), (4, 'azul', VECTOR(0, 0, 1)), (5, 'celeste', VECTOR(0, 0.5, 0.9));",
  },
  {
    name: "Índice IVF",
    sql: "CREATE INDEX idx_embeddings_v ON embeddings USING IVF (v) WITH (nlist=2, nprobe=2, metric='cosine');",
  },
  {
    name: "k-NN vectorial",
    sql: "SELECT id, nombre, vector_distance(v, VECTOR(1, 0.2, 0), 'cosine') AS distancia\nFROM embeddings\nORDER BY vector_distance(v, VECTOR(1, 0.2, 0), 'cosine')\nLIMIT 3;",
  },
  {
    name: "Script (varias)",
    sql: "BEGIN TRANSACTION;\nINSERT INTO alumno VALUES (4, 'Dan', 7);\nSELECT * FROM alumno WHERE codigo >= 1 AND codigo <= 4;\nEND TRANSACTION;",
  },
  {
    name: "BEGIN",
    sql: "BEGIN TRANSACTION;",
  },
  {
    name: "END",
    sql: "END TRANSACTION;",
  },
];
