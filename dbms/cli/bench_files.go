package main

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"

	"github.com/dbms-go/v2/dbms/lib/storage"
	"github.com/dbms-go/v2/dbms/lib/storage/heap"
	"github.com/dbms-go/v2/dbms/lib/storage/sequential"
)

// Comparación 1.6 (gestión de archivos): heap file vs archivo secuencial
// paginado. Para cada tamaño se mide inserción, búsqueda por clave primaria,
// espacio en disco y tiempo de reorganización.

// fileRow es una fila de la tabla resumen de esta comparación.
type fileRow struct {
	n          int
	tecnica    string
	insercion  float64 // ms
	pkBusqueda float64 // µs por consulta
	discoKB    float64
	reorg      float64 // ms de reorganización
	reorgDisco float64 // % de disco recuperado
}

func benchFiles(cfg *config) error {
	if cfg.queries <= 0 {
		cfg.queries = 200
	}
	if err := mustDir(cfg.dir); err != nil {
		return err
	}
	fmt.Println("== Comparación 1.6 · Gestión de archivos: Heap File vs Archivo Secuencial Paginado ==")
	fmt.Printf("   consultas por medición: %d · directorio: %s\n", cfg.queries, cfg.dir)

	csv, err := newCSV(cfg.out, "archivos.csv",
		"n", "tecnica", "insercion_ms", "busqueda_pk_us", "disco_bytes", "reorg_ms", "reorg_espacio_recuperado_pct")
	if err != nil {
		return err
	}

	var todas []fileRow
	for _, n := range cfg.sizes {
		filas := makeRows(n, cfg.seed)
		// Orden de clave aleatorio: es lo que obliga al archivo secuencial a
		// usar su área auxiliar y a reconstruirse, y es el caso realista de una
		// tabla que crece con las claves llegando en desorden.
		insercion := shuffleRows(filas, cfg.seed)
		clavesPK := randomKeys(n, cfg.queries, cfg.seed+7)
		clavesBorrar := randomKeys(n, n/5, cfg.seed+11)

		fmt.Printf("\n-- %d filas --\n", n)

		hp, err := benchHeap(cfg, n, insercion, clavesPK, clavesBorrar)
		if err != nil {
			return fmt.Errorf("heap con %d filas: %w", n, err)
		}
		sp, err := benchSecuencial(cfg, n, insercion, clavesPK, clavesBorrar)
		if err != nil {
			return fmt.Errorf("secuencial con %d filas: %w", n, err)
		}

		todas = append(todas, hp, sp)
		csv.add(i2s(n), hp.tecnica, f2s(hp.insercion), f2s(hp.pkBusqueda),
			i64s(int64(hp.discoKB*1024)), f2s(hp.reorg), f2s(hp.reorgDisco))
		csv.add(i2s(n), sp.tecnica, f2s(sp.insercion), f2s(sp.pkBusqueda),
			i64s(int64(sp.discoKB*1024)), f2s(sp.reorg), f2s(sp.reorgDisco))
	}

	resumenArchivos(todas)
	if err := csv.save(); err != nil {
		return err
	}
	fmt.Printf("\nCSV: %s\n", csv.path)
	return dibujarGraficasArchivos(cfg.out, todas)
}

// ---------------------------------------------------------------------------
// Heap file
// ---------------------------------------------------------------------------

