import math
import os

import requests

DB_URL = os.getenv("AUDIO_DB_URL", "http://127.0.0.1:8081").rstrip("/")
SESSION = requests.Session()


def _post(path, payload, timeout=300):
    response = SESSION.post(DB_URL + path, json=payload, timeout=timeout)
    response.raise_for_status()
    return response.json()


def query(sql):
    data = _post("/api/query", {"sql": sql})
    if data.get("Error") or data.get("Failed", -1) != -1:
        raise RuntimeError(data.get("Error") or str(data))
    return data.get("Results", data)


def init_db():
    # Run only against a new, empty AudioMatch database.
    query("""CREATE TABLE audio_tracks CLUSTERED BY (id) (
        id INT PRIMARY KEY, title VARCHAR(120), artist VARCHAR(100), duration_ms INT);""")
    query("""CREATE TABLE audio_segments CLUSTERED BY (id) (
        id INT PRIMARY KEY, track_id INT, start_ms INT, end_ms INT, embedding VECTOR(320));""")


def vector_literal(vector):
    if len(vector) != 320 or not all(math.isfinite(v) for v in vector):
        raise ValueError("Vector invalido")
    return "VECTOR(" + ",".join(f"{v:.9f}" for v in vector) + ")"


def insert_segment(segment_id, track_id, segment):
    return query(f"INSERT INTO audio_segments VALUES ({segment_id}, {track_id}, "
                 f"{round(segment['start'] * 1000)}, {round(segment['end'] * 1000)}, "
                 f"{vector_literal(segment['vector'])});")


def search_methods(vector, methods, k=5, ef_search=128):
    data = _post("/api/vector/search", {
    "table": "audio_segments",
    "column": "embedding",
    "vector": vector,
    "methods": methods,
    "metric": "cosine",
    "k": k,
    "nlist": 64,
    "nprobe": 24,
    "efSearch": ef_search,
    "skipRecall": True,
})
    results = data["results"]
    if len(results) != len(methods):
        raise RuntimeError("Respuesta vectorial incompleta")
    by_method = {}
    for requested, result in zip(methods, results):
        method = str(result.get("method", requested)).lower()
        if method not in methods:
            method = requested
        if result.get("error") or result.get("Error"):
            raise RuntimeError(result.get("error") or result.get("Error"))
        by_method[method] = result
    if set(by_method) != set(methods):
        raise RuntimeError("Metodos de busqueda inesperados")
    return by_method


def search_vector(vector, method="ivf", k=5):
    return search_methods(vector, [method], k)[method]
