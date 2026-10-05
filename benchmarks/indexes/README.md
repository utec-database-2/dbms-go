# Benchmark: B+ agrupado vs B+ no agrupado vs Hash Dinámico

Este benchmark implementa la comparación experimental solicitada en la Parte 1 del proyecto.

## Qué mide

1. `construction.csv`: tiempo de construcción del índice. La creación/población de HeapFile y SequentialFile ocurre antes del cronómetro.
2. `equality.csv`: búsqueda por igualdad exacta. En los tres casos se resuelve el RID hasta el payload.
3. `range.csv`: B+ usa `RangeSearch`; Hash Dinámico usa `HeapFile.Scan` como fallback porque `SupportsRange()==false`.
4. `ordering.csv`: B+ usa `OrderedScan`; Hash Dinámico usa `HeapFile.Scan + sort` porque no conserva orden.
5. `storage.csv`: tamaño únicamente del archivo `.idx`, es decir, espacio adicional del índice.
6. `updates.csv`: costo promedio de inserciones y eliminaciones frecuentes, incluyendo la actualización del almacenamiento base y del índice.

## Ubicación

Copiar esta carpeta como:

```text
D:\dbms-go\benchmarks\indexes\
```

El benchmark usa estos paquetes del proyecto:

```text
github.com/dbms-go/v2/indexes/bplus
github.com/dbms-go/v2/dbms/lib/index/extendible
github.com/dbms-go/v2/dbms/lib/storage/heap
github.com/dbms-go/v2/dbms/lib/concurrency/sequential
```

## Primera prueba recomendada (rápida)

Desde `D:\dbms-go`:

```powershell
go run ./benchmarks/indexes -sizes 1000 -repeats 1 -queries 100 -range-queries 10 -updates 100
```

Esto sirve para comprobar que todas las APIs están integradas correctamente antes de una corrida larga.

## Corrida para el informe

```powershell
go run ./benchmarks/indexes `
  -sizes 1000,10000,100000 `
  -repeats 3 `
  -queries 1000 `
  -range-queries 100 `
  -range-fraction 0.01 `
  -updates 1000
```

**Nota:** el Hash Dinámico persistente escribe buckets/directorio en disco y 100 000 registros puede tomar bastante más que los B+. No interrumpir la corrida final solo porque esa etapa sea lenta.

## Generar gráficas

Instalar matplotlib si fuera necesario:

```powershell
py -m pip install matplotlib
```

Luego:

```powershell
py .\benchmarks\indexes\plot_results.py .\benchmarks\indexes\results
```

Se generan:

```text
results\plots\01_construction.png
results\plots\02_equality.png
results\plots\03_range.png
results\plots\04_ordering.png
results\plots\05_storage.png
results\plots\06_updates.png
```

## Metodología y decisiones

- Dataset determinista: claves `1..N` y payload de tamaño fijo.
- El SequentialFile se carga en orden porque ese orden físico forma parte del B+ agrupado.
- El HeapFile recibe las mismas claves en orden pseudoaleatorio determinista.
- Dataset por defecto: `1 000`, `10 000`, `100 000` registros.
- B+ order por defecto: `64`.
- Hash bucket size por defecto: `64`.
- Payload por defecto: `64 bytes`.
- Las estructuras se miden en orden rotativo entre repeticiones para reducir sesgo por caché.
- Igualdad: las mismas claves se usan para las tres estructuras.
- Rango: selectividad por defecto `1%`.
- Hash no implementa RangeSearch ni recorrido ordenado: se registra explícitamente el fallback usado.
- Espacio adicional: solo `.idx`; HeapFile/SequentialFile quedan fuera de esa cifra.
- Para updates se insertan claves nuevas y luego se eliminan exactamente esas entradas, manteniendo el dataset base estable entre repeticiones.