func benchHeap(cfg *config, n int, filas, clavesPK, clavesBorrar []storage.Tuple) (fileRow, error) {
	base := filepath.Join(cfg.dir, fmt.Sprintf("heap_%d", n))
	limpiar(base)

	h, err := heap.New(base, benchTable, heap.Options{})
	if err != nil {
		return fileRow{}, err
	}
	if err := h.Open(); err != nil {
		return fileRow{}, err
	}

	// Inserción: una fila por vez, como haría el motor.
	t0 := now()
	for _, r := range filas {
		if _, err := h.Insert(r); err != nil {
			h.Close()
			return fileRow{}, err
		}
	}
	if err := h.Sync(); err != nil {
		h.Close()
		return fileRow{}, err
	}
	insercion := ms(now().Sub(t0))

	// Búsqueda por clave primaria: el heap no tiene orden, así que la única vía
	// sin índice es recorrerlo entero. Es el coste real de buscar por PK en un
	// heap, y el motivo por el que hace falta un índice.
	t0 = now()
	for _, k := range clavesPK {
		if _, err := h.Get(buscarEnHeap(h, k[0].(int32))); err != nil {
			h.Close()
			return fileRow{}, err
		}
	}
	pkBusqueda := us(now().Sub(t0)) / float64(len(clavesPK))

	// Se borra una quinta parte de las filas para dejar basura y medir la
	// reorganización.
	live := append([]storage.Tuple(nil), filas...)
	for _, k := range clavesBorrar {
		rid := buscarEnHeap(h, k[0].(int32))
		if err := h.Delete(rid); err != nil {
			h.Close()
			return fileRow{}, err
		}
	}
	live = sinClaves(live, clavesBorrar)
	if err := h.Sync(); err != nil {
		h.Close()
		return fileRow{}, err
	}

	antes, err := fileSize(base, storage.SUFFIX_DATA)
	if err != nil {
		h.Close()
		return fileRow{}, err
	}

	// Reorganización del heap: reescribir las filas vivas en un archivo nuevo y
	// quedarse con ese. Es lo que hay que hacer para recuperar el espacio de
	// los huecos que dejan los borrados.
	t0 = now()
	if err := compactarHeap(base, benchTable, live); err != nil {
		h.Close()
		return fileRow{}, err
	}
	reorg := ms(now().Sub(t0))

	despues, err := fileSize(base, storage.SUFFIX_DATA)
	if err != nil {
		h.Close()
		return fileRow{}, err
	}
	if err := h.Close(); err != nil {
		return fileRow{}, err
	}

	return fileRow{
		n:          n,
		tecnica:    "Heap file",
		insercion:  insercion,
		pkBusqueda: pkBusqueda,
		discoKB:    float64(despues) / 1024,
		reorg:      reorg,
		reorgDisco: pct(antes-despues, antes),
	}, nil
}

// buscarEnHeap recorre el heap buscando la fila con esa clave primaria. Devuelve
// un RID vacío si no está (el heap no tiene dónde buscarla directamente).
func buscarEnHeap(h *heap.HeapFile, clave int32) storage.RID {
	var found storage.RID
	it := h.Scan()
	for it.Next() {
		if t, ok := it.Tuple()[0].(int32); ok && t == clave {
			found = it.RID()
			break
		}
	}
	return found
}

