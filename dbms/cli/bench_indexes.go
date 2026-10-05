package main

import (
	"fmt"
	"path/filepath"

	"github.com/dbms-go/v2/dbms/lib/external/sorting"
	"github.com/dbms-go/v2/dbms/lib/index/bplus"
	"github.com/dbms-go/v2/dbms/lib/index/extendible"
	"github.com/dbms-go/v2/dbms/lib/storage"
	"github.com/dbms-go/v2/dbms/lib/storage/heap"
	"github.com/dbms-go/v2/dbms/lib/storage/sequential"
)

// Comparación 1.6 (estructuras de indexación): B+ agrupado, B+ no agrupado y
// hash extensible. Se mide construcción, consulta por igualdad, por rango y
// recorrido ordenado, espacio adicional y comportamiento con altas y bajas
// frecuentes.
//
// Nota importante para leer los números: en este proyecto los tres índices viven
// en memoria, así que su "espacio adicional" se mide en memoria. El B+ agrupado
// además se apoya en el archivo secuencial, cuyo tamaño en disco se reporta
// aparte.

// indexRow es una fila de la tabla resumen de esta comparación.
type indexRow struct {
	n        int
	tecnica  string
	build    float64 // ms para cargar n filas con el índice montado
	sinIdx   float64 // ms para cargar las mismas filas sin ningún índice
	costeIdx float64 // build - sinIdx
	igualdad float64 // µs por búsqueda exacta
	rango    float64 // µs por consulta de rango (≈1% de las claves)
	ordenado float64 // ms para recorrer todo en orden de clave
	churn    float64 // µs por operación (5% altas + 5% bajas)
	disco    int64   // bytes del archivo de la tabla
	memoria  uint64  // bytes de índice en memoria
	altura   int     // altura del B+ (0 si no aplica)
	nodos    int
}

func benchIndexes(cfg *config) error {
	if cfg.queries <= 0 {
		cfg.queries = 500
	}
	fmt.Println("\n== Comparación 1.6 · Estructuras de indexación: B+ agrupado vs B+ no agrupado vs Hash dinámico ==")
	fmt.Printf("   consultas por medición: %d\n", cfg.queries)

	if err := mustDir(cfg.dir); err != nil {
		return err
	}

	csv, err := newCSV(cfg.out, "indices.csv",
		"n", "tecnica", "construccion_ms", "carga_sin_indice_ms", "coste_indice_ms",
		"igualdad_us", "rango_us", "ordenado_ms", "churn_us",
		"disco_bytes", "memoria_bytes", "altura", "nodos")
	if err != nil {
		return err
	}

	var todas []indexRow
	for _, n := range cfg.sizes {
		filas := makeRows(n, cfg.seed)
		clavesEq := randomKeys(n, cfg.queries, cfg.seed+3)
		clavesRango := randomKeys(n, cfg.queries/10+1, cfg.seed+5)
		segEq := randomSegundos(n, cfg.queries, cfg.seed+3)
		segRango := randomSegundos(n, cfg.queries/10+1, cfg.seed+5)
		fmt.Printf("\n-- %d filas --\n", n)

		base := filaIndex{cfg: cfg, n: n, filas: filas,
			clavesEq: clavesEq, clavesRango: clavesRango,
			segEq: segEq, segRango: segRango}

		sinIdx, err := base.cargaSinIndice()
		if err != nil {
			return fmt.Errorf("carga sin índice con %d filas: %w", n, err)
		}
		agrupado, err := base.bplusAgrupado()
		if err != nil {
			return fmt.Errorf("B+ agrupado con %d filas: %w", n, err)
		}
		noAgrupado, err := base.bplusNoAgrupado()
		if err != nil {
			return fmt.Errorf("B+ no agrupado con %d filas: %w", n, err)
		}
		hash, err := base.hashDinamico()
		if err != nil {
			return fmt.Errorf("hash con %d filas: %w", n, err)
		}

		for _, r := range []indexRow{agrupado, noAgrupado, hash} {
			r.sinIdx = sinIdx
			r.costeIdx = r.build - sinIdx
			todas = append(todas, r)
			csv.add(i2s(r.n), r.tecnica, f2s(r.build), f2s(r.sinIdx), f2s(r.costeIdx),
				f2s(r.igualdad), f2s(r.rango), f2s(r.ordenado), f2s(r.churn),
				i64s(r.disco), i64s(int64(r.memoria)), i2s(r.altura), i2s(r.nodos))
		}
	}

	resumenIndices(todas)
	if err := csv.save(); err != nil {
		return err
	}
	fmt.Printf("\nCSV: %s\n", csv.path)
	return dibujarGraficasIndices(cfg.out, todas)
}

