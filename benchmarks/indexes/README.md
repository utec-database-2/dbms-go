# Benchmark: B+ agrupado vs B+ no agrupado vs Hash Dinámico

## Pasos para ejecutar las pruebas

### 1. Ubicar el benchmark

Copiar la carpeta en:

```text
D:\dbms-go\benchmarks\indexes\
```

### 2. Ejecutar una prueba rápida

Desde:

```powershell
cd D:\dbms-go
```

Ejecutar:

```powershell
go run ./benchmarks/indexes -sizes 1000 -repeats 1 -queries 100 -range-queries 10 -updates 100
```

Esta prueba permite verificar que B+ agrupado, B+ no agrupado y Hash Dinámico funcionan correctamente dentro del benchmark.

### 3. Ejecutar la prueba completa

Para obtener los resultados que se usarán en el informe:

```powershell
go run ./benchmarks/indexes `
  -sizes 1000,10000,100000 `
  -repeats 3 `
  -queries 1000 `
  -range-queries 100 `
  -range-fraction 0.01 `
  -updates 1000
```

Los resultados se guardarán en:

```text
benchmarks\indexes\results\
```

### 4. Generar las gráficas

Instalar `matplotlib` si fuera necesario:

```powershell
py -m pip install matplotlib
```

Luego ejecutar:

```powershell
py .\benchmarks\indexes\plot_results.py .\benchmarks\indexes\results
```

Las gráficas se guardarán en:

```text
benchmarks\indexes\results\plots\
```

---

## Decisiones metodológicas

- Se utilizan datasets de **1 000, 10 000 y 100 000 registros**.
- Las claves utilizadas son deterministas: `1..N`.
- El payload tiene un tamaño fijo de **64 bytes**.
- El B+ utiliza un `order` de **64**.
- El Hash Dinámico utiliza un `bucketSize` de **64**.
- El `SequentialFile` se carga ordenado por clave, ya que representa el almacenamiento físico utilizado por 