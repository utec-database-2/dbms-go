#!/usr/bin/env python3
"""Prepara MIRACL-es (dev + un shard) para textbench/pgtextprep/pgtextbench.

Solo biblioteca estándar. Los datos se almacenan FUERA del repositorio, porque los datos como tal también estan fuera
ya que pesa y por ello no se subió todo - si es necesario lo subo 
"""
from __future__ import annotations

import argparse
import gzip
import hashlib
import json
import os
import random
import shutil
import sys
import tempfile
import unicodedata
import urllib.request
from collections import Counter, defaultdict
from pathlib import Path

CORPUS_URL = (
    "https://huggingface.co/datasets/miracl/miracl-corpus/resolve/main/"
    "miracl-corpus-v1.0-es/docs-0.jsonl.gz"
)
TOPICS_URL = (
    "https://huggingface.co/datasets/miracl/miracl/resolve/main/"
    "miracl-v1.0-es/topics/topics.miracl-v1.0-es-dev.tsv"
)
QRELS_URL = (
    "https://huggingface.co/datasets/miracl/miracl/resolve/main/"
    "miracl-v1.0-es/qrels/qrels.miracl-v1.0-es-dev.tsv"
)
SOURCES = {
    "docs-0.jsonl.gz": CORPUS_URL,
    "topics.miracl-v1.0-es-dev.tsv": TOPICS_URL,
    "qrels.miracl-v1.0-es-dev.tsv": QRELS_URL,
}

# Consultas controladas para latencia, NO con qrels de MIRACL.
PERFORMANCE_QUERIES = {
    "1": [
        "historia", "ciencia", "matemáticas", "música", "geografía",
    ],
    "3": [
        "historia del Perú", "ciencia y tecnología", "arte y cultura",
        "animales de América", "universidades del mundo",
    ],
    "5+": [
        "historia de la ciencia moderna en Europa",
        "investigación científica de las enfermedades infecciosas",
        "desarrollo de la inteligencia artificial en universidades",
        "geografía e historia cultural de América Latina",
        "teoría matemática aplicada a los sistemas informáticos",
    ],
}


def get_source_files(raw_dir: Path, download: bool) -> dict[str, Path]:
    raw_dir.mkdir(parents=True, exist_ok=True)
    out = {}
    for filename, url in SOURCES.items():
        path = raw_dir / filename
        if path.is_file() and path.stat().st_size > 0:
            print(f"Disponible: {path}", file=sys.stderr)
        elif not download:
            raise ValueError(f"Falta {path}; añada --download para obtener MIRACL-es")
        else:
            partial = path.with_name(path.name + ".partial")
            print(f"Descargando {filename} desde Hugging Face...", file=sys.stderr)
            try:
                req = urllib.request.Request(url, headers={"User-Agent": "bd2-miraclprep/1.0"})
                with urllib.request.urlopen(req, timeout=120) as response, partial.open("wb") as output:
                    shutil.copyfileobj(response, output, 1024 * 1024)
                if partial.stat().st_size == 0:
                    raise ValueError(f"Descarga vacía: {url}")
                os.replace(partial, path)
            except Exception:
                partial.unlink(missing_ok=True)
                raise
        out[filename] = path
    return out


def read_topics(path: Path) -> dict[str, str]:
    topics = {}
    with path.open(encoding="utf-8-sig") as f:
        for line_no, line in enumerate(f, 1):
            line = line.rstrip("\r\n")
            if not line:
                continue
            if "\t" not in line:
                raise ValueError(f"topics:{line_no}: se esperaba qid\\tconsulta")
            qid, query = line.split("\t", 1)
            if not qid or not query.strip() or qid in topics:
                raise ValueError(f"topics:{line_no}: qid duplicado/vacío o consulta vacía")
            topics[qid] = query.strip()
    if not topics:
        raise ValueError("El archivo de consultas está vacío")
    return topics


def read_judgments(path: Path) -> dict[str, dict[str, int]]:
    qrels: dict[str, dict[str, int]] = defaultdict(dict)
    with path.open(encoding="utf-8-sig") as f:
        for line_no, line in enumerate(f, 1):
            if not line.strip():
                continue
            fields = line.split()
            if len(fields) != 4:
                raise ValueError(f"qrels:{line_no}: formato TREC requiere 4 columnas")
            qid, _unused, docid, label = fields
            grade = int(label)
            if grade < 0:
                continue  # sin evaluar; jamás lo convertimos a relevante
            if docid in qrels[qid] and qrels[qid][docid] != grade:
                raise ValueError(f"qrels:{line_no}: juicios contradictorios para {qid}/{docid}")
            qrels[qid][docid] = grade
    if not qrels:
        raise ValueError("No hay juicios de relevancia")
    return dict(qrels)


