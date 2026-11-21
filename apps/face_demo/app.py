"""Demo académica local: únicamente participantes que aceptaron el registro."""
import os
import hashlib
import threading
import time
from pathlib import Path

from contextlib import asynccontextmanager

from fastapi import FastAPI, File, Form, HTTPException, UploadFile
from fastapi.responses import FileResponse, Response
from fastapi.staticfiles import StaticFiles

from db import DBMS, DBError
from model import FaceModel, MAX_IMAGE, MAX_UPLOAD, normalize_upload


ROOT = Path(__file__).resolve().parent


@asynccontextmanager
async def lifespan(app):
    db.setup()

    stats = db.stats()
    print(f'Base de datos: {stats}', flush=True)

    print('Cargando FaceNet...', flush=True)
    model.load()

    if stats['samples'] > 0:
        probe = [1.0] + [0.0] * 511

        for method in ('ivf', 'hnsw'):
            result = db.nearest(probe, method=method)
            print(
                f'{method.upper()} listo | '
                f'Construccion: {result["build_ms"]:.2f} ms',
                flush=True
            )

    print('FaceLab listo.', flush=True)
    yield


app = FastAPI(
    title='FaceLab · DBMS-Go',
    lifespan=lifespan
)

app.mount('/static', StaticFiles(directory=ROOT / 'static'), name='static')
db = DBMS()
model = FaceModel()
enroll_lock = threading.Lock()
# Umbral provisional seleccionado mediante calibración LFW (4 muestras/identidad).
THRESHOLD = float(os.getenv('FACE_THRESHOLD', '0.70'))
if not 0 <= THRESHOLD <= 1:
    raise ValueError('FACE_THRESHOLD debe estar entre 0 y 1.')
RECOMMENDED_SAMPLES = 4


class PersonCache:
    def __init__(self):
        self.client = None
        self.ttl = int(os.getenv('FACE_CACHE_TTL', '30'))
        if self.ttl < 1:
            raise ValueError('FACE_CACHE_TTL debe ser positivo.')
        namespace = os.getenv('FACE_CACHE_NAMESPACE', db.base)
        self.prefix = 'face-demo:v5:' + hashlib.sha256(namespace.encode()).hexdigest()[:24]
        self.lock = threading.Lock()
        self.counters = dict.fromkeys(('hit', 'miss', 'unavailable', 'disabled', 'skipped'), 0)
        if os.getenv('FACE_REDIS_URL'):
            import redis
            self.client = redis.Redis.from_url(os.environ['FACE_REDIS_URL'], decode_responses=True,
                                               socket_connect_timeout=.1, socket_timeout=.1)

    def get(self, person_id):
        key = f'{self.prefix}:person:{person_id}'
        if self.client is None:
            return db.person(person_id), 'disabled'
        try:
            value = self.client.get(key)
        except Exception:
            return db.person(person_id), 'unavailable'
        if value is not None:
            return value, 'hit'
        value = db.person(person_id)
        if value is not None:
            try:
                self.client.setex(key, self.ttl, value)
            except Exception:
                return value, 'unavailable'
        return value, 'miss'

    def record(self, state):
        with self.lock:
            self.counters[state] += 1

    def metrics(self):
        with self.lock:
            counts = self.counters.copy()
        attempts = counts['hit'] + counts['miss']
        return {**counts, 'hit_rate': counts['hit'] / attempts if attempts else None, 'ttl_seconds': self.ttl}

    def connection(self):
        if self.client is None:
            return 'disabled'
        try:
            return 'online' if self.client.ping() else 'unavailable'
        except Exception:
            return 'unavailable'


cache = PersonCache()


def image_embedding(photo):
    start = time.perf_counter()
    vector, face_box = model.embed_with_box(photo.file.read(MAX_IMAGE + 1))
    return vector, face_box, (time.perf_counter()-start)*1000


def error_response(error):
    if isinstance(error, ValueError):
        raise HTTPException(422, str(error)) from error
    if isinstance(error, DBError):
        raise HTTPException(502, str(error)) from error
    raise error


@app.get('/')
def page():
    return FileResponse(ROOT/'static'/'index.html')


