from __future__ import annotations

import csv
import os
import sys
from collections import defaultdict
from statistics import mean

import matplotlib.pyplot as plt

RESULTS = sys.argv[1] if len(sys.argv) > 1 else os.path.join("benchmarks", "indexes", "results")
OUT = os.path.join(RESULTS, "plots")
os.makedirs(OUT, exist_ok=True)

LABELS = {
    "bplus_clustered": "B+ agrupado",
    "bplus_unclustered": "B+ no agrupado",
    "extendible_hash": "Hash dinámico",
}


def rows(name):
    with open(os.path.join(RESULTS, name), newline="", encoding="utf-8") as f:
        return list(csv.DictReader(f))


def grouped_mean(data, value, extra=None):
    groups = defaultdict(list)
    for r in data:
        key = (int(r["n"]), r["structure"])
        if extra:
            key += (r[extra],)
        groups[key].append(float(r[value]))
    return {k: mean(v) for k, v in groups.items()}


def plot_metric(csv_name, value, ylabel, filename, title):
    data = rows(csv_name)
    g = grouped_mean(data, value)
    structures = ["bplus_clustered", "bplus_unclustered", "extendible_hash"]
    plt.figure(figsize=(8, 5))
    for s in structures:
        pts = sorted((n, v) for (n, st), v in g.items() if st == s)
        if pts:
            plt.plot([p[0] for p in pts], [p[1] for p in pts], marker="o", label=LABELS[s])
    plt.xscale("log")
    plt.xlabel("Número de registros (N)")
    plt.ylabel(ylabel)
    plt.title(title)
    plt.grid(True, alpha=0.3)
    plt.legend()
    plt.tight_layout()
    plt.savefig(os.path.join(OUT, filename), dpi=180)
    plt.close()


plot_metric("construction.csv", "total_ms", "Tiempo (ms)", "01_construction.png", "Tiempo de construcción del índice")
plot_metric("equality.csv", "avg_us", "Tiempo promedio (µs)", "02_equality.png", "Búsqueda por igualdad exacta")
plot_metric("range.csv", "avg_ms", "Tiempo promedio (ms)", "03_range.png", "Búsqueda por rango")
plot_metric("ordering.csv", "total_ms", "Tiempo (ms)", "04_ordering.png", "Ordenamiento / recorrido ordenado")
plot_metric("storage.csv", "index_mb", "Espacio del índice (MB)", "05_storage.png", "Espacio adicional requerido")

# Updates: una línea por estructura y operación.
data = rows("updates.csv")
g = grouped_mean(data, "avg_us", extra="operation")
plt.figure(figsize=(8, 5))
for s in ["bplus_clustered", "bplus_unclustered", "extendible_hash"]:
    for op in ["insert", "delete"]:
        pts = sorted((n, v) for (n, st, operation), v in g.items() if st == s and operation == op)
        if pts:
            plt.plot(
                [p[0] for p in pts],
                [p[1] for p in pts],
                marker="o",
                label=f"{LABELS[s]} - {op}",
            )
plt.xscale("log")
plt.xlabel("Número de registros iniciales (N)")
plt.ylabel("Tiempo promedio (µs/op)")
plt.title("Inserciones y eliminaciones frecuentes")
plt.grid(True, alpha=0.3)
plt.legend()
plt.tight_layout()
plt.savefig(os.path.join(OUT, "06_updates.png"), dpi=180)
plt.close()

print(f"Gráficas creadas en: {OUT}")
