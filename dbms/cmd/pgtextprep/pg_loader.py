"""Cargar y validar PostgreSQL GIN:

Ejemplos:
  py -B dbms/cmd/pgtextprep/pg_loader.py check
  py -B dbms/cmd/pgtextprep/pg_loader.py load --input-dir "$env:TEMP\bd2_pgprep_12"
  py -B dbms/cmd/pgtextprep/pg_loader.py search --input-dir "$env:TEMP\bd2_pgprep_12" --query-id q1
  py -B dbms/cmd/pgtextprep/pg_loader.py explain --input-dir "$env:TEMP\bd2_pgprep_12" --query-id q1

"""
from __future__ import annotations

import argparse
import csv
import getpass
import json
import os
import sys
import time
from pathlib import Path

SCHEMA = "bd2_textbench"
TABLE = "documents"
GIN_INDEX = "documents_search_vector_gin_idx"


def read_manifest(folder: Path) -> dict:
    with (folder / "manifest.json").open("r", encoding="utf-8") as f:
        obj = json.load(f)
    if (not isinstance(obj, dict) or obj.get("schema_version") != "pgtextprep-v1"
            or not isinstance(obj.get("documents"), int) or obj["documents"] <= 0):
        raise ValueError("manifest.json no es un export válido de pgtextprep-v1")
    return obj


def iter_document_rows(path: Path):
    with path.open("r", encoding="utf-8-sig", newline="") as f:
        reader = csv.DictReader(f)
        if reader.fieldnames != ["doc_id", "external_id", "normalized_text"]:
            raise ValueError(f"Cabecera incorrecta en {path}: {reader.fieldnames}")
        for position, row in enumerate(reader):
            if None in row or any(v is None for v in row.values()):
                raise ValueError(f"CSV inválido en fila {position + 2}")
            docid = int(row["doc_id"])
            ext = row["external_id"]
            if docid != position or not ext:
                raise ValueError(f"DocID o external_id inválido en fila {position + 2}")
            yield docid, ext, row["normalized_text"]


def lookup_query(path: Path, query_id: str) -> dict:
    with path.open("r", encoding="utf-8-sig", newline="") as f:
        reader = csv.DictReader(f)
        expected = ["query_id", "raw_text", "normalized_tokens", "or_tsquery", "raw_words"]
        if reader.fieldnames != expected:
            raise ValueError(f"Cabecera incorrecta en {path}: {reader.fieldnames}")
        found = None
        for row in reader:
            if row["query_id"] == query_id:
                if found is not None:
                    raise ValueError(f"Consulta duplicada: {query_id}")
                found = row
    if found is None:
        raise ValueError(f"No existe query_id={query_id} en {path}")
    return found


def sql_objects():
    try:
        from psycopg import sql
    except ImportError as exc:
        raise RuntimeError("Instala Psycopg 3: py -m pip install 'psycopg[binary]'") from exc
    return sql, sql.Identifier(SCHEMA, TABLE), sql.Identifier(GIN_INDEX)


def connection():
    try:
        import psycopg
    except ImportError as exc:
        raise RuntimeError("Instala Psycopg 3: py -m pip install 'psycopg[binary]'") from exc
    password = os.environ.get("PGPASSWORD")
    if password is None:
        password = getpass.getpass("Contraseña de PostgreSQL (usuario postgres): ")
    return psycopg.connect(host="127.0.0.1", port=5432, dbname="postgres", user="postgres",
                           password=password, connect_timeout=8, application_name="bd2_pgtextprep")


def check(conn):
    row = conn.execute("SELECT current_database(), current_user, current_setting('server_version')").fetchone()
    print(f"Conexión OK: base={row[0]}, usuario={row[1]}, versión={row[2]}")