@app.get('/api/status')
def status():
    try:
        with enroll_lock:
            db.setup()
        return {**db.stats(), 'threshold': THRESHOLD, 'model': 'FaceNet / MTCNN', 'dim': 512,
                'recommended_samples': RECOMMENDED_SAMPLES, 'cache_connection': cache.connection(), 'cache_stats': cache.metrics()}
    except DBError as e:
        error_response(e)


@app.post('/api/normalize')
def normalize(photo: UploadFile = File(...)):
    """Decode a photograph locally when the phone browser cannot open it."""
    try:
        content = normalize_upload(photo.file.read(MAX_UPLOAD + 1))
        return Response(content, media_type='image/jpeg', headers={'Cache-Control': 'no-store'})
    except ValueError as e:
        error_response(e)


@app.post('/api/enroll')
def enroll(photo: UploadFile = File(...), alias: str = Form(...), consent: bool = Form(...)):
    if not consent:
        raise HTTPException(422, 'Se requiere consentimiento del participante.')
    try:
        vector, face_box, embedding_ms = image_embedding(photo)
        with enroll_lock:
            db.setup()
            person_id = db.enroll(alias, vector)
            try:
                person_samples = db.sample_count(person_id)
            except DBError:
                person_samples = None  # La muestra ya se guardó: no provocar un registro duplicado al reintentar.
        return {'person_id': person_id, 'person_samples': person_samples, 'recommended_samples': RECOMMENDED_SAMPLES,
                'embedding_ms': embedding_ms, 'face_box': face_box, **db.stats()}
    except (DBError, ValueError) as e:
        error_response(e)


@app.post('/api/recognize')
def recognize(photo: UploadFile = File(...), method: str = Form('ivf'), consent: bool = Form(...)):
    if not consent:
        raise HTTPException(422, 'Usa únicamente imágenes de participantes que consintieron.')
    start = time.perf_counter()
    try:
        if method not in ('hnsw', 'ivf', 'seq', 'ivf_fallback'):
            raise ValueError('Método no válido.')
        vector, face_box, embedding_ms = image_embedding(photo)
        if db.stats()['samples'] == 0:
            cache.record('skipped')
            return {'matched': False, 'reason': 'No hay muestras registradas.', 'embedding_ms': embedding_ms,
                    'face_box': face_box, 'threshold': THRESHOLD, 'cache_state': 'skipped', 'cache_hit': None,
                    'cache_stats': cache.metrics(), 'requested_method': method, 'fallback_used': False,
                    'total_ms': (time.perf_counter()-start)*1000}
        result = db.nearest(vector, 'ivf' if method == 'ivf_fallback' else method)
        hits = result.pop('hits')
        top = hits[0] if hits else None
        fallback_used = False
        if method == 'ivf_fallback' and (top is None or top['score'] < THRESHOLD):
            exact = db.nearest(vector, 'seq')
            exact_hits = exact.pop('hits')
            candidate = exact_hits[0] if exact_hits else None
            if candidate is not None and (top is None or candidate['score'] > top['score']):
                top = candidate
            for field in ('search_ms', 'http_ms', 'build_ms'):
                result[field] = result.get(field, 0) + exact.get(field, 0)
            result['method'] = 'ivf+seq'
            fallback_used = True
        score = top['score'] if top else None
        alias, cache_state, metadata_ms = (None, 'skipped', None)
        if top and score >= THRESHOLD:
            metadata_start = time.perf_counter()
            alias, cache_state = cache.get(top['person_id'])
            metadata_ms = (time.perf_counter()-metadata_start)*1000
        cache.record(cache_state)
        hit = True if cache_state == 'hit' else False if cache_state == 'miss' else None
        return {**result, 'matched': alias is not None, 'alias': alias, 'score': score,
                'threshold': THRESHOLD, 'cache_hit': hit, 'cache_state': cache_state, 'cache_stats': cache.metrics(),
                'metadata_ms': metadata_ms, 'requested_method': method, 'fallback_used': fallback_used,
                'embedding_ms': embedding_ms, 'face_box': face_box,
                'total_ms': (time.perf_counter()-start)*1000}
    except (DBError, ValueError) as e:
        error_response(e)