def sanitize_text(text: str, max_chars: int) -> str:
    # El preprocesador Go original deja algunos signos en los tokens.
    # PostgreSQL 'simple' exige que los lexemas resultantes sean alfanuméricos;
    # normalizamos TODA puntuación a espacio, idénticamente para ambos sistemas.
    text = text[:max_chars]
    return " ".join("".join(ch if ch.isalnum() else " " for ch in text).split())


def read_corpus_shard(path: Path, judged_ids: set[str], limit: int,
                      max_chars: int, seed: int):
    rng = random.Random(seed)
    sample: list[tuple[int, str, str]] = []
    judged: dict[str, tuple[int, str, str]] = {}
    seen: set[str] = set()
    total = 0
    with gzip.open(path, "rt", encoding="utf-8") as f:
        for line_num, line in enumerate(f, 1):
            if not line.strip():
                continue
            try:
                obj = json.loads(line)
            except json.JSONDecodeError as exc:
                raise ValueError(f"corpus:{line_num}: JSON inválido") from exc
            docid = obj.get("docid")
            title = obj.get("title", "")
            body = obj.get("text", "")
            if not isinstance(docid, str) or not docid:
                raise ValueError(f"corpus:{line_num}: docid inválido")
            if not isinstance(title, str) or not isinstance(body, str):
                raise ValueError(f"corpus:{line_num}: title/text debe ser texto")
            if docid in seen:
                raise ValueError(f"corpus:{line_num}: docid duplicado {docid}")
            seen.add(docid)
            cleaned = sanitize_text(title + " " + body, max_chars)
            if not cleaned:
                continue
            record = (line_num, docid, cleaned)
            total += 1
            if len(sample) < limit:
                sample.append(record)
            else:
                pos = rng.randrange(total)
                if pos < limit:
                    sample[pos] = record
            if docid in judged_ids:
                judged[docid] = record
    if total < limit:
        raise ValueError(f"Shard insuficiente: contiene {total} documentos útiles, se pidieron {limit}")
    return sample, judged, total


def choose_queries(topics: dict[str, str], judgments: dict[str, dict[str, int]],
                   judged: dict, max_queries: int):
    # Solo consultas con al menos un documento oficialmente relevante y
    # presente EN EL SHARD, no etiquetas inferidas de palabras compartidas.
    candidate = {}
    for qid, txt in topics.items():
        present = [d for d, grade in judgments.get(qid, {}).items() if grade > 0 and d in judged]
        if present:
            candidate[qid] = (txt, present)
    # Distribución aproximada 1 / 3 / 5+, y relleno con otros tamaños.
    def bucket(text: str) -> str:
        n = len(text.split())
        return "1" if n == 1 else "3" if n == 3 else "5+" if n >= 5 else "other"
    groups = defaultdict(list)
    for qid, (txt, _ids) in candidate.items():
        groups[bucket(txt)].append(qid)
    for group in groups.values():
        group.sort()
    chosen = []
    # Priorizar diversidad de longitudes de consultas oficiales, sin alterarlas.
    target = (max_queries + 2) // 3
    for b in ("1", "3", "5+"):
        chosen.extend(groups[b][:target])
    if len(chosen) < max_queries:
        remaining = sorted(q for q in candidate if q not in chosen)
        chosen.extend(remaining[:max_queries - len(chosen)])
    chosen = chosen[:max_queries]
    if not chosen:
        raise ValueError("No hay consultas con qrels positivos en este shard: pruebe otro shard")
    return chosen, candidate


