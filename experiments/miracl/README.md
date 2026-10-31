# BD II — Comparación experimental de recuperación textual (MIRACL español)

Esta carpeta contiene los resultados de experimentos de TF-IDF + Coseno,
BM25 (ambos sobre un índice SPIMI en Go) y PostgreSQL nativo con GIN + `ts_rank_cd`.
Los resultados corresponden a una ejecución en Windows (también debería de funcionar en linux) con Go 1.27.1 y
PostgreSQL local, en colecciones anidadas de 1 000, 10 000 y 100 000 pasajes.

## Organización

- `csv/`: los **ocho CSV originales** de rendimiento y relevancia; además de resúmenes derivados, para saber cuántas consultas utilizaron realmente GIN
- `graficas/`: las seis gráficas de latencia, relevancia y tamaños de índices.
- `memoria/`: cuatro registros JSON de memoria; opcionalmente gráficas y resumen saber el consumo de RAM observado
- `metadata/`: procedencia de MIRACL, consultas, juicios de relevancia,
  metodología y hashes SHA-256 de las evidencias.

## Resultados de relevancia (100K; 30 consultas)

| Sistema | Precision@10 macro | Recall@10 macro |
|---|---:|---:|
| TF-IDF + Coseno | 0.1000 | 0.5656 |
| BM25 | 0.1300 | 0.7583 |
| PostgreSQL nativo + GIN | 0.0833 | 0.4050 |

Las métricas utilizan únicamente los **juicios positivos conocidos en el
subconjunto** (53 juicios positivos para 30 consultas). La selección ancló
documentos juzgados al comienzo del corpus para poder medir 1K y se basó en
un shard de MIRACL; los resultados **no reproducen el benchmark oficial** ni
permiten suponer que documentos no juzgados son irrelevantes.
El máximo P@10 atribuible a los 53 juicios en estas 30 consultas es 0.1767.

Fuente: MIRACL español, documentación y URLs originales en
`metadata/manifest_corpus.json`.
