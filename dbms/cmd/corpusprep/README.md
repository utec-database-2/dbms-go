# Corpus experimental MIRACL (español)

Este módulo prepara un **subconjunto controlado** de MIRACL español a partir del primer shard de pasajes de Wikipedia y los temas/juicios oficiales del split `dev`.

## Entradas públicas

- `miracl/miracl-corpus`, primer archivo `miracl-corpus-v1.0-es/docs-0.jsonl.gz`.
- `miracl/miracl`, `topics.miracl-v1.0-es-dev.tsv`.
- `miracl/miracl`, `qrels.miracl-v1.0-es-dev.tsv`.

Los enlaces exactos están en `miracl_prep.py`. Se descarga aproximadamente un archivo comprimido de ~93 MB más dos TSV pequeños; no es necesario descargar toda la colección.

## Ejecución Windows (también debería funcionar en linux...)

```powershell
cd D:\dbms-go # o lugar donde hayas clonado el repositorio, en este caso en el disco D
py -B -m unittest discover -s dbms/cmd/corpusprep -p test_miracl_prep.py -v
py -B dbms/cmd/corpusprep/miracl_prep.py --download --raw-dir D:\datos\miracl_es\raw --output-dir D:\datos\miracl_es\preparado --max-docs 100000 --max-queries 30 --seed 2026
```

Si el servidor falla durante la descarga, vuelva a ejecutar el mismo comando: los archivos completos se reutilizan y los `.partial` incompletos se eliminan.

## Archivos de salida

- `documents.jsonl`: **exactamente** 100 000 pasajes (campos `id`, `text`); los primeros 1000 y 10000 definen las colecciones menores.
- `queries_performance.json`: 15 consultas controladas, cinco de 1 palabra, cinco de 3 palabras y cinco de 5+ palabras.
- `queries_relevance.json`: hasta 30 consultas originales del `dev`, sin cambios de redacción.
- `qrels_relevance.json`: documentos presentes en el subconjunto.
- `manifest.json`: procedencia, hashes SHA-256, selección, semillas, parámetros, sesgos.

## Dos evaluaciones distintas

### Rendimiento: 1, 3 y 5+ palabras

```powershell
go run ./dbms/cmd/textbench --documents D:\datos\miracl_es\preparado\documents.jsonl --queries D:\datos\miracl_es\preparado\queries_performance.json --sizes 1000,10000,100000 --warmup 3 --repeats 30 --top-k 10 --output D:\datos\miracl_es\spimi_latencia.csv
```

**No pasar `--qrels` en este experimento**, porque las consultas controladas no tienen juicios oficiales.

### Relevancia: temas MIRACL originales

```powershell
go run ./dbms/cmd/textbench --documents D:\datos\miracl_es\preparado\documents.jsonl --queries D:\datos\miracl_es\preparado\queries_relevance.json --qrels D:\datos\miracl_es\preparado\qrels_relevance.json --sizes 1000,10000,100000 --warmup 3 --repeats 30 --top-k 10 --output D:\datos\miracl_es\spimi_relevancia.csv
```

En una primera ejecución con datos grandes se recomienda comenzar por `--sizes 1000`, confirmar PostgreSQL y seguir con las colecciones mayores.

Referencias oficiales:
- https://huggingface.co/datasets/miracl/miracl-corpus
- https://huggingface.co/datasets/miracl/miracl

## Pruebas

```powershell
py -B -m unittest discover -s dbms/cmd/corpusprep -p test_miracl_prep.py -v
```
