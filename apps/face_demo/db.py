"""Cliente REST del DBMS-Go. No guarda perfiles ni hace k-NN localmente."""
import json
import math
import os
import secrets
import time
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

DIM = 512
PERSONS = "face_persons"
SAMPLES = "face_samples"
SCHEMA = {
    PERSONS: "CREATE TABLE face_persons CLUSTERED BY (id) (id INT PRIMARY KEY, alias VARCHAR(80));",
    SAMPLES: "CREATE TABLE face_samples CLUSTERED BY (id) (id INT PRIMARY KEY, person_id INT, embedding VECTOR(512));",
}


class DBError(RuntimeError):
    pass


def clean_alias(alias):
    alias = alias.strip()
    if not alias or len(alias.encode('utf-8')) > 80 or not all(c.isalnum() or c in ' -_' for c in alias):
        raise ValueError('Usa un alias de hasta 80 bytes: letras, números, espacios, guiones.')
    return alias


def check_vector(vector):
    if len(vector) != DIM or not all(math.isfinite(v) for v in vector):
        raise ValueError(f'Se requiere un vector finito de {DIM} componentes.')
    norm = math.sqrt(sum(v*v for v in vector))
    if norm <= 1e-12:
        raise ValueError('Embedding nulo.')
    return [float(v/norm) for v in vector]


def sql_vector(vector):
    # El lexer SQL propio no admite exponentes; usa decimales fijos.
    return 'VECTOR(' + ', '.join(f'{v:.9f}' for v in check_vector(vector)) + ')'


class DBMS:
    def __init__(self, base=None):
        self.base = (base or os.getenv('FACE_DB_URL', 'http://127.0.0.1:8080')).rstrip('/')

    def request(self, path, body=None):
        req = Request(self.base + path, data=None if body is None else json.dumps(body).encode(),
                      headers={'Content-Type': 'application/json'})
        try:
            with urlopen(req, timeout=60) as response:
                return json.load(response)
        except HTTPError as e:
            try:
                detail = json.load(e).get('error', str(e))
            except (ValueError, AttributeError):
                detail = str(e)
            raise DBError(detail) from e
        except (URLError, TimeoutError) as e:
            raise DBError('No se pudo contactar con DBMS-Go. Revisa FACE_DB_URL y el servidor.') from e

    def query(self, sql):
        result = self.request('/api/query', {'sql': sql})
        if result.get('Error') or result.get('Failed', -1) >= 0:
            raise DBError(result.get('Error') or 'Falló el script SQL')
        return result['Results']

    def tables(self):
        return {t['Name'].lower(): t for t in self.request('/api/tables')}

    def setup(self):
        tables = self.tables()
        for name, ddl in SCHEMA.items():
            if name not in tables:
                self.query(ddl)

    def stats(self):
        tables = self.tables()
        return {'persons': tables.get(PERSONS, {}).get('Records', 0),
                'samples': tables.get(SAMPLES, {}).get('Records', 0)}

    def enroll(self, alias, vector):
        alias = clean_alias(alias)
        literal = sql_vector(vector)
        rows = self.query(f"SELECT id FROM {PERSONS} WHERE alias = '{alias}';")[0]['Rows']
        person_id = int(rows[0][0]) if rows else secrets.randbelow(2**31-1)+1
        sample_id = secrets.randbelow(2**31-1)+1
        statements = ['BEGIN TRANSACTION;']
        if not rows:
            statements.append(f"INSERT INTO {PERSONS} VALUES ({person_id}, '{alias}');")
        statements.append(f'INSERT INTO {SAMPLES} VALUES ({sample_id}, {person_id}, {literal});')
        statements.append('END TRANSACTION;')
        self.query('\n'.join(statements))
        return person_id

    def person(self, person_id):
        rows = self.query(f'SELECT alias FROM {PERSONS} WHERE id = {int(person_id)};')[0]['Rows']
        return rows[0][0] if rows else None

    def sample_count(self, person_id):
        rows = self.query(f'SELECT id FROM {SAMPLES} WHERE person_id = {int(person_id)};')[0]['Rows']
        return len(rows or [])

    def nearest(self, vector, method='hnsw', k=5, compare=False):
        if method not in ('hnsw', 'ivf', 'seq'):
            raise ValueError('Método no válido.')
        body = {'table': SAMPLES, 'column': 'embedding', 'vector': check_vector(vector),
                'methods': [method], 'metric': 'cosine', 'k': k, 'efSearch': 64,
                'skipRecall': not compare}
        start = time.perf_counter()
        response = self.request('/api/vector/search', body)
        http_ms = (time.perf_counter()-start)*1000
        result = response['results'][0]
        if result.get('error'):
            raise DBError(result['error'])
        person_col = response['columns'].index('person_id')
        # No reenviar los embeddings devueltos por el API de Parte 4 a la UI.
        hits = [{'person_id': int(h['values'][person_col]), 'score': float(h['score'])}
                for h in result.get('hits', [])]
        return {'hits': hits, 'search_ms': result['elapsedMs'], 'http_ms': http_ms,
                'build_ms': result['buildMs'], 'recall': result.get('recall'), 'method': method}
