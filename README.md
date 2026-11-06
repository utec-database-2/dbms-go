# dbms-go

Minigestor de base de datos multimodal escrito en Go para el curso de Base de Datos 2 (UTEC, 2026-2). Incluye almacenamiento en disco, índices, un motor SQL propio con transacciones concurrentes, un índice espacial R-Tree y un frontend web con mapa.

## Funcionalidades

**Parte 1: base de datos relacional**

- **Almacenamiento**: Heap File con páginas *slotted* y reutilización de espacio libre (*free list* o *move the last*), y Archivo Secuencial Paginado con inserción ordenada, borrado *lazy* (tombstones) y reorganización automática.
- **Índices**: B+ agrupado (sobre el archivo secuencial), B+ no agrupado (sobre el heap) y Hash Extensible.
- **Algoritmos externos**: `ORDER BY` con *external sorting* (runs + *k-way merge*, con volcado a disco), `GROUP BY` y `JOIN` con *external hashing* (particionado Grace hash).
- **SQL**: `CREATE TABLE`, `CREATE INDEX`, `INSERT`, `SELECT` (WHERE, JOIN, GROUP BY, HAVING, ORDER BY, LIMIT), `UPDATE`, `DELETE`, `DROP`, `TRUNCATE`. Cada consulta devuelve su plan de ejecución.
- **Transacciones y concurrencia**: `BEGIN TRANSACTION` / `END TRANSACTION` (o `COMMIT`), `ROLLBACK`, `SAVEPOINT`. Usa un WAL con recuperación al arrancar, sesiones concurrentes con bloqueo en dos fases estricto (2PL) y detección de *deadlocks* con grafo de espera.
- **Demostración con hilos**: `dbms concurrencia` simula race conditions y muestra cómo el motor las evita.

**Parte 2: base de datos espacial**

- **R-Tree** con división estilo R\*, búsqueda por radio, *k-NN best-first* e intersección con polígonos. Distancias Euclidiana y Haversine.
- **Extensión SQL**: tipo `POINT` con índice R-Tree automático y funciones `distancia`, `dentro`, `POINT` y `POLYGON`.
- **Panel de mapa** (Leaflet) con consultas por radio, k-NN y polígono.

**Partes 3 y 4 en el frontend: búsqueda textual y multimedia**

- **Ventana de búsqueda textual**: consulta sobre una columna de texto con el índice invertido SPIMI (preprocesamiento con stopwords y *stemming*) y ranking TF-IDF + coseno o BM25 (con `k1` y `b`). Resalta los términos encontrados, compara ambas técnicas lado a lado (cambios de posición y coincidencias en el top-k) y muestra el plan: términos, `df`, tamaño del índice y tiempos.
- **Ventana de búsqueda multimedia**: k-NN sobre una columna `VECTOR(n)` con HNSW, IVF (`nlist`, `nprobe`) o búsqueda secuencial, con distancia euclidiana, coseno o producto punto. Muestra una galería con aciertos y errores por categoría, el Recall@k de cada índice frente al secuencial y los tiempos de construcción y consulta. La consulta puede ser un elemento de la colección o una imagen subida.
- Ambas ventanas pueden cargar datos de ejemplo de `datos_demo/` con un botón y pasar su consulta al editor como SQL (`MATCH` y `vector_distance`).

**Comparaciones experimentales**: `dbms bench` mide Heap vs Secuencial, B+ agrupado vs no agrupado vs Hash, y Secuencial vs R-Tree vs GiST de PostgreSQL, y genera CSV y gráficas.

## Arquitectura

```
┌───────────────────────────────────────────────────────────────────────┐
│ Frontend (React + Vite + Leaflet)                                     │
│ Paneles: Archivos · Consultas · Resultados · Plan de ejecución · Mapa │
└───────────────────────────────┬───────────────────────────────────────┘
                                │ HTTP/JSON
┌───────────────────────────────▼───────────────────────────────────────┐
│ Servidor HTTP (dbms/cmd/server)                                       │
│ /api/query · /api/tables · /api/tables/{t}/import · /api/spatial/*    │
│ /api/text/search · /api/vector/* · /api/media/*                       │
└───────────────────────────────┬───────────────────────────────────────┘
┌───────────────────────────────▼───────────────────────────────────────┐
│ Motor SQL (dbms/lib/sql)                                              │
│ lexer/parser (dsl) → planificador → ejecutor                          │
│ sesiones + 2PL (lockmanager) · WAL (transaction)                      │
│ external sorting / external hashing (external)                        │
└──────────────┬──────────────────────────────────┬─────────────────────┘
┌──────────────▼───────────────────┐ ┌────────────▼─────────────────────┐
│ Índices (dbms/lib/index)         │ │ Índice espacial (index/rtree)    │
│ B+ agrupado · B+ no agrupado ·   │ │ radio · k-NN · polígono          │
│ Hash extensible                  │ │                                  │
└──────────────┬───────────────────┘ └────────────┬─────────────────────┘
┌──────────────▼──────────────────────────────────▼─────────────────────┐
│ Almacenamiento (dbms/lib/storage): Heap File · Archivo Secuencial     │
│ páginas de 4 KB, codificación de tuplas, RID (archivo, página, slot)  │
└───────────────────────────────────────────────────────────────────────┘
```

