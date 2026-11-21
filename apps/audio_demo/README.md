# AudioMatch v2

Aplicación de la opción C del proyecto BD2. La base de canciones y los segmentos MFCC de 320 dimensiones se almacenan en DBMS-Go. La búsqueda se ejecuta mediante su API REST, usando IVF/HNSW y similitud coseno. El frontend presenta coincidencias acústicas, no una determinación legal de copyright.

## Instalación sobre la v1

1. Detener Uvicorn (no DBMS-Go). Copiar estos archivos a `apps/audio_demo`, respaldando previamente los archivos originales. Mantener `.venv`, las canciones y `data-audio`.
2. Con DBMS-Go en `127.0.0.1:8081`, ejecutar desde PowerShell:

```powershell
.\.venv\Scripts\python.exe benchmark_features.py cancion1.wav
```

Si aparece `COMPATIBILIDAD OK` y el modo recomendado es `batch`, usar el valor por defecto. Si el benchmark falla o batch es más lento, establecer `$env:AUDIO_FEATURE_MODE = "legacy"` antes de iniciar la aplicación. La extracción batch conserva el orden de los coeficientes y el recorte logarítmico de cada segmento. La prueba local verifica la compatibilidad con la versión de librosa instalada.

```powershell
.\.venv\Scripts\python.exe -m uvicorn app:app --host 127.0.0.1 --port 8001
```

Abrir http://localhost:8001. No ejecutar `init_db` sobre una base existente.

## Pruebas de aceptación

- Canción registrada: cobertura cercana al 100%.
- Fragmento de la canción: coincidencias en todo el fragmento.
- Canción desconocida: 0 coincidencias en las pruebas negativas.
- `mezcla.wav`: coincidencias entre 10 y 40 s (60% de cobertura).
- Comparar IVF y HNSW usando el mismo audio, umbral y valor de k. Los tiempos de k-NN no equivalen al tiempo de la aplicación ni constituyen Recall@K; para Recall@K se requiere una referencia exacta.
- Registrar una segunda canción original desde la interfaz y consultar el catálogo.

Con `cancion1.wav`, `fragmento.wav`, `cancion2.wav` y `mezcla.wav` disponibles (y solo Cancion 1 registrada), ejecutar `python validate_audio.py` para reunir los resultados de las cuatro pruebas. Excluye las ventanas que atraviesan los límites de la mezcla; mide precisión y recall de detección, no Recall@K del índice.

## Tiempos

La extracción batch reduce llamadas repetidas a MFCC. La sesión HTTP reutiliza conexiones cuando el servidor lo permite. El arranque precalienta librosa. La interfaz distingue lectura del audio, extracción, REST (incluye el tiempo del motor), tiempo de búsqueda vectorial y total del detector. `request_ms` incluye además la copia local del archivo y espera del bloqueo, pero no carga por la red del navegador.

## Entorno

`AUDIO_DB_URL`: http://127.0.0.1:8081 por defecto. `AUDIO_FEATURE_MODE`: batch (predeterminado) o legacy. `AUDIO_EF_SEARCH`: 128 por defecto, ajustable para HNSW. El umbral inicial es 0.95; debe validarse con varios archivos positivos y negativos.