def build_outputs(raw: dict[str, Path], output_dir: Path, max_docs: int,
                  max_queries: int, seed: int, max_chars: int) -> dict:
    if output_dir.exists():
        raise FileExistsError(f"La carpeta de salida ya existe: {output_dir}. Use una ruta nueva")
    topics = read_topics(raw["topics.miracl-v1.0-es-dev.tsv"])
    judgments = read_judgments(raw["qrels.miracl-v1.0-es-dev.tsv"])
    judged_ids = {docid for qid in topics for docid in judgments.get(qid, {})}
    sample, judged, scanned = read_corpus_shard(
        raw["docs-0.jsonl.gz"], judged_ids, max_docs, max_chars, seed)
    chosen, candidates = choose_queries(topics, judgments, judged, max_queries)
    anchored_ids = {
        docid for qid in chosen for docid in judgments.get(qid, {}) if docid in judged
    }
    anchors = sorted((judged[d] for d in anchored_ids), key=lambda row: row[0])
    if len(anchors) > min(max_docs, 1000):
        raise ValueError(f"Hay {len(anchors)} juicios anclados; excede el prefijo de 1000 documentos")
    selected = list(anchors)
    selected_ids = set(anchored_ids)
    for record in sorted(sample, key=lambda row: row[0]):
        if len(selected) == max_docs:
            break
        if record[1] not in selected_ids:
            selected.append(record)
            selected_ids.add(record[1])
    if len(selected) < max_docs:
        raise ValueError("No se completó el corpus solicitado")

    relevant = {
        qid: sorted(d for d, grade in judgments[qid].items() if grade > 0 and d in selected_ids)
        for qid in chosen
    }
    if any(not docs for docs in relevant.values()):
        raise AssertionError("Consulta sin qrels positivos tras seleccionar documentos")
    rel_queries = [{"id": qid, "text": topics[qid]} for qid in chosen]
    perf_queries = [
        {"id": f"lat_{b.replace('+', 'p')}_{i:02d}", "text": text}
        for b, texts in PERFORMANCE_QUERIES.items()
        for i, text in enumerate(texts, 1)
    ]
    output_dir.parent.mkdir(parents=True, exist_ok=True)
    temp = Path(tempfile.mkdtemp(prefix=output_dir.name + "-tmp-", dir=output_dir.parent))
    try:
        digest = hashlib.sha256()
        with (temp / "documents.jsonl").open("wb") as f:
            for _, docid, body in selected:
                line = (json.dumps({"id": docid, "text": body}, ensure_ascii=False, separators=(",", ":")) + "\n").encode("utf-8")
                f.write(line)
                digest.update(line)
        for name, obj in [
            ("queries_relevance.json", rel_queries),
            ("qrels_relevance.json", relevant),
            ("queries_performance.json", perf_queries),
        ]:
            (temp / name).write_text(json.dumps(obj, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        bucket_counts = dict(Counter(
            "1" if len(q["text"].split()) == 1 else
            "3" if len(q["text"].split()) == 3 else
            "5+" if len(q["text"].split()) >= 5 else "other"
            for q in rel_queries
        ))
        metadata = {
            "dataset": "MIRACL español v1.0 (dev, primer shard de corpus)",
            "official_urls": SOURCES,
            "source_shard_documents_scanned": scanned,
            "sampling": "reserva uniforme reproducible en docs-0 + anclaje de documentos juzgados al prefijo de 1000",
            "relevance_selection_bias": "consultas elegidas por tener positivos en el shard; no representativo de toda MIRACL",
            "judgments_limitation": "qrels no exhaustivos; documentos sin juicio no necesariamente irrelevantes",
            "max_docs": max_docs,
            "documents_in_first_1000": min(1000, max_docs),
            "anchored_judged_documents": len(anchors),
            "official_queries": len(rel_queries),
            "official_query_buckets": bucket_counts,
            "positive_qrels_in_subset": sum(map(len, relevant.values())),
            "performance_queries": len(perf_queries),
            "performance_has_qrels": False,
            "sanitization": "puntuacion y simbolos -> espacios para preservar compatibilidad de lexemas Go/PostgreSQL",
            "max_text_chars": max_chars,
            "seed": seed,
            "documents_sha256": digest.hexdigest(),
            "raw_files_sha256": {
                name: file_sha256(path) for name, path in raw.items()
            },
        }
        (temp / "manifest.json").write_text(json.dumps(metadata, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        os.replace(temp, output_dir)
        return metadata
    except Exception:
        shutil.rmtree(temp, ignore_errors=True)
        raise


def file_sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--raw-dir", type=Path, required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    parser.add_argument("--download", action="store_true", help="descargar primer shard y qrels oficiales")
    parser.add_argument("--max-docs", type=int, default=100000)
    parser.add_argument("--max-queries", type=int, default=30)
    parser.add_argument("--max-text-chars", type=int, default=12000)
    parser.add_argument("--seed", type=int, default=2026)
    args = parser.parse_args(argv)
    try:
        if not (1000 <= args.max_docs <= 100000):
            raise ValueError("--max-docs debe estar entre 1000 y 100000")
        if not (1 <= args.max_queries <= 300):
            raise ValueError("--max-queries debe estar entre 1 y 300")
        if not (100 <= args.max_text_chars <= 100000):
            raise ValueError("--max-text-chars debe estar entre 100 y 100000")
        raw = get_source_files(args.raw_dir, args.download)
        result = build_outputs(raw, args.output_dir, args.max_docs, args.max_queries,
                               args.seed, args.max_text_chars)
        print(f"Corpus: {result['max_docs']} documentos; consultas oficiales: {result['official_queries']}; "
              f"qrels positivos: {result['positive_qrels_in_subset']}; "
              f"consultas de latencia: {result['performance_queries']}")
        print(f"Consultas oficiales por longitud: {result['official_query_buckets']}")
        print(f"Salida: {args.output_dir}")
        return 0
    except (OSError, ValueError, UnicodeError, EOFError) as exc:
        print(f"miracl_prep: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