Cada sesión toma bloqueos de tabla (S para leer, X para escribir) antes de ejecutar una sentencia y, dentro de una transacción, los conserva hasta `COMMIT`/`ROLLBACK`. Si esperar un bloqueo cerraría un ciclo en el grafo de espera, la transacción que lo pidió se deshace (`ROLLBACK` automático) y el cliente puede reintentar.

## Organización del código

| Carpeta | Contenido |
| - | - |
| `dbms/lib/storage` | Tipos, codificación de tuplas, Heap File (`heap/`) y Archivo Secuencial Paginado (`sequential/`). |
| `dbms/lib/index` | B+ agrupado y no agrupado (`bplus/`), Hash Extensible (`extendible/`), R-Tree (`rtree/`). |
| `dbms/lib/external` | External sorting (`sorting/`) y external hashing (`hashing/`). |
| `dbms/lib/dsl` | Lexer, tokens, parser y AST del SQL. |
| `dbms/lib/sql` | Motor: catálogo, planificador, ejecución, JOIN, SQL espacial, sesiones y transacciones. |
| `dbms/lib/transaction` | Write-ahead log, rollback, savepoints y recuperación. |
| `dbms/lib/lockmanager` | Tabla de bloqueos S/X, 2PL y detección de deadlocks. |
| `dbms/lib/spatial` | Capa espacial que usa el panel de mapa (radio, k-NN y polígono). |
| `dbms/cmd/server` | Servidor HTTP para el frontend (`search.go`: búsqueda textual y vectorial). |
| `dbms/cmd/imgcsv` | Extrae el vector de cada imagen de una carpeta y genera un CSV para importarlo. |
| `datos_demo` | Datos de ejemplo de las partes 3 y 4: documentos y una galería de imágenes con sus vectores. |
| `dbms/cli` | `dbms bench` (comparaciones experimentales) y `dbms concurrencia` (demo con hilos). |
| `frontend` | Interfaz web (React + Vite + Leaflet). |
| `informe`, `presentacion` | Informe (LaTeX) y presentación (Beamer). |

## Instalación

Requisitos:

- Go 1.26 o superior
- Node.js 20.19 o superior y npm (para el frontend; lo exige Vite 8)
- PostgreSQL (sin PostGIS), solo si quieres repetir la comparación con GiST

```bash
git clone https://github.com/utec-database-2/dbms-go.git
cd dbms-go
go build ./...
cd frontend && npm install
```

## Uso

### 1. Servidor

```bash
go run ./dbms/cmd/server -addr :8080 -dir ./data -media ./datos_demo
```

Los archivos de las tablas, el catálogo y el WAL se guardan en `-dir` y se recuperan al reiniciar. `-media` es la carpeta de imágenes y audio que se sirve en `/api/media/`: las tablas multimedia guardan rutas relativas a ella.

### 2. Frontend

```bash
cd frontend
npm run dev
```

