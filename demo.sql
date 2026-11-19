DROP TABLE IF EXISTS demo_t2;
DROP TABLE IF EXISTS demo_t1;
DROP TABLE IF EXISTS demo_tmp;
DROP TABLE IF EXISTS imagen;
DROP TABLE IF EXISTS tiendas;
DROP TABLE IF EXISTS docs;
DROP TABLE IF EXISTS matricula;
DROP TABLE IF EXISTS curso;
DROP TABLE IF EXISTS alumno;

CREATE TABLE alumno (
  codigo INT PRIMARY KEY,
  nombre VARCHAR(40),
  ciclo INT
);

CREATE CLUSTERED TABLE curso (
  id INT PRIMARY KEY,
  titulo VARCHAR(40)
);

CREATE TABLE matricula (
  id INT PRIMARY KEY,
  alumno_codigo INT,
  curso_id INT,
  nota INT
);

INSERT INTO alumno VALUES
  (1, 'Ana', 5),
  (2, 'Bob', 5),
  (3, 'Cid', 6),
  (4, 'Dan', 7);

INSERT INTO curso VALUES
  (10, 'Base de Datos 2'),
  (20, 'Inteligencia Artificial'),
  (30, 'Sistemas Operativos');

INSERT INTO matricula VALUES
  (100, 1, 10, 18),
  (101, 1, 20, 15),
  (102, 2, 10, 12),
  (103, 3, 30, 16),
  (104, 3, 10, 14);

CREATE INDEX hash_alumno_ciclo ON alumno (ciclo);
CREATE INDEX idx_alumno_nombre ON alumno (nombre);

SELECT * FROM alumno WHERE codigo = 3;

SELECT codigo, nombre
FROM alumno
WHERE codigo >= 1 AND codigo <= 3;

SELECT codigo, nombre
FROM alumno
WHERE ciclo = 5;

SELECT codigo, nombre
FROM alumno
WHERE nombre = 'Ana';

SELECT * FROM curso WHERE id = 20;

SELECT DISTINCT ciclo
FROM alumno
ORDER BY ciclo;

SELECT nombre, ciclo
FROM alumno
ORDER BY ciclo DESC
LIMIT 2 OFFSET 1;

SELECT ciclo, COUNT(*) AS total
FROM alumno
GROUP BY ciclo
HAVING COUNT(*) > 1
ORDER BY ciclo;

SELECT a.nombre, c.titulo, m.nota
FROM alumno a
JOIN matricula m ON a.codigo = m.alumno_codigo
JOIN curso c ON m.curso_id = c.id
WHERE m.nota >= 14
ORDER BY m.nota DESC;

SELECT a.nombre, COUNT(m.id) AS cursos
FROM alumno a
LEFT JOIN matricula m ON a.codigo = m.alumno_codigo
GROUP BY a.nombre
ORDER BY a.nombre;

UPDATE alumno SET ciclo = 6 WHERE codigo = 2;
DELETE FROM matricula WHERE id = 104;

SELECT * FROM alumno WHERE codigo = 2;
SELECT * FROM matricula WHERE id = 104;

BEGIN TRANSACTION;

UPDATE alumno SET ciclo = 7 WHERE codigo = 3;

SAVEPOINT antes_de_insertar;

INSERT INTO alumno VALUES (5, 'Eva', 8);

ROLLBACK TO SAVEPOINT antes_de_insertar;

COMMIT;

SELECT * FROM alumno WHERE codigo = 3;
SELECT * FROM alumno WHERE codigo = 5;

BEGIN TRANSACTION;
INSERT INTO alumno VALUES (99, 'Temporal', 1);
ROLLBACK;

SELECT * FROM alumno WHERE codigo = 99;

CREATE TABLE docs (
  id INT PRIMARY KEY,
  cuerpo VARCHAR(80)
);

INSERT INTO docs VALUES
  (1, 'base de datos'),
  (2, 'base de datos relacionales'),
  (3, 'grafos y redes'),
  (4, 'datos datos grandes');

SELECT id, MATCH(cuerpo, 'datos') AS score
FROM docs
WHERE MATCH(cuerpo, 'datos') > 0
ORDER BY MATCH(cuerpo, 'datos') DESC;