// filaIndex agrupa lo que las tres técnicas necesitan para medir en igualdad.
type filaIndex struct {
	cfg         *config
	n           int
	filas       []storage.Tuple
	clavesEq    []storage.Tuple
	clavesRango []storage.Tuple
	segEq       []int32
	segRango    []int32
}

func (f filaIndex) base() string { return filepath.Join(f.cfg.dir, fmt.Sprintf("idx_%d", f.n)) }

// cargaSinIndice es la referencia: las mismas filas, sin ningún índice.
func (f filaIndex) cargaSinIndice() (float64, error) {
	base := f.base() + "_plano"
	limpiar(base)
	h, err := heap.New(base, benchTable, heap.Options{})
	if err != nil {
		return 0, err
	}
	if err := h.Open(); err != nil {
		return 0, err
	}
	t0 := now()
	for _, r := range f.filas {
		if _, err := h.Insert(r); err != nil {
			h.Close()
			return 0, err
		}
	}
	if err := h.Sync(); err != nil {
		h.Close()
		return 0, err
	}
	d := ms(now().Sub(t0))
	return d, h.Close()
}

// ---------------------------------------------------------------------------
// B+ agrupado: el índice ES el almacenamiento ordenado (archivo secuencial)
// ---------------------------------------------------------------------------

func (f filaIndex) bplusAgrupado() (indexRow, error) {
	base := f.base() + "_agrupado"
	limpiar(base)

	s, err := sequential.New(base, benchTable, sequential.Options{})
	if err != nil {
		return indexRow{}, err
	}
	if err := s.Open(); err != nil {
		return indexRow{}, err
	}
	idx, err := bplus.NewClusteredIndex(bplusOrder, &seqStorage{f: s}, benchTable)
	if err != nil {
		return indexRow{}, err
	}

	// Carga: cada inserción escribe la fila en el archivo y la registra en el
	// árbol. Con claves en orden creciente es el mejor caso del agrupado; con
	// claves en desorden, el archivo secuencial tiene que absorber el
	// desorden. Se mide el caso realista: desorden.
	desorden := shuffleRows(f.filas, f.cfg.seed+13)
	var build float64
	memIdx, err := medirMem(func() error {
		t0 := now()
		for _, r := range desorden {
			if err := idx.Insert(r); err != nil {
				return err
			}
		}
		if err := s.Sync(); err != nil {
			return err
		}
		build = ms(now().Sub(t0))
		return nil
	})
	if err != nil {
		s.Close()
		return indexRow{}, err
	}

	// Igualdad.
	t0 := now()
	for _, k := range f.clavesEq {
		if _, err := idx.Search(storage.Tuple{k[0]}); err != nil {
			s.Close()
			return indexRow{}, err
		}
	}
	igualdad := us(now().Sub(t0)) / float64(len(f.clavesEq))

	// Rango: cada consulta cubre ~1% de las claves.
	t0 = now()
	for _, k := range f.clavesRango {
		lo := storage.Tuple{k[0]}
		hi := storage.Tuple{k[0].(int32) + int32(f.n/100)}
		if _, err := idx.RangeSearch(lo, hi); err != nil {
			s.Close()
			return indexRow{}, err
		}
	}
	rango := us(now().Sub(t0)) / float64(len(f.clavesRango))

	// Recorrido ordenado completo: es un RangeSearch sobre todo el espacio de
	// claves, que es exactamente lo que sabe hacer un B+.
	t0 = now()
	if _, err := idx.RangeSearch(storage.Tuple{int32(0)}, storage.Tuple{int32(f.n)}); err != nil {
		s.Close()
		return indexRow{}, err
	}
	ordenado := ms(now().Sub(t0))

	// Altas y bajas frecuentes.
	churn, err := medirChurnSecuencial(s, idx, f.filas, f.n/20)
	if err != nil {
		s.Close()
		return indexRow{}, err
	}

	stats := idx.TreeStats()
	disco, _ := fileSize(base, sequential.SUFFIX_SEQ, sequential.SUFFIX_AUX)
	if err := s.Close(); err != nil {
		return indexRow{}, err
	}

	return indexRow{
		n: f.n, tecnica: "B+ agrupado", build: build,
		igualdad: igualdad, rango: rango, ordenado: ordenado, churn: churn,
		disco: disco, memoria: memIdx, altura: stats.Height, nodos: stats.Nodes,
	}, nil
}

