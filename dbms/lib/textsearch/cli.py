from __future__ import annotations

import argparse
import json
from pathlib import Path

from .tfidf import TFIDFRanker


def load_documents(path: str | Path) -> list[tuple[str, str]]:
    """Lee JSONL: {"id":"...","text":"..."}; acepta doc_id/content/contenido."""
    docs = []
    with Path(path).open(encoding="utf-8") as stream:
        for line_no, line in enumerate(stream, 1):
            if not line.strip():
                continue
            item = json.loads(line)
            doc_id = item.get("id", item.get("doc_id"))
            content = item.get("text", item.get("content", item.get("contenido")))
            if doc_id is None or not isinstance(content, str):
                raise ValueError(f"JSONL línea {line_no}: se requieren id y text")
            docs.append((str(doc_id), content))
    return docs


def main() -> None:
    parser = argparse.ArgumentParser(description="TF-IDF + coseno: Parte 3, BD2")
    sub = parser.add_subparsers(dest="action", required=True)
    build = sub.add_parser("build", help="Construir el modelo desde SPIMI o JSONL")
    inputs = build.add_mutually_exclusive_group(required=True)
    inputs.add_argument("--documents", help="JSONL de documentos (modo local, sin SPIMI)")
    inputs.add_argument("--spimi", help="JSON del índice invertido entregado por SPIMI")
    build.add_argument("--output", required=True, help="Ruta del índice TF-IDF JSON")

    search = sub.add_parser("search", help="Consultar un índice persistido")
    search.add_argument("--index", required=True)
    search.add_argument("--query", required=True)
    search.add_argument("--top-k", type=int, default=10)

    bench = sub.add_parser("bench", help="Benchmark exclusivo del módulo TF-IDF")
    bench.add_argument("--documents", required=True, help="JSONL; al menos max(sizes) documentos")
    bench.add_argument("--queries", required=True, help="JSON: lista de strings o {consulta:[relevantes]}")
    bench.add_argument("--sizes", default="1000,10000,100000")
    bench.add_argument("--repeats", type=int, default=30)
    bench.add_argument("--output", default="resultados/texto_tfidf.csv")

    args = parser.parse_args()
    if args.action == "build":
        ranker = (TFIDFRanker.from_spimi_file(args.spimi) if args.spimi
                  else TFIDFRanker.from_documents(load_documents(args.documents)))
        ranker.save(args.output)
        print(json.dumps({"documents": ranker.total_documents,
                          "terms": len(ranker.postings), "output": args.output}, ensure_ascii=False))
    elif args.action == "search":
        ranker = TFIDFRanker.load(args.index)
        hits = ranker.search(args.query, args.top_k)
        print(json.dumps([{"doc_id": h.doc_id, "score": round(h.score, 10)} for h in hits],
                         ensure_ascii=False, indent=2))
    elif args.action == "bench":
        from .benchmark import benchmark
        sizes = [int(s) for s in args.sizes.split(",")]
        benchmark(args.documents, args.queries, sizes, args.repeats, args.output)


if __name__ == "__main__":
    main()


# py -m textsearch.cli build --documents textsearch/examples/docs.jsonl --output resultados/tfidf_demo.json
# py -m textsearch.cli search --index resultados/tfidf_demo.json --query "base de datos" --top-k 3