def load(conn, folder: Path, reset: bool):
    """Crea una tabla de experimento y carga docs.csv. Transacción atómica."""
    manifest = read_manifest(folder)
    docs = folder / "docs.csv"
    if not docs.is_file():
        raise FileNotFoundError(docs)
    sql, table, index = sql_objects()
    with conn.cursor() as cur:
        # El esquema es dedicado, pero la base sigue siendo 'postgres'.
        cur.execute(sql.SQL("CREATE SCHEMA IF NOT EXISTS {}").format(sql.Identifier(SCHEMA)))
        exists = cur.execute("SELECT to_regclass(%s) IS NOT NULL", (f"{SCHEMA}.{TABLE}",)).fetchone()[0]
        if exists and not reset:
            raise RuntimeError("La tabla del experimento ya existe. Si deseas reconstruirla, "
                               "ejecuta 'load ... --reset' (reemplaza SOLO bd2_textbench.documents).")
        if exists:
            cur.execute(sql.SQL("DROP TABLE {}").format(table))
        cur.execute(sql.SQL("""
            CREATE TABLE {} (
                doc_id integer PRIMARY KEY CHECK (doc_id >= 0),
                external_id text NOT NULL UNIQUE,
                normalized_text text NOT NULL,
                search_vector tsvector GENERATED ALWAYS AS
                   (to_tsvector('simple'::regconfig, normalized_text)) STORED
            )
        """).format(table))
        count = 0
        start = time.perf_counter()
        with cur.copy(sql.SQL("COPY {} (doc_id, external_id, normalized_text) FROM STDIN").format(table)) as cp:
            for row in iter_document_rows(docs):
                cp.write_row(row)
                count += 1
        load_ms = (time.perf_counter() - start) * 1000.0
        if count != manifest["documents"]:
            raise ValueError(f"CSV contiene {count} docs; manifest espera {manifest['documents']}")
        actual = cur.execute(sql.SQL("SELECT count(*) FROM {}").format(table)).fetchone()[0]
        if actual != count:
            raise RuntimeError(f"PostgreSQL reporta {actual} documentos; esperados {count}")
        # El intervalo medido representa solamente CREATE INDEX, sin COPY ni ANALYZE.
        start = time.perf_counter()
        cur.execute(sql.SQL("CREATE INDEX {} ON {} USING GIN (search_vector)").format(index, table))
        gin_build_ms = (time.perf_counter() - start) * 1000.0
        cur.execute(sql.SQL("ANALYZE {}").format(table))
        mismatch = cur.execute(sql.SQL("""
            SELECT count(*) FROM {} AS d
            WHERE ARRAY(SELECT unnest(tsvector_to_array(d.search_vector)) ORDER BY 1)
                  IS DISTINCT FROM
                  ARRAY(SELECT DISTINCT term FROM unnest(string_to_array(d.normalized_text, ' ')) AS term
                        WHERE term <> '' ORDER BY term)
        """).format(table)).fetchone()[0]
        if mismatch:
            raise RuntimeError(f"¡Se encontraron {mismatch} documentos con vocabulario diferente en PostgreSQL!")
        relation = f"{SCHEMA}.{TABLE}"
        index_name = f"{SCHEMA}.{GIN_INDEX}"
        index_bytes, table_bytes, total_bytes = cur.execute(
            "SELECT pg_relation_size(%s::regclass), pg_relation_size(%s::regclass), "
            "pg_total_relation_size(%s::regclass)", (index_name, relation, relation)
        ).fetchone()
        print(f"Carga OK: {count} documentos | COPY={load_ms:.3f} ms | "
              f"CREATE GIN={gin_build_ms:.3f} ms")
        print(f"GIN={index_bytes} bytes | tabla={table_bytes} bytes | total={total_bytes} bytes")
        print(f"Vocabulario: {mismatch} discrepancias (esperado: 0)")
    # conn context manager hace commit al salir de main() si no hubo errores.


def search(conn, folder: Path, query_id: str, top_k: int, explain: bool):
    if top_k < 0:
        raise ValueError("top-k debe ser >= 0")
    q = lookup_query(folder / "queries.csv", query_id)
    expression = q["or_tsquery"]
    if not expression or top_k == 0:
        print("Sin resultados: consulta normalizada vacía o top-k=0")
        return
    sql, table, _ = sql_objects()
    query = sql.SQL("""
        SELECT doc_id, external_id,
               ts_rank_cd(search_vector, to_tsquery('simple'::regconfig, %s)) AS score
        FROM {}
        WHERE search_vector @@ to_tsquery('simple'::regconfig, %s)
        ORDER BY score DESC, doc_id ASC
        LIMIT %s
    """).format(table)
    if explain:
        query = sql.SQL("EXPLAIN (ANALYZE, BUFFERS) ") + query
    with conn.cursor() as cur:
        cur.execute(query, (expression, expression, top_k))
        rows = cur.fetchall()
    if explain:
        print("\n".join(str(r[0]) for r in rows))
    else:
        print(json.dumps([{"doc_id": r[0], "external_id": r[1], "score": float(r[2])}
                          for r in rows], ensure_ascii=False, indent=2))


def main(argv=None):
    parser = argparse.ArgumentParser(description="PostgreSQL GIN, conexión local con Psycopg 3")
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("check", help="Comprobar conexión a localhost:5432/postgres")
    load_parser = sub.add_parser("load", help="Cargar docs.csv y construir GIN en esquema propio")
    load_parser.add_argument("--input-dir", type=Path, required=True)
    load_parser.add_argument("--reset", action="store_true", help="Reemplazar SOLO la tabla de ensayo")
    for name in ("search", "explain"):
        p = sub.add_parser(name, help=f"Ejecutar {name} con consultas normalizadas")
        p.add_argument("--input-dir", type=Path, required=True)
        p.add_argument("--query-id", required=True)
        p.add_argument("--top-k", type=int, default=10)
    args = parser.parse_args(argv)
    try:
        with connection() as conn:
            if args.command == "check":
                check(conn)
            elif args.command == "load":
                load(conn, args.input_dir, args.reset)
            else:
                search(conn, args.input_dir, args.query_id, args.top_k,
                       explain=(args.command == "explain"))
        return 0
    except Exception as exc:
        print(f"pg_loader: {type(exc).__name__}: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
