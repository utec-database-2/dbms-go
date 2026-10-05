# Benchmark: Heap File vs Archivo Secuencial Paginado

## Pasos para ejecutar las pruebas

### 1. Ubicar el benchmark

Copiar esta carpeta como:

```text
D:\dbms-go\benchmarks\storage\
```

### 2. Ejecutar una prueba rápida

Desde `D:\dbms-go`:

```powershell
go run ./benchmarks/storage -sizes 1000 -repeats 1 -queries 100 -delete-fraction 0.35
```

### 3. Ejecutar la corrida para el informe

```powershell
go run ./benchmarks/storage `
  -sizes 1000,10000,100000 `
  -repeats 3 `
  -queries 1000 `
  -delete-fraction 0.35
```

Los resultados se guardan en:

```text
benchmarks\storage\results\
```

### 4. Generar las gráficas

Si fuera necesario:

```powershell
py -m pip install matplotlib
```

Luego:

```powershell
py .\benchmarks\storage\plot_results.py .\benchmarks\storage\results
```

Las gráficas se guardan en:

```text
benchmarks\storage\results\plots\
```

## Decisiones metodológicas

- Se prueban `1 000`, `10 000` y `100 000` registros.
- Cada tamaño se repite `3` veces.
- Ambas estructuras reciben exactamente las mismas claves y el mismo orden pseudoaleatorio determinista de llegada.
- Se usa orden pseudoaleatorio porque esta prueba mide el costo real de que el Archivo Secuencial mantenga los registros ordenados durante inserciones.
- Cada registro lógico ocupa `64 bytes`: `8 bytes` de clave primaria y `56 bytes` de payload.
- Heap File usa páginas de `4096 bytes`.
- La capacidad de página del Sequential File se calcula para aproximar el mismo tamaño físico de página de 4 KiB según su layout actual.
- La creación del archivo se realiza antes de iniciar el cronómetro; el tiempo de inserción mide únicamente las inserciones de los `N` registros.
- La búsqueda por clave primaria usa las mismas claves de consulta en ambas estructuras.
- Heap File no posee búsqueda por clave: se utiliza `Scan` hasta encontrar la clave dentro del payload.
- Sequential File utiliza su operación `Search(key)`.
- El espacio en disco del Heap File es el tamaño de su archivo `.heap`.
- El espacio en disco del Sequential File es la suma del archivo principal `.seq` y el archivo de overflow `.ovf`.
- Para la prueba de reorganización se elimina el `35%` de los registros.
- En Sequential File se desactiva temporalmente la reorganización automática durante los deletes (`SetReorgThreshold(1.0)`) para medir por separado el costo de eliminación lazy y el costo de `Reorganize()`.
- Heap File no tiene una reorganización global: `Delete` compacta inmediatamente la página afectada. Por eso se registra como `delete_with_page_compaction` y no como una reorganización global.
- También se registra `delete_plus_reorganize` para Sequential File, permitiendo comparar el costo total de su estrategia diferida contra la recuperación inmediata de espacio del Heap File.
- El orden de evaluación Heap/Sequential alterna entre repeticiones para reducir sesgo por caché.
