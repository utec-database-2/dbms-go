# PostgreSQL GIN nativo — preparación y carga

Los documentos se exportan con el texto original; PostgreSQL genera `tsvector` con la configuración `spanish`. El corpus, el orden y los `external_id` son iguales a los de SPIMI, pero las representaciones léxicas pueden diferir.

## Exportar 1000 documentos

```powershell
go run ./dbms/cmd/pgtextprep `
  --documents "D:\datos\miracl_es\preparado\documents.jsonl" `
  --queries "D:\datos\miracl_es\preparado\queries_performance.json" `
  --max-docs 1000 `
  --output-dir "D:\datos\miracl_es\pg_native_1000"
```

Se generan `docs.csv` (`doc_id,external_id,raw_text`), `queries.csv` (`query_id,raw_text,raw_words`) y `manifest.json` (`pgtextprep-native-v2`). El hash SHA-256 corresponde a las líneas JSONL originales del mismo prefijo que procesa `textbench`.

## Cargar en PostgreSQL local

Conexión predeterminada: `127.0.0.1:5432`, base `postgres`, usuario `postgres`, contraseña solicitada por consola. Requiere `py -m pip install "psycopg[binary]"`.

```powershell
py -B dbms/cmd/pgtextprep/pg_loader.py load --input-dir "D:\datos\miracl_es\pg_native_1000" --reset
py -B dbms/cmd/pgtextprep/pg_loader.py search --input-dir "D:\datos\miracl_es\pg_native_1000" --query-id lat_1_01 --top-k 10
py -B dbms/cmd/pgtextprep/pg_loader.py explain --input-dir "D:\datos\miracl_es\pg_native_1000" --query-id lat_1_01 --top-k 10
```

`--reset` reemplaza **únicamente** `bd2_textbench.documents`. La operación es transaccional. Se construye `search_vector tsvector GENERATED ALWAYS AS (to_tsvector('spanish',raw_text)) STORED` y un índice GIN. La validación comprueba que el vector coincida con el analizador nativo (no con Go).