// ---------------------------------------------------------------------------
// B+ no agrupado: índice en memoria que apunta al RID de un heap file
// ---------------------------------------------------------------------------

func (f filaIndex) bplusNoAgrupado() (indexRow, error) {
	base := f.base() + "_noagrupado"
	limpiar(base)

	h, err := heap.New(base, benchTable, heap.Options{})
	if err != nil {
		return indexRow{}, err
	}
	if err := h.Open(); err != nil {
		return indexRow{}, err
	}
	idx, err := bplus.NewUnclusteredIndex(bplusOrder, &heapStorage{h: h}, func(t storage.Tuple) (any, error) {
		return t[1], nil
	})
	if err != nil {
		return indexRow{}, err
	}

	var build float64
	memIdx, err := medirMem(func() error {
		t0 := now()
		for _, r := range f.filas {
			rid, err := h.Insert(r)
			if err != nil {
				return err
			}
			if err := idx.InsertRID(r[1], rid); err != nil {
				return err
			}
		}
		if err := h.Sync(); err != nil {
			return err
		}
		build = ms(now().Sub(t0))
		return nil
	})
	if err != nil {
		h.Close()
		return indexRow{}, err
	}

	t0 := now()
	for _, k := range f.segEq {
		if _, err := idx.Search(k); err != nil {
			h.Close()
			return indexRow{}, err
		}
	}
	igualdad := us(now().Sub(t0)) / float64(len(f.segEq))

	t0 = now()
	for _, lo := range f.segRango {
		if _, err := idx.RangeSearch(lo, lo+int32(f.n/100)); err != nil {
			h.Close()
			return indexRow{}, err
		}
	}
	rango := us(now().Sub(t0)) / float64(len(f.segRango))

	t0 = now()
	if _, err := idx.RangeSearch(int32(0), int32(f.n)); err != nil {
		h.Close()
		return indexRow{}, err
	}
	ordenado := ms(now().Sub(t0))

	churn, err := medirChurnHeap(h, idx, f.filas, f.n/20)
	if err != nil {
		h.Close()
		return indexRow{}, err
	}

	stats := idx.TreeStats()
	disco, _ := fileSize(base, storage.SUFFIX_DATA)
	if err := h.Close(); err != nil {
		return indexRow{}, err
	}

	return indexRow{
		n: f.n, tecnica: "B+ no agrupado", build: build,
		igualdad: igualdad, rango: rango, ordenado: ordenado, churn: churn,
		disco: disco, memoria: memIdx, altura: stats.Height, nodos: stats.Nodes,
	}, nil
}

// ---------------------------------------------------------------------------
// Hash dinámico: solo igualdad, sin orden ni rangos
// ---------------------------------------------------------------------------