SELECT id
FROM docs
WHERE MATCH(cuerpo, 'relacional') > 0;

CREATE TABLE tiendas (
  id INT PRIMARY KEY,
  nombre VARCHAR(40),
  ubicacion POINT
);

INSERT INTO tiendas VALUES
  (1, 'Centro',  POINT(-12.0464, -77.0428)),
  (2, 'Norte',   POINT(-12.0200, -77.0428)),
  (3, 'Cercana', POINT(-12.0500, -77.0428)),
  (4, 'Sur',     POINT(-12.0700, -77.0428)),
  (5, 'Lejana',  POINT(-12.1060, -77.0428)),
  (6, 'Oeste',   'POINT(-12.0464 -77.0600)');

SELECT nombre, distancia(ubicacion, POINT(-12.0464, -77.0428)) AS metros
FROM tiendas
WHERE id = 1;

SELECT nombre
FROM tiendas
WHERE distancia(ubicacion, POINT(-12.0464, -77.0428)) < 3000;

SELECT nombre
FROM tiendas
ORDER BY distancia(ubicacion, POINT(-12.0464, -77.0428))
LIMIT 3;

SELECT nombre
FROM tiendas
WHERE dentro(ubicacion,
  POLYGON(
    POINT(-12.040, -77.050),
    POINT(-12.040, -77.035),
    POINT(-12.055, -77.035),
    POINT(-12.055, -77.050)
  )) = true;

SELECT nombre
FROM tiendas
WHERE distancia(ubicacion, POINT(-12.0464, -77.0428), 'euclidean') < 0.05;

CREATE TABLE imagen (
  id INT PRIMARY KEY,
  embedding VECTOR(3)
);

INSERT INTO imagen VALUES
  (1, VECTOR(1.0, 0.0, 0.0)),
  (2, VECTOR(0.9, 0.1, 0.0)),
  (3, VECTOR(0.0, 1.0, 0.0)),
  (4, VECTOR(0.0, 0.0, 1.0)),
  (5, VECTOR(0.5, 0.5, 0.0)),
  (6, VECTOR(0.7, 0.7, 0.0)),
  (7, VECTOR(1.0, 0.2, 0.0)),
  (8, VECTOR(0.2, 1.0, 0.0));

INSERT INTO imagen VALUES (9, '[1.0, 0.0, 0.0]');

CREATE INDEX ivf_imagen
ON imagen
USING IVF (embedding)
WITH (nlist = 2, nprobe = 2, metric = 'euclidean');

SELECT id, vector_distance(embedding, VECTOR(1.0, 0.0, 0.0)) AS distancia
FROM imagen
ORDER BY vector_distance(embedding, VECTOR(1.0, 0.0, 0.0))
LIMIT 3;

SELECT id
FROM imagen
WHERE id != 9
ORDER BY vector_distance(embedding, VECTOR(1.0, 0.0, 0.0))
LIMIT 3;

DROP INDEX ivf_imagen;

SELECT id
FROM imagen
ORDER BY vector_distance(embedding, VECTOR(1.0, 0.0, 0.0))
LIMIT 3;

CREATE TABLE demo_t1 (id INT PRIMARY KEY, x INT);
CREATE TABLE demo_t2 (id INT PRIMARY KEY, x INT);

INSERT INTO demo_t1 VALUES (1, 10), (2, 20);
INSERT INTO demo_t2 VALUES (1, 10), (2, 20);

CREATE INDEX idx_demo_x ON demo_t1 (x);
CREATE INDEX idx_demo_x ON demo_t2 (x);

DROP INDEX idx_demo_x ON demo_t2;

SELECT * FROM demo_t2 WHERE x = 20;

DROP INDEX idx_demo_x;

DROP INDEX IF EXISTS idx_demo_x;
DROP INDEX IF EXISTS idx_no_existe;

CREATE INDEX idx_demo_x ON demo_t1 (x);
DROP INDEX idx_demo_x;

CREATE TABLE demo_tmp (id INT PRIMARY KEY, valor INT);

INSERT INTO demo_tmp VALUES (1, 100), (2, 200), (3, 300);

TRUNCATE TABLE demo_tmp;

SELECT COUNT(*) FROM demo_tmp;

DROP TABLE demo_tmp;

DROP TABLE IF EXISTS demo_tmp;