// compactarHeap reescribe las filas vivas en un heap nuevo y lo deja en el sitio
// del antiguo.
func compactarHeap(base string, tabla storage.Table, vivas []storage.Tuple) error {
	tmp := base + ".compact"
	limpiar(tmp)

	h, err := heap.New(tmp, tabla, heap.Options{})
	if err != nil {
		return err
	}
	if err := h.Open(); err != nil {
		return err
	}
	for _, r := range vivas {
		if _, err := h.Insert(r); err != nil {
			h.Close()
			return err
		}
	}
	if err := h.Sync(); err != nil {
		h.Close()
		return err
	}
	if err := h.Close(); err != nil {
		return err
	}

	// El rename deja el heap compacto en el sitio del que tenía el heap
	// original, así el resto del benchmark sigue viendo el mismo archivo.
	dst := base + storage.SUFFIX_DATA
	if err := os.Rename(tmp+storage.SUFFIX_DATA, dst); err != nil {
		return fmt.Errorf("no se pudo sustituir el heap por su versión compacta: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Archivo secuencial paginado
// ---------------------------------------------------------------------------

func benchSecuencial(cfg *config, n int, filas, clavesPK, clavesBorrar []storage.Tuple) (fileRow, error) {
	base := filepath.Join(cfg.dir, fmt.Sprintf("seq_%d", n))
	limpiar(base)

	s, err := sequential.New(base, benchTable, sequential.Options{})
	if err != nil {
		return fileRow{}, err
	}
	if err := s.Open(); err != nil {
		return fileRow{}, err
	}

	t0 := now()
	for _, r := range filas {
		if err := s.Insert(r); err != nil {
			s.Close()
			return fileRow{}, err
		}
	}
	if err := s.Sync(); err != nil {
		s.Close()
		return fileRow{}, err
	}
	insercion := ms(now().Sub(t0))

	// Búsqueda por clave primaria: aquí sí hay búsqueda binaria sobre las
	// páginas ordenadas, sin necesidad de ningún índice aparte.
	t0 = now()
	for _, k := range clavesPK {
		if _, err := s.Search(storage.Tuple{k[0]}); err != nil {
			s.Close()
			return fileRow{}, err
		}
	}
	pkBusqueda := us(now().Sub(t0)) / float64(len(clavesPK))

	for _, k := range clavesBorrar {
		if err := s.Delete(storage.Tuple{k[0]}); err != nil && !errors.Is(err, storage.ErrRecordNotFound) {
			s.Close()
			return fileRow{}, err
		}
	}
	if err := s.Sync(); err != nil {
		s.Close()
		return fileRow{}, err
	}

	antes, err := fileSize(base, sequential.SUFFIX_SEQ, sequential.SUFFIX_AUX)
	if err != nil {
		s.Close()
		return fileRow{}, err
	}

	// Reorganización: reconstruir el archivo ordenado principal desde las filas
	// vivas y vaciar el área auxiliar.
	t0 = now()
	if err := s.Rebuild(); err != nil {
		s.Close()
		return fileRow{}, err
	}
	if err := s.Sync(); err != nil {
		s.Close()
		return fileRow{}, err
	}
	reorg := ms(now().Sub(t0))

	despues, err := fileSize(base, sequential.SUFFIX_SEQ, sequential.SUFFIX_AUX)
	if err != nil {
		s.Close()
		return fileRow{}, err
	}
	if err := s.Close(); err != nil {
		return fileRow{}, err
	}

	return fileRow{
		n:          n,
		tecnica:    "Secuencial paginado",
		insercion:  insercion,
		pkBusqueda: pkBusqueda,
		discoKB:    float64(despues) / 1024,
		reorg:      reorg,
		reorgDisco: pct(antes-despues, antes),
	}, nil
}

// ---------------------------------------------------------------------------
// Presentación
// ---------------------------------------------------------------------------

func resumenArchivos(filas []fileRow) {
	cabeceras := []string{"registros", "técnica", "inserción (ms)", "búsqueda PK (µs)", "disco", "reorg (ms)", "espacio recuperado"}
	var cuerpo [][]string
	for _, f := range filas {
		cuerpo = append(cuerpo, []string{
			i2s(f.n), f.tecnica,
			fmt.Sprintf("%.1f", f.insercion),
			fmt.Sprintf("%.1f", f.pkBusqueda),
			humanBytes(int64(f.discoKB * 1024)),
			fmt.Sprintf("%.1f", f.reorg),
			fmt.Sprintf("%.1f%%", f.reorgDisco),
		})
	}
	tabla("1.6 · Gestión de archivos", cabeceras, cuerpo)

	for _, n := range uniqueN(filas, func(f fileRow) int { return f.n }) {
		var et []string
		var insN, pkN, reorgN []float64
		for _, f := range filas {
			if f.n != n {
				continue
			}
			et = append(et, f.tecnica)
			insN = append(insN, f.insercion)
			pkN = append(pkN, f.pkBusqueda)
			reorgN = append(reorgN, f.reorg)
		}
		barras(fmt.Sprintf("Inserción de %d registros (ms, menos es mejor)", n), et, insN, "ms", true)
		barras(fmt.Sprintf("Búsqueda por clave primaria con %d registros (µs, menos es mejor)", n), et, pkN, "µs", true)
		barras(fmt.Sprintf("Reorganización tras borrar el 20%% con %d registros (ms, menos es mejor)", n), et, reorgN, "ms", true)
	}
}

func uniqueN[T any](filas []T, n func(T) int) []int {
	vistos := map[int]bool{}
	var out []int
	for _, f := range filas {
		v := n(f)
		if !vistos[v] {
			vistos[v] = true
			out = append(out, v)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Utilidades
// ---------------------------------------------------------------------------

// randomKeys elige q claves distintas del rango [0, n).
func randomKeys(n, q int, seed int64) []storage.Tuple {
	if q > n {
		q = n
	}
	rng := rand.New(rand.NewSource(seed))
	perm := rng.Perm(n)
	out := make([]storage.Tuple, 0, q)
	for _, p := range perm[:q] {
		out = append(out, storage.Tuple{int32(p)})
	}
	return out
}

// sinClaves quita de vivas las filas cuya PK está en claves.
func sinClaves(vivas, claves []storage.Tuple) []storage.Tuple {
	borrar := make(map[int32]bool, len(claves))
	for _, k := range claves {
		borrar[k[0].(int32)] = true
	}
	out := vivas[:0]
	for _, r := range vivas {
		if !borrar[r[0].(int32)] {
			out = append(out, r)
		}
	}
	return out
}

// limpiar borra los archivos de una base antes de empezar, para no medir sobre
// datos de una ejecución anterior.
func limpiar(base string) {
	for _, s := range []string{
		storage.SUFFIX_DATA,
		sequential.SUFFIX_SEQ,
		sequential.SUFFIX_AUX,
		".compact" + storage.SUFFIX_DATA,
	} {
		_ = os.Remove(base + s)
	}
}

func pct(parte, total int64) float64 {
	if total == 0 {
		return 0
	}
	return float64(parte) / float64(total) * 100
}