Abre la URL que imprime Vite (por defecto http://localhost:5173). El frontend usa `http://localhost:8080` como servidor; para usar otro, define `VITE_API_URL`. El Panel de Consultas trae ejemplos para cada funcionalidad.

Las búsquedas textual y multimedia se abren desde la barra lateral izquierda. Si no hay tablas adecuadas, cada ventana ofrece cargar los datos de `datos_demo/`: 42 documentos y 48 imágenes en 8 categorías. Para regenerar la galería o crear otra desde una carpeta de imágenes:

```bash
python3 datos_demo/generar_imagenes.py
go run ./dbms/cmd/imgcsv -dir datos_demo/imagenes -out datos_demo/imagenes.csv
```

Las tablas de documentos y de vectores grandes deben ser agrupadas (`CLUSTERED BY (id)`), porque el heap usa slots de 128 bytes.

### 3. Demostración de concurrencia

```bash
go run ./dbms/cli concurrencia -hilos 5 -depositos 3
```

Corre cinco escenarios: actualizaciones perdidas sin transacciones; transacciones con 2PL y deadlocks reintentados; `UPDATE saldo = saldo + 10` en fila; deadlock entre dos tablas; y una lectura sucia evitada.

### 4. Comparaciones experimentales

```bash
go run ./dbms/cli bench files   -n 1000,10000,100000
go run ./dbms/cli bench indexes -n 1000,10000,100000
go run ./dbms/cli bench spatial -n 1000,10000,100000 -pg-socket /ruta/al/socket
go run ./dbms/cli bench charts
```

Los resultados quedan en `resultados/` como CSV y gráficas SVG.

## Ejemplos de SQL

```sql
-- Tablas e índices
CREATE TABLE alumno (codigo INT PRIMARY KEY, nombre VARCHAR(40), ciclo INT);  -- heap + B+ no agrupado
CREATE CLUSTERED TABLE curso (id INT PRIMARY KEY, titulo VARCHAR(40));        -- secuencial + B+ agrupado
CREATE INDEX hash_ciclo ON alumno (ciclo);                                    -- hash extensible
CREATE TABLE matricula (id INT PRIMARY KEY, codigo INT, curso VARCHAR(30));

-- Consultas
SELECT * FROM alumno WHERE codigo >= 1 AND codigo <= 10;          -- rango con el B+
SELECT ciclo, COUNT(*) FROM alumno GROUP BY ciclo;                -- external hashing
SELECT * FROM alumno ORDER BY ciclo DESC LIMIT 5;                 -- external sorting
SELECT a.nombre, m.curso FROM alumno a JOIN matricula m ON a.codigo = m.codigo;

-- Transacciones
BEGIN TRANSACTION;
UPDATE alumno SET ciclo = 7 WHERE codigo = 1;
END TRANSACTION;

-- Espacial
CREATE TABLE tiendas (id INT PRIMARY KEY, nombre VARCHAR(40), ubicacion POINT);
INSERT INTO tiendas VALUES (1, 'Centro', POINT(-12.0464, -77.0428));
SELECT * FROM tiendas WHERE distancia(ubicacion, POINT(-12.0464, -77.0428)) < 5000;     -- metros
SELECT * FROM tiendas ORDER BY distancia(ubicacion, POINT(-12.0464, -77.0428)) LIMIT 10; -- k-NN
SELECT * FROM tiendas WHERE dentro(ubicacion,
  POLYGON(POINT(-12.04, -77.05), POINT(-12.04, -77.03), POINT(-12.06, -77.03))) = true;
SELECT * FROM tiendas WHERE distancia(ubicacion, POINT(-12.04, -77.04), 'euclidean') < 0.05;
```

El tipo de índice se elige por nombre: `pk_<tabla>` es el índice de la clave primaria, `hash_*` crea un hash extensible, `rtree_*` un R-Tree y cualquier otro nombre un B+ no agrupado. Toda tabla necesita `PRIMARY KEY`.

## API del servidor

| Método y ruta | Cuerpo | Respuesta |
| - | - | - |
| `GET /api/tables` | | Tablas con columnas, filas, páginas e índices |
| `POST /api/query` | `{"sql": "..."}` (varias sentencias separadas por `;`) | `Results`: un elemento por sentencia con `Columns`, `Rows`, `Affected`, `Message`, `Plan`, `ElapsedMs`; más `Failed`/`Error` si el script falló a mitad y `ElapsedMs` total |
| `POST /api/tables/{t}/import` | multipart con `file` (CSV) | Filas importadas, omitidas y errores |
| `POST /api/spatial/range` | `{latitude, longitude, radius, metric}` | Puntos dentro del radio (km o grados) |
| `POST /api/spatial/knn` | `{latitude, longitude, k, metric}` | k vecinos ordenados por distancia |
| `POST /api/spatial/polygon` | `{vertices: [{lat, lon}, ...]}` | Puntos dentro del polígono |
| `POST /api/text/search` | `{table, column, query, methods: ["tfidf","bm25"], k, k1, b}` | Términos preprocesados con su `df`, estadísticas del índice SPIMI y, por técnica, los documentos con su score, las palabras encontradas y los pasos del plan |
| `POST /api/vector/search` | `{table, column, row \| vector, methods: ["hnsw","ivf","seq"], metric, k, nlist, nprobe, efSearch}` | Por técnica: vecinos con distancia y similitud, Recall@k frente al secuencial, tiempos de construcción y consulta, y los pasos del plan |
| `POST /api/vector/items` | `{table, column}` | Filas que tienen vector (sin el vector), para elegir la consulta |
| `POST /api/vector/extract` | multipart con `file` (imagen) y `dim` | Vector de la imagen (histograma de color RGB de `dim` = b³ dimensiones) |
| `GET /api/media/{ruta}` | | Archivo de la carpeta `-media` |

Los índices de búsqueda textual y vectorial se construyen en la primera consulta sobre cada columna y se reconstruyen cuando un script o una importación modifica la tabla. `metric` de `/api/vector/search` es `cosine`, `euclidean` o `dot`; IVF no admite `dot`, porque no puede entrenar sus centroides con producto punto.

`metric` de los endpoints espaciales es `haversine` o `euclidean`. El panel de mapa usa la tabla `ubicaciones`, que debe tener `id`, `nombre`, `tipo` y las coordenadas: `latitud` y `longitud`, o una columna `POINT` llamada `ubicacion`.

Un script se parsea completo antes de ejecutar nada: si una sentencia tiene error de sintaxis no corre ninguna y la respuesta es 400. Si falla al ejecutarse (tabla inexistente, clave duplicada, deadlock, …) se detiene ahí, la respuesta es 200 con `Failed`/`Error` apuntando a la sentencia y `Results` con las que sí corrieron. Sin `BEGIN` cada sentencia corre en autocommit; con `BEGIN ... COMMIT` dentro del mismo script todas corren en la misma transacción.

## Pruebas

```bash
go test ./...
go test -race ./dbms/lib/sql ./dbms/lib/lockmanager   # pruebas de concurrencia con el detector de carreras
cd frontend && npm run lint && npm run build
```

## Licencia

MIT. Software libre.