func (f filaIndex) hashDinamico() (indexRow, error) {
	base := f.base() + "_hash"
	limpiar(base)

	h, err := heap.New(base, benchTable, heap.Options{})
	if err != nil {
		return indexRow{}, err
	}
	if err := h.Open(); err != nil {
		return indexRow{}, err
	}
	idx := extendible.New(hashBucketSize)

	var build float64
	memIdx, err := medirMem(func() error {
		t0 := now()
		for _, r := range f.filas {
			rid, err := h.Insert(r)
			if err != nil {
				return err
			}
			if err := idx.Insert(r[1], rid); err != nil {
				return err
			}
		}
		if err := h.Sync(); err != nil {
			return err
		}
		build = ms(now().Sub(t0))
		return nil
	})
	if err != nil {
		h.Close()
		return indexRow{}, err
	}

	t0 := now()
	for _, k := range f.segEq {
		if _, err := idx.Search(k); err != nil {
			h.Close()
			return indexRow{}, err
		}
	}
	igualdad := us(now().Sub(t0)) / float64(len(f.segEq))

	// El hash no soporta rangos: la única forma es recorrer el heap entero y
	// filtrar. Se mide ese coste real, que es lo que paga quien elige hash para
	// una columna por la que luego consulta rangos.
	t0 = now()
	for _, lo := range f.segRango {
		hi := lo + int32(f.n/100)
		var encontrados int
		it := h.Scan()
		for it.Next() {
			v := it.Tuple()[1].(int32)
			if v >= lo && v <= hi {
				encontrados++
			}
		}
		if err := it.Err(); err != nil {
			h.Close()
			return indexRow{}, err
		}
	}
	rango := us(now().Sub(t0)) / float64(len(f.segRango))

	// Tampoco está ordenado: recorrer en orden exige escanear y después ordenar,
	// así que se miden las dos mitades.
	t0 = now()
	var todas []storage.Tuple
	it := h.Scan()
	for it.Next() {
		todas = append(todas, it.Tuple())
	}
	if err := it.Err(); err != nil {
		h.Close()
		return indexRow{}, err
	}
	if _, _, err := sorting.ExternalSort(todas, func(t storage.Tuple) (any, error) { return t[1], nil }, false, 4096); err != nil {
		h.Close()
		return indexRow{}, err
	}
	ordenado := ms(now().Sub(t0))

	churn, err := medirChurnHash(h, idx, f.filas, f.n/20)
	if err != nil {
		h.Close()
		return indexRow{}, err
	}

	st := idx.Stats()
	disco, _ := fileSize(base, storage.SUFFIX_DATA)
	if err := h.Close(); err != nil {
		return indexRow{}, err
	}

	return indexRow{
		n: f.n, tecnica: "Hash dinámico", build: build,
		igualdad: igualdad, rango: rango, ordenado: ordenado, churn: churn,
		disco: disco, memoria: memIdx, nodos: st.Buckets,
	}, nil
}

// ---------------------------------------------------------------------------
// Altas y bajas frecuentes (5% de cada uno) sobre los datos ya cargados
// ---------------------------------------------------------------------------

func medirChurnSecuencial(s *sequential.SequentialFile, idx *bplus.ClusteredIndex, filas []storage.Tuple, ops int) (float64, error) {
	base := int32(900000000)
	nuevas := []storage.Tuple{
		{base + 1, int32(1), "nueva-0000001"},
		{base + 2, int32(2), "nueva-0000002"},
	}
	if ops < len(nuevas) {
		ops = len(nuevas)
	}
	vivas := append([]storage.Tuple(nil), filas...)

	t0 := now()
	for i := 0; i < len(nuevas); i++ {
		if err := idx.Insert(nuevas[i]); err != nil {
			return 0, err
		}
		vivas = append(vivas, nuevas[i])
	}
	for i := 0; i < len(nuevas); i++ {
		if err := idx.Delete(storage.Tuple{nuevas[i][0]}); err != nil {
			return 0, err
		}
	}
	// Unas actualizaciones para que el caso sea mixto, como en el mundo real.
	// En el B+ agrupado una actualización es borrar la clave antigua e insertar
	// la nueva, porque el índice es el propio almacenamiento ordenado.
	for i := 0; i < len(nuevas)*10 && i < ops; i++ {
		j := i % len(vivas)
		prev := append(storage.Tuple(nil), vivas[j]...)
		// El borrado se pide por clave primaria, no por fila completa.
		if err := idx.Delete(storage.Tuple{prev[0]}); err != nil {
			return 0, err
		}
		vivas[j][1] = int32(prev[1].(int32) + 1)
		if err := idx.Insert(vivas[j]); err != nil {
			return 0, err
		}
	}
	if err := s.Sync(); err != nil {
		return 0, err
	}
	return us(now().Sub(t0)) / float64(len(nuevas)*12), nil
}

