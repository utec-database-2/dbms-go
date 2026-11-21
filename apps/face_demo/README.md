# FaceLab — Parte 5 (Opción D)

Aplicación web adaptable a móviles, diseñada como demostración local de reconocimiento de participantes registrados voluntariamente. No autentica usuarios ni está concebida para vigilancia. La interfaz permite registrar una muestra, subir una fotografía o usar la cámara, reconocer en modo continuo y visualizar las latencias reales. El almacenamiento y la búsqueda k-NN ocurren en **DBMS-Go**, no en Python ni en Redis.

## Arquitectura

```
Web móvil / cámara -> FastAPI -> MTCNN -> FaceNet (512 float32)
                            |       -> REST POST /api/vector/search
                            |                 -> HNSW / IVF en DBMS-Go
                            |                 -> person_id
                            +-> Redis opcional / REST POST /api/query -> alias
```

Modalidades: relacional (`face_persons`, B+ agrupado, alias) y multimedia (`face_samples`, embeddings faciales). Se permite registrar más de una muestra por alias. Las tablas se crean al consultar el estado de la app. No se guardan fotografías crudas. Los índices del endpoint vectorial se construyen la primera vez que se consulta la tabla y se mantienen en la caché de índices de Go (se invalidan tras escrituras). `skipRecall: true` evita el recorrido secuencial destinado a las comparaciones experimentales cuando solamente se usa HNSW o IVF.

## Arranque en Windows

Requiere Go 1.26.6 (según `go.mod`) y Python 3.11 de 64 bits. Desde la raíz del repositorio, abre dos terminales PowerShell:

Terminal 1:

```powershell
go run ./dbms/cmd/server -addr 127.0.0.1:8080 -dir ./data-face -media ./datos_demo
```

Terminal 2:

```powershell
cd apps/face_demo
py -3.11 -m venv .venv
.\.venv\Scripts\python.exe -m pip install -r requirements.txt
.\.venv\Scripts\python.exe -m uvicorn app:app --host 127.0.0.1 --port 8000
```

Abre `http://localhost:8000`. La primera inferencia descarga automáticamente los pesos preentrenados de FaceNet (aprox. 107 MB). Calienta el modelo con una foto antes de medir. Para abrir con cámara desde un teléfono, usa HTTPS mediante un certificado confiable y una red privada; `http://<IP LAN>` normalmente no tiene permiso de cámara. No expongas el API SQL del DBMS a internet. Esta versión es una interfaz web móvil, no un APK nativo.

Linux/macOS: `python3.11 -m venv .venv`, `.venv/bin/python -m pip install -r requirements.txt`, `.venv/bin/python -m uvicorn app:app --port 8000`.

## Flujo de demostración

1. Activa la cámara o sube una foto de un participante voluntario.
2. Escribe un alias y guarda la muestra solo si la persona autoriza el registro; el botón confirma esa condición sin diálogo adicional. Registra 3–5 muestras propias con iluminación y ángulos diferentes.
3. Captura una foto distinta. Ejecuta HNSW y comprueba la coincidencia y la latencia.
4. Prueba una foto de otro participante que haya aceptado participar, sin muestras registradas, para probar el rechazo. No uses fotografías de terceros sin permiso.
5. Compara HNSW, IVF y secuencial. `k-NN` mide solo la consulta del índice, `API REST` incluye red, serialización y construcción del índice en la consulta fría, `Proceso total` incluye inferencia y lectura de metadata.

El umbral provisional (`FACE_THRESHOLD`, por defecto 0.70) se seleccionó con IVF y cuatro muestras por identidad en LFW. Debe validarse también en la población de demostración. IVF es el método predeterminado; el modo opcional IVF + respaldo exacto consulta SEQ únicamente si IVF no supera el umbral y suma los tiempos de ambas búsquedas. No sustituye las mediciones independientes de cada índice. La similitud del coseno no es una probabilidad de identidad.

Las fotos seleccionadas se convierten localmente a JPEG (lado máximo 1600 píxeles; límite de entrada 15 MB). Si el navegador no puede abrir el archivo, `/api/normalize` intenta decodificarlo con Pillow, con un límite adicional de 32 megapíxeles. No todos los formatos propietarios tienen soporte; una imagen ilegible requiere otra copia. La foto preparada no se almacena en Redis ni en el DBMS. El escaneo de una persona desconocida es una respuesta válida (HTTP 200), no un error de imagen.

## Dataset para evaluación

Para usar LFW localmente, verifica los términos del dataset y prepara una carpeta con subcarpetas por identidad. Inicia una instancia de DBMS en otro puerto (`8081`) con un directorio de datos separado (`./data-face-eval`) para no mezclar el dataset con los participantes de la demostración. La importación no conserva nombres de las carpetas; asigna alias `Eval-00001`, etc.

```powershell
$env:FACE_DB_URL = "http://127.0.0.1:8081"
.\.venv\Scripts\python.exe import_dataset.py "C:\datasets\lfw" --persons 100 --samples 2 --accept-terms
```

Usa muestras distintas para registro y evaluación; no evalúes consultando exactamente la imagen almacenada. El importador no descarga ni incluye el dataset. Registra el conteo real de personas y muestras y documenta las condiciones de uso.

## Redis (opcional)

Arranca Redis localmente, configura `$env:FACE_REDIS_URL = "redis://127.0.0.1:6379/0"` antes de iniciar FastAPI. Cachea el alias de una persona tras una coincidencia, por 30 segundos (`FACE_CACHE_TTL` configurable), sin saltarse la consulta vectorial. `FACE_CACHE_NAMESPACE` permite separar bases que reutilizan la misma URL; por defecto se usa la URL de DBMS. Si cambias de base o haces cambios externos de perfiles, usa un namespace diferente o espera que expire la caché. No cachea fotografías ni embeddings. Si Redis falla, consulta directamente al DBMS. El panel distingue HIT, MISS, no aplica (sin coincidencia), no disponible y desactivado; la tasa de aciertos es HIT/(HIT+MISS) de consultas exitosas a la caché. Los contadores son del proceso FastAPI desde su arranque, no estadísticas globales de Redis. La latencia de alias incluye Redis o el acceso de respaldo a DBMS cuando corresponde.

## Pruebas

```powershell
go test ./dbms/cmd/server
cd apps/face_demo
.\.venv\Scripts\python.exe -m unittest -v test_app
```

Las pruebas unitarias de Python simulan el modelo y el API Go; comprueban los contratos y el tratamiento de las coincidencias. Para la validación de extremo a extremo se requiere iniciar ambos servidores y utilizar imágenes consentidas reales. No se afirma aún que la búsqueda cumpla 100 ms: deben medirse el primer acceso, el estado caliente y percentiles (p50, p95) con el dataset elegido.

Para medir 100 consultas calientes por método sobre un embedding de evaluación:

```powershell
.\.venv\Scripts\python.exe benchmark.py "C:\fotos\evaluacion.jpg" --iterations 100
```

Este benchmark mide latencia de una consulta repetida, no calidad de reconocimiento. La precisión y la tasa de falsos positivos deben medirse con múltiples fotos de evaluación no usadas en el registro.

Modelo: [facenet-pytorch](https://github.com/timesler/facenet-pytorch), InceptionResnetV1 entrenado con VGGFace2.
