from __future__ import annotations

import csv
import json
import statistics
import time
import tracemalloc
from pathlib import Path

from .cli import load_documents
from .tfidf import TFIDFRanker, tokenize


def precision_recall_at_k(retrieved: list[str], relevant: set[str], k: int = 10) -> tuple[float, float]:
    if k <= 0:
        raise ValueError("k debe ser positivo")
    hits = len(set(retrieved[:k]) & relevant)
    return hits / k, hits / len(relevant) if relevant else 0.0


def benchmark(documents_file: str, queries_file: str, sizes: list[int], repeats: int, output: str) -> None:
    if repeats < 1 or any(n < 1 for n in sizes):
        raise ValueError("repeats y todos los sizes deben ser positivos")
    docs = load_documents(documents_file)
    with Path(queries_file).open(encoding="utf-8") as f:
        queries = json.load(f)
    if isinstance(queries, list):
        items = [(q, None) for q in queries]
    elif isinstance(queries, dict):
        items = [(q, {str(d) for d in rel}) for q, rel in queries.items()]
    else:
        raise ValueError("queries debe ser lista de consultas o {consulta:[ids_relevantes]}")
    if not items or any(not isinstance(q, str) for q, _ in items):
        raise ValueError("debe haber al menos una consulta textual")

    dst = Path(output)
    dst.parent.mkdir(parents=True, exist_ok=True)
    rows = []
    for n in sizes:
        if n > len(docs):
            raise ValueError(f"se pidieron {n} documentos, pero el archivo tiene {len(docs)}")
        subset = docs[:n]
        # El cronómetro se mide sin tracemalloc para evitar su sobrecarga.
        build_start = time.perf_counter_ns()
        model = TFIDFRanker.from_documents(subset)
        build_ms = (time.perf_counter_ns() - build_start) / 1_000_000

        # Segunda construcción instrumentada: pico de asignaciones de Python,
        # excluyendo el dataset cargado. No equivale a memoria residente (RSS).
        tracemalloc.start()
        memory_model = TFIDFRanker.from_documents(subset)
        _, peak = tracemalloc.get_traced_memory()
        tracemalloc.stop()
        del memory_model

        # Archivo serializado para medir tamaño real del índice en disco.
        index_path = dst.parent / f"tfidf_index_{n}.json"
        model.save(index_path)
        index_bytes = index_path.stat().st_size
        active_ids = {doc_id for doc_id, _ in subset}

        for query, relevant in items:
            samples = []
            for _ in range(repeats):
                started = time.perf_counter_ns()
                found = model.search(query, 10)
                samples.append((time.perf_counter_ns() - started) / 1_000_000)
            # Juicios de relevancia deben corresponder al subconjunto evaluado.
            labels = (relevant & active_ids) if relevant is not None else None
            p10, r10 = (precision_recall_at_k([h.doc_id for h in found], labels)
                        if labels is not None else (None, None))
            s = sorted(samples)
            rows.append({
                "method": "TF-IDF+Cosine", "n_documents": n,
                "query": query, "query_words": len(tokenize(query)),
                "build_ms": round(build_ms, 4),
                "query_mean_ms": round(statistics.mean(samples), 6),
                "query_median_ms": round(statistics.median(samples), 6),
                "query_p95_ms": round(s[max(0, int(0.95 * len(s) + 0.999999) - 1)], 6),
                "index_disk_bytes": index_bytes,
                "build_peak_alloc_bytes": peak,
                "precision_at_10": p10, "recall_at_10": r10,
                "query_repeats": repeats,
            })
        print(f"N={n}: índice {index_bytes} bytes, construcción {build_ms:.2f} ms")
    with dst.open("w", newline="", encoding="utf-8") as stream:
        writer = csv.DictWriter(stream, fieldnames=list(rows[0]))
        writer.writeheader()
        writer.writerows(rows)
    print(f"CSV: {dst}")