func medirChurnHeap(h *heap.HeapFile, idx *bplus.UnclusteredIndex, filas []storage.Tuple, ops int) (float64, error) {
	base := int32(900000000)
	vivas := append([]storage.Tuple(nil), filas...)
	rids := make([]storage.RID, 0, ops)

	t0 := now()
	for i := 0; i < ops; i++ {
		row := storage.Tuple{base + int32(i), int32(i % 1000), fmt.Sprintf("churn-%07d", i)}
		rid, err := h.Insert(row)
		if err != nil {
			return 0, err
		}
		if err := idx.InsertRID(row[1], rid); err != nil {
			return 0, err
		}
		rids = append(rids, rid)
	}
	for _, rid := range rids {
		// Para desindexar por RID no hace falta la fila: es lo propio del B+.
		if err := idx.RemoveRID(rid); err != nil {
			return 0, err
		}
		if err := h.Delete(rid); err != nil {
			return 0, err
		}
	}
	// Unas cuantas actualizaciones para que el caso sea mixto.
	for i := 0; i < ops && i < len(vivas); i++ {
		rid := buscarEnHeap(h, vivas[i][0].(int32))
		row, err := h.Get(rid)
		if err != nil {
			continue
		}
		prevKey := row[1]
		row[1] = int32(i % 997)
		if err := h.Update(rid, row); err != nil {
			return 0, err
		}
		if err := idx.ReindexRID(rid, prevKey, row[1]); err != nil {
			return 0, err
		}
	}
	if err := h.Sync(); err != nil {
		return 0, err
	}
	return us(now().Sub(t0)) / float64(ops*3), nil
}

func medirChurnHash(h *heap.HeapFile, idx *extendible.Index, filas []storage.Tuple, ops int) (float64, error) {
	base := int32(900000000)
	vivas := append([]storage.Tuple(nil), filas...)

	t0 := now()
	for i := 0; i < ops; i++ {
		row := storage.Tuple{base + int32(i), int32(i % 1000), fmt.Sprintf("churn-%07d", i)}
		rid, err := h.Insert(row)
		if err != nil {
			return 0, err
		}
		if err := idx.Insert(row[1], rid); err != nil {
			return 0, err
		}
		if _, err := idx.Delete(row[1], rid); err != nil {
			return 0, err
		}
		if err := h.Delete(rid); err != nil {
			return 0, err
		}
	}
	for i := 0; i < ops && i < len(vivas); i++ {
		rid := buscarEnHeap(h, vivas[i][0].(int32))
		row, err := h.Get(rid)
		if err != nil {
			continue
		}
		prevKey := row[1]
		row[1] = int32(i % 991)
		if err := h.Update(rid, row); err != nil {
			return 0, err
		}
		if _, err := idx.Delete(prevKey, rid); err != nil {
			return 0, err
		}
		if err := idx.Insert(row[1], rid); err != nil {
			return 0, err
		}
	}
	if err := h.Sync(); err != nil {
		return 0, err
	}
	return us(now().Sub(t0)) / float64(ops*2), nil
}

// ---------------------------------------------------------------------------
// Adaptadores: los archivos exponen iteradores propios y los índices piden
// callbacks, así que hace falta una capa fina en cada caso.
// ---------------------------------------------------------------------------

// heapStorage expone un HeapFile con la firma que pide el B+ no agrupado.
type heapStorage struct{ h *heap.HeapFile }

