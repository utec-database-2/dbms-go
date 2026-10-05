import csv
import os
import sys
from collections import defaultdict
from statistics import mean

import matplotlib.pyplot as plt


def read_csv(path):
    with open(path, newline="", encoding="utf-8") as f:
        return list(csv.DictReader(f))


def grouped(rows, value_field, extra_filter=None):
    data = defaultdict(list)
    for row in rows:
        if extra_filter and not extra_filter(row):
            continue
        key = (int(row["n"]), row["structure"])
        data[key].append(float(row[value_field]))
    out = defaultdict(dict)
    for (n, structure), values in data.items():
        out[structure][n] = mean(values)
    return out


def line_plot(data, title, ylabel, output):
    plt.figure()
    for structure, by_n in sorted(data.items()):
        xs = sorted(by_n)
        ys = [by_n[x] for x in xs]
        plt.plot(xs, ys, marker="o", label=structure)
    plt.xscale("log")
    plt.xlabel("Número de registros")
    plt.ylabel(ylabel)
    plt.title(title)
    plt.legend()
    plt.grid(True, alpha=0.25)
    plt.tight_layout()
    plt.savefig(output, dpi=180)
    plt.close()


def maintenance_plot(rows, output):
    data = defaultdict(list)
    for row in rows:
        label = f'{row["structure"]}: {row["phase"]}'
        data[(label, int(row["n"]))].append(float(row["time_ms"]))
    grouped_data = defaultdict(dict)
    for (label, n), values in data.items():
        grouped_data[label][n] = mean(values)

    plt.figure()
    for label, by_n in sorted(grouped_data.items()):
        xs = sorted(by_n)
        ys = [by_n[x] for x in xs]
        plt.plot(xs, ys, marker="o", label=label)
    plt.xscale("log")
    plt.xlabel("Número de registros")
    plt.ylabel("Tiempo (ms)")
    plt.title("Mantenimiento y reorganización tras eliminaciones")
    plt.legend(fontsize=8)
    plt.grid(True, alpha=0.25)
    plt.tight_layout()
    plt.savefig(output, dpi=180)
    plt.close()


def main():
    results = sys.argv[1] if len(sys.argv) > 1 else os.path.join("benchmarks", "storage", "results")
    plots = os.path.join(results, "plots")
    os.makedirs(plots, exist_ok=True)

    insertion = read_csv(os.path.join(results, "insertion.csv"))
    search = read_csv(os.path.join(results, "search.csv"))
    storage = read_csv(os.path.join(results, "storage.csv"))
    reorg = read_csv(os.path.join(results, "reorganization.csv"))

    line_plot(grouped(insertion, "time_ms"), "Tiempo de inserción", "Tiempo (ms)", os.path.join(plots, "01_insertion.png"))
    line_plot(grouped(search, "avg_us"), "Búsqueda por clave primaria", "Tiempo promedio (µs)", os.path.join(plots, "02_search.png"))
    line_plot(grouped(storage, "total_bytes"), "Espacio en disco", "Bytes", os.path.join(plots, "03_storage.png"))
    maintenance_plot(reorg, os.path.join(plots, "04_reorganization.png"))

    print(f"Gráficas generadas en: {plots}")


if __name__ == "__main__":
    main()
