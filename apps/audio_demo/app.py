from contextlib import asynccontextmanager
from pathlib import Path
from tempfile import TemporaryDirectory
from threading import Lock
from time import perf_counter
from dbms_client import search_methods

from fastapi import FastAPI, UploadFile, Form, HTTPException
from fastapi.responses import FileResponse

from detector import register_track, detect_audio, get_rows
from features import warmup

BASE_DIR = Path(__file__).resolve().parent
db_lock = Lock()
MAX_BYTES = 100 * 1024 * 1024


@asynccontextmanager
async def lifespan(app):
    print("AudioMatch: preparando MFCC...", flush=True)
    warmup()

    tracks = get_rows("SELECT id FROM audio_tracks;")
    print(f"Canciones registradas: {len(tracks)}", flush=True)

    if tracks:
        probe = [1.0] + [0.0] * 319

        for method in ("ivf", "hnsw"):
            result = search_methods(probe, [method])[method]

            print(
                f"{method.upper()} listo | "
                f"Construccion: {result['buildMs']:.2f} ms",
                flush=True
            )

    print("AudioMatch listo.", flush=True)
    yield

app = FastAPI(title="AudioMatch", version="2.0", lifespan=lifespan)


@app.get("/")
def home():
    return FileResponse(BASE_DIR / "index.html")


@app.get("/api/tracks")
def tracks():
    try:
        with db_lock:
            rows = get_rows("SELECT * FROM audio_tracks;")
        return [{"id": row[0], "title": row[1], "artist": row[2],
                 "duration": int(row[3]) / 1000} for row in rows]
    except Exception as error:
        raise HTTPException(503, detail=str(error)) from error


def save_audio(file, folder):
    suffix = Path(file.filename or "").suffix.lower()
    if suffix not in {".wav", ".mp3", ".flac", ".ogg"}:
        raise ValueError("Formato no soportado. Usa WAV, MP3, FLAC u OGG.")
    path = Path(folder) / ("audio" + suffix)
    size = 0
    with path.open("wb") as output:
        while chunk := file.file.read(1024 * 1024):
            size += len(chunk)
            if size > MAX_BYTES:
                raise ValueError("El archivo supera los 100 MB permitidos")
            output.write(chunk)
    if not size:
        raise ValueError("Archivo vacio")
    return path


@app.post("/api/register")
def register(file: UploadFile, title: str = Form(...), artist: str = Form(...)):
    try:
        with TemporaryDirectory() as folder:
            path = save_audio(file, folder)
            with db_lock:
                result = register_track(str(path), title, artist)
        return {"message": "Cancion registrada correctamente", **result}
    except Exception as error:
        raise HTTPException(400, detail=str(error)) from error
    finally:
        file.file.close()


@app.post("/api/analyze")
def analyze(file: UploadFile, method: str = Form("ivf"), threshold: float = Form(0.98), k: int = Form(5)):
    try:
        start = perf_counter()
        with TemporaryDirectory() as folder:
            path = save_audio(file, folder)
            with db_lock:
                result = detect_audio(str(path), method, threshold, k)
        result["request_ms"] = (perf_counter() - start) * 1000
        return result
    except Exception as error:
        raise HTTPException(400, detail=str(error)) from error
    finally:
        file.file.close()