func (h *heapStorage) Insert(t storage.Tuple) (storage.RID, error)   { return h.h.Insert(t) }
func (h *heapStorage) Get(rid storage.RID) (storage.Tuple, error)    { return h.h.Get(rid) }
func (h *heapStorage) Update(rid storage.RID, t storage.Tuple) error { return h.h.Update(rid, t) }
func (h *heapStorage) Delete(rid storage.RID) error                  { return h.h.Delete(rid) }
func (h *heapStorage) Open() error                                   { return h.h.Open() }
func (h *heapStorage) Close() error                                  { return h.h.Close() }
func (h *heapStorage) Rows() int64                                   { return h.h.Rows() }
func (h *heapStorage) Scan(fn func(storage.RID, storage.Tuple) bool) error {
	it := h.h.Scan()
	for it.Next() {
		if !fn(it.RID(), it.Tuple()) {
			break
		}
	}
	return it.Err()
}

type seqStorage struct{ f *sequential.SequentialFile }

func (s *seqStorage) Insert(t storage.Tuple) error { return s.f.Insert(t) }
func (s *seqStorage) Search(key storage.Tuple) (storage.Tuple, error) {
	return s.f.Search(key)
}
func (s *seqStorage) Update(key, t storage.Tuple) error { return s.f.Update(key, t) }
func (s *seqStorage) Delete(key storage.Tuple) error    { return s.f.Delete(key) }
func (s *seqStorage) Open() error                       { return s.f.Open() }
func (s *seqStorage) Close() error                      { return s.f.Close() }
func (s *seqStorage) Scan(fn func(storage.Tuple) bool) error {
	it := s.f.Scan()
	for it.Next() {
		if !fn(it.Tuple()) {
			break
		}
	}
	return it.Err()
}

// ---------------------------------------------------------------------------
// Presentación
// ---------------------------------------------------------------------------

func resumenIndices(filas []indexRow) {
	tit := "1.6 · Estructuras de indexación"
	medir := func(unidad, etiqueta string, campo func(indexRow) float64) {
		var et []string
		var v []float64
		for _, f := range filas {
			et = append(et, fmt.Sprintf("%s/%d", f.tecnica, f.n))
			v = append(v, campo(f))
		}
		barras(etiqueta+" ("+unidad+")", et, v, unidad, true)
	}

	medir("ms", "Construcción (cargar n filas con el índice montado)", func(f indexRow) float64 { return f.build })
	medir("ms", "Coste del índice por encima de cargar sin índice", func(f indexRow) float64 { return f.costeIdx })
	medir("µs", "Búsqueda por igualdad exacta", func(f indexRow) float64 { return f.igualdad })
	medir("µs", "Búsqueda por rango (~1% de las claves)", func(f indexRow) float64 { return f.rango })
	medir("ms", "Recorrido completo en orden de clave", func(f indexRow) float64 { return f.ordenado })
	medir("µs", "Alta/baja/update (5% + 5%)", func(f indexRow) float64 { return f.churn })

	for _, n := range uniqueN(filas, func(f indexRow) int { return f.n }) {
		var et []string
		var vDisco, vMem []float64
		for _, f := range filas {
			if f.n != n {
				continue
			}
			et = append(et, f.tecnica)
			vDisco = append(vDisco, float64(f.disco))
			vMem = append(vMem, float64(f.memoria))
		}
		barras(fmt.Sprintf("Espacio en disco de la tabla con %d filas", n), et, vDisco, "B", false)
		barras(fmt.Sprintf("Memoria del índice con %d filas", n), et, vMem, "B", false)
	}

	var cuerpo [][]string
	for _, f := range filas {
		altura := "-"
		if f.altura > 0 {
			altura = i2s(f.altura)
		}
		cuerpo = append(cuerpo, []string{
			i2s(f.n), f.tecnica, fmt.Sprintf("%.1f", f.build), fmt.Sprintf("%.1f", f.costeIdx),
			fmt.Sprintf("%.2f", f.igualdad), fmt.Sprintf("%.1f", f.rango), fmt.Sprintf("%.1f", f.ordenado),
			fmt.Sprintf("%.1f", f.churn), humanBytes(f.disco), humanBytes(int64(f.memoria)), altura,
		})
	}
	tabla(tit, []string{
		"filas", "técnica", "construcción (ms)", "coste índice (ms)", "igualdad (µs)",
		"rango (µs)", "ordenado (ms)", "churn (µs)", "disco", "índice en memoria", "altura",
	}, cuerpo)
}
