// Explicación de cada operador del plan (Kind del motor, ver
// dbms/lib/sql/engine.go): qué hace y por qué el motor lo usa.
export const PLAN_INFO = {
  "seq-scan": {
    title: "Recorrido secuencial",
    what: "Lee todas las páginas del archivo de la tabla, fila por fila.",
    why: "Se usa cuando el WHERE no filtra por una columna con índice (o no hay WHERE): no queda otra que mirar todas las filas.",
  },
  "index-seek": {
    title: "Búsqueda en índice",
    what: "Baja por el índice (B+ o hash) directo a la clave o al rango buscado y lee solo esas filas.",
    why: "El WHERE compara la clave del índice con = o con un rango (>=, <=), así que no hace falta leer toda la tabla.",
  },
  "index-scan": {
    title: "Recorrido de índice",
    what: "Recorre las hojas del índice en orden de la clave.",
    why: "Sirve para devolver las filas ya ordenadas por la clave sin un ordenamiento aparte.",
  },
  filter: {
    title: "σ Selección",
    what: "Evalúa la condición del WHERE en cada fila que le llega y deja pasar solo las que cumplen.",
    why: "Va encima del acceso cuando la condición no se resolvió completa con el índice (por ejemplo, distancia() < r).",
  },
  project: {
    title: "π Proyección",
    what: "Se queda solo con las columnas pedidas en el SELECT y calcula expresiones como distancia().",
    why: "Es la raíz de casi todo SELECT: arma la forma final del resultado.",
  },
  "external-sort": {
    title: "τ Ordenamiento externo",
    what: "Ordena las filas para el ORDER BY. Si no caben en el buffer, ordena por partes (runs) en disco y luego las mezcla.",
    why: "Hace falta porque el ORDER BY pide un orden distinto al que sale del acceso a la tabla.",
  },
  "group-by": {
    title: "γ Agrupación",
    what: "Junta las filas con el mismo valor en las columnas del GROUP BY y calcula COUNT, SUM, AVG, MIN o MAX.",
    why: "Aparece cuando la consulta tiene GROUP BY o funciones de agregación.",
  },
  join: {
    title: "⋈ Join",
    what: "Combina las filas de las dos entradas que cumplen la condición del ON.",
    why: "La entrada izquierda y la derecha son los dos subárboles; cada una se resuelve con su mejor acceso.",
  },
  limit: {
    title: "Límite",
    what: "Corta el resultado después de N filas.",
    why: "Viene del LIMIT. Con ORDER BY distancia(...) es la forma de pedir los k vecinos más cercanos.",
  },
  insert: {
    title: "Inserción",
    what: "Escribe las filas nuevas en el archivo de la tabla y agrega sus claves a los índices.",
    why: "Es el plan de un INSERT.",
  },
  update: {
    title: "Actualización",
    what: "Cambia los valores de las filas que cumplen el WHERE y actualiza los índices afectados.",
    why: "Es el plan de un UPDATE.",
  },
  delete: {
    title: "Eliminación",
    what: "Borra las filas que cumplen el WHERE y quita sus claves de los índices.",
    why: "Es el plan de un DELETE.",
  },
  begin: {
    title: "BEGIN",
    what: "Abre una transacción: los cambios siguientes quedan pendientes hasta confirmarla.",
    why: "Permite agrupar varias sentencias como una sola unidad (todo o nada).",
  },
  commit: {
    title: "COMMIT",
    what: "Confirma la transacción: los cambios quedan escritos en el WAL y se liberan los locks.",
    why: "Cierra la transacción guardando sus cambios.",
  },
  rollback: {
    title: "ROLLBACK",
    what: "Deshace los cambios de la transacción y libera los locks.",
    why: "Cierra la transacción descartando sus cambios.",
  },
  lock: {
    title: "Lock (2PL)",
    what: "Toma los locks de la tabla con el protocolo de bloqueo en dos fases.",
    why: "Aísla la transacción de las demás: nadie más modifica lo que esta está leyendo o escribiendo.",
  },
};

// Kind de un paso de la lista de texto: "[index-seek] ..." → "index-seek".
export function kindOfStep(step) {
  const m = /^\[([a-z-]+)\]/.exec(step);
  return m ? m[1] : null;
}
