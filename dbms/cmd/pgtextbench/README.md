# Benchmark PostgreSQL GIN con FTS español nativo

Usa `pgtextprep-native-v2`: `docs.csv`, `queries.csv`, `manifest.json`. Verifica que PostgreSQL contenga los **textos originales** y los identificadores correctos antes de medir. El mismo hash SHA-256 debe aparecer en los CSV `textbench` y `pgtextbench` de un mismo prefijo.

El índice GIN usa `to_tsvector('spanish', raw_text)`; las consultas usan `websearch_to_tsquery('spanish', ... )` sobre las palabras originales unidas con `OR`. El ranking es `ts_rank_cd`; las puntuaciones no son comparables numéricamente con BM25 o coseno.

```powershell
py -B dbms/cmd/pgtextbench/run_local.py `
  --input-dir "D:\datos\miracl_es\pg_native_1000" `
  --warmup 2 --repeats 5 --min-sample-ms 50 --max-batch 1048576 `
  --top-k 10 `
  --output "D:\datos\miracl_es\postgres_native_1000.csv"
```

Medimos el tiempo de ida y vuelta Go→PostgreSQL→Go, no únicamente el acceso al índice GIN. Se reutiliza una sola conexión persistente, se realiza calentamiento y se registra el plan `EXPLAIN` fuera del cronómetro. Un índice GIN puede existir sin que el optimizador lo utilice (`Seq Scan`); comprobar `gin_used` en el CSV. La diferencia de preprocesamiento entre Go y PostgreSQL es **parte del diseño experimental**.
