import argparse
import os
from collections import defaultdict
from time import perf_counter

from features import process_audio
from dbms_client import query, vector_literal, search_methods

THRESHOLD = 0.98


def get_rows(sql):
    result = query(sql)
    return result[0].get("Rows") or []


def next_id(table):
    return max((int(r[0]) for r in get_rows(f"SELECT id FROM {table};")), default=0) + 1


def sql_text(value, limit):
    value = value.strip()
    if not value or len(value) > limit or any(c in value for c in ("'", "\\", "\n", "\r", "\x00")):
        raise ValueError("Titulo o artista no valido (evita comillas y saltos de linea)")
    return f"'{value}'"


def register_track(path, title, artist):
    title_sql, artist_sql = sql_text(title, 120), sql_text(artist, 100)
    data = process_audio(path, hop_seconds=1.0)
    if not data["segments"]:
        raise ValueError("No hay segmentos con audio valido")
    track_id, segment_id = next_id("audio_tracks"), next_id("audio_segments")
    duration_ms = round(data["duration"] * 1000)
    statements = ["BEGIN TRANSACTION;", f"INSERT INTO audio_tracks VALUES ({track_id}, {title_sql}, {artist_sql}, {duration_ms});"]
    for segment in data["segments"]:
        statements.append(f"INSERT INTO audio_segments VALUES ({segment_id}, {track_id}, "
                          f"{round(segment['start'] * 1000)}, {round(segment['end'] * 1000)}, "
                          f"{vector_literal(segment['vector'])});")
        segment_id += 1
    statements.append("END TRANSACTION;")
    start = perf_counter()
    query("\n".join(statements))
    return {"id": track_id, "title": title, "artist": artist, "duration": data["duration"],
            "segments": len(data["segments"]), "load_ms": data["load_ms"],
            "features_ms": data["features_ms"], "write_ms": (perf_counter() - start) * 1000}


def merge_intervals(matches):
    spans = []
    for match in sorted(matches, key=lambda m: (m["start"], m["end"])):
        start, end = match["start"], match["end"]
        if spans and start <= spans[-1][1] + 1e-6:
            spans[-1][1] = max(spans[-1][1], end)
        else:
            spans.append([start, end])
    return spans


def summarize(matches, duration):
    groups = defaultdict(list)
    for match in matches:
        groups[match["track_id"]].append(match)
    tracks = []
    for track_id, rows in groups.items():
        spans = merge_intervals(rows)
        covered = sum(end - start for start, end in spans)
        tracks.append({"track_id": track_id, "title": rows[0]["title"], "artist": rows[0]["artist"],
                       "segments": len(rows), "covered_seconds": covered,
                       "coverage": 100 * covered / duration, "intervals": spans,
                       "mean_score": sum(m["similarity"] for m in rows) / len(rows),
                       "max_score": max(m["similarity"] for m in rows)})
    total = sum(b - a for a, b in merge_intervals(matches))
    return sorted(tracks, key=lambda t: t["covered_seconds"], reverse=True), total


def detect_audio(path, method="ivf", threshold=THRESHOLD, k=5):
    if method not in {"ivf", "hnsw", "compare"} or not 0 <= threshold <= 1 or not 1 <= k <= 20:
        raise ValueError("Parametros de busqueda invalidos")
    start = perf_counter()
    data = process_audio(path)
    processed = perf_counter()
    tracks = {int(r[0]): r for r in get_rows("SELECT * FROM audio_tracks;")}
    catalog_ms = (perf_counter() - processed) * 1000
    if not tracks:
        raise ValueError("Registra al menos una cancion original")
    methods = ["ivf", "hnsw"] if method == "compare" else [method]
    all_matches = {m: [] for m in methods}
    engine_ms = dict.fromkeys(methods, 0.0)
    rest_ms = 0.0
    ef_search = int(os.getenv("AUDIO_EF_SEARCH", "128"))
    for segment in data["segments"]:
        call_start = perf_counter()
        results = search_methods(segment["vector"], methods, k, ef_search)
        rest_ms += (perf_counter() - call_start) * 1000
        for name, result in results.items():
            engine_ms[name] += float(result["elapsedMs"])
            hits = result.get("hits") or []
            if not hits:
                continue
            best = max(hits, key=lambda h: h["score"])
            score = float(best["score"])
            if score < threshold:
                continue
            values = best["values"]
            track_id = int(values[1])
            if track_id not in tracks:
                continue
            track = tracks[track_id]
            all_matches[name].append({"start": segment["start"], "end": segment["end"],
                                      "track_id": track_id, "title": track[1], "artist": track[2],
                                      "original_start": int(values[2]) / 1000,
                                      "original_end": int(values[3]) / 1000,
                                      "similarity": min(1.0, score)})
    comparisons = []
    outputs = {}
    count = len(data["segments"])
    for name in methods:
        summaries, covered = summarize(all_matches[name], data["duration"])
        outputs[name] = {"matches": all_matches[name], "tracks": summaries,
                         "covered_seconds": covered, "coverage": 100 * covered / data["duration"],
                         "search_ms": engine_ms[name]}
        comparisons.append({"method": name, "matches": len(all_matches[name]),
                            "coverage": outputs[name]["coverage"], "search_ms": engine_ms[name],
                            "mean_search_ms": engine_ms[name] / count if count else 0})
    primary = methods[0]
    return {"duration": data["duration"], "segments": count, "waveform": data["waveform"],
            "windows": [{"start": s["start"], "end": s["end"]} for s in data["segments"]],
            "method": primary, "threshold": threshold, "k": k, "outputs": outputs,
            "comparisons": comparisons, **outputs[primary],
            "timings": {"load_ms": data["load_ms"], "features_ms": data["features_ms"],
                        "catalog_ms": catalog_ms, "rest_ms": rest_ms,
                        "engine_ms": sum(engine_ms.values()),
                        "total_ms": (perf_counter() - start) * 1000}}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    sub = parser.add_subparsers(dest="command", required=True)
    reg = sub.add_parser("register")
    reg.add_argument("path")
    reg.add_argument("--title", required=True)
    reg.add_argument("--artist", required=True)
    det = sub.add_parser("detect")
    det.add_argument("path")
    det.add_argument("--method", choices=["ivf", "hnsw", "compare"], default="ivf")
    det.add_argument("--threshold", type=float, default=THRESHOLD)
    args = parser.parse_args()
    if args.command == "register":
        print(register_track(args.path, args.title, args.artist))
    else:
        result = detect_audio(args.path, args.method, args.threshold)
        for match in result["matches"]:
            print(f"{match['start']:.1f}s - {match['end']:.1f}s | {match['title']} | {match['similarity'] * 100:.2f}%")
        print("Coincidencias:", len(result["matches"]))
        print("Cobertura:", result["coverage"])
        print("Comparacion:", result["comparisons"])
        print("Tiempos (ms):", result["timings"])
