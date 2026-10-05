package main

import (
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/dbms-go/v2/dbms/lib/index/rtree"
	"github.com/dbms-go/v2/dbms/lib/storage"
)

// Comparación 2.2.4: búsqueda secuencial frente al R-Tree del proyecto y el
// GiST de PostgreSQL, con consultas por radio y k vecinos más cercanos sobre
// datasets de 1 000, 10 000 y 100 000 puntos.
//
// Las tres técnicas responden lo mismo y se verifican entre sí: el número de
// filas que devuelve cada una tiene que coincidir, así que la comparación de
// tiempos es sobre respuestas idénticas.

const (
	// Bounding box del dataset: zona de Lima.
	latMin, latMax = -12.10, -11.90
	lonMin, lonMax = -77.20, -76.90
	// Radio de la Tierra en kilómetros: el mismo valor que usa el R-Tree.
	tiendaKm = 6371.0
	// technicianGiST es el nombre con el que aparece GiST en tablas y gráficas.
	tecnicaGiST = "GiST (PostgreSQL)"
)

// spatialRow es una fila de la tabla resumen, en formato largo: una fila por
// consulta y técnica.
type spatialRow struct {
	n            int
	tecnica      string
	consulta     string // construccion, radio1, radio5, radio10, knn10, knn50, knn100
	valor        float64
	resultados   float64 // filas devueltas por consulta, como promedio
	construccion float64 // ms
	espacio      int64   // bytes: memoria del R-Tree, disco del GiST
}

func benchSpatial(cfg *config) error {
	if cfg.queries <= 0 {
		cfg.queries = 100
	}
	fmt.Println("\n== Comparación 2.2.4 · Secuencial vs R-Tree vs GiST (PostgreSQL) ==")
	fmt.Printf("   consultas por medición: %d · dataset: Lima (%.2f..%.2f lat, %.2f..%.2f lon)\n",
		cfg.queries, latMin, latMax, lonMin, lonMax)

	csv, err := newCSV(cfg.out, "espacial.csv",
		"n", "tecnica", "consulta", "valor_ms", "resultados_promedio", "construccion_ms", "espacio_bytes")
	if err != nil {
		return err
	}

	var todas []spatialRow
	for _, n := range cfg.sizes {
		puntos := makePoints(n, cfg.seed)
		centros := makeCenters(cfg.queries, cfg.seed+99)
		fmt.Printf("\n-- %d puntos --\n", n)

		sec, err := medirSecuencial(puntos, centros)
		if err != nil {
			return fmt.Errorf("secuencial con %d puntos: %w", n, err)
		}
		rt, err := medirRTree(puntos, centros)
		if err != nil {
			return fmt.Errorf("R-Tree con %d puntos: %w", n, err)
		}
		// Cada técnica aporta el tamaño del dataset en sus filas.
		for i := range sec {
			sec[i].n = n
		}
		for i := range rt {
			rt[i].n = n
		}
		todas = append(todas, sec...)
		todas = append(todas, rt...)

		if !cfg.noGiST {
			g, err := medirGiST(cfg, n, puntos, centros)
			if err != nil {
				fmt.Printf("   GiST no disponible (%v); la comparación queda con dos técnicas.\n", err)
			} else {
				todas = append(todas, g...)
			}
		}

		if err := verificarResultados(n, todas); err != nil {
			return err
		}
	}

	for _, r := range todas {
		csv.add(i2s(r.n), r.tecnica, r.consulta, f2s(r.valor), f2s(r.resultados),
			f2s(r.construccion), i64s(r.espacio))
	}

	resumenEspacial(todas)
	if err := csv.save(); err != nil {
		return err
	}
	fmt.Printf("\nCSV: %s\n", csv.path)
	return dibujarGraficasEspaciales(cfg.out, todas)
}

// ---------------------------------------------------------------------------
// Dataset
// ---------------------------------------------------------------------------

type punto struct {
	id  int32
	lat float64
	lon float64
}

func (p punto) point() rtree.Point { return rtree.Point{Lat: p.lat, Lon: p.lon} }

// makePoints genera el dataset. Con la misma semilla siempre sale lo mismo,
// que es lo que hace comparables las mediciones.
func makePoints(n int, seed int64) []punto {
	rng := rand.New(rand.NewSource(seed))
	out := make([]punto, n)
	for i := range out {
		out[i] = punto{
			id:  int32(i),
			lat: latMin + rng.Float64()*(latMax-latMin),
			lon: lonMin + rng.Float64()*(lonMax-lonMin),
		}
	}
	return out
}

// makeCenters genera los centros de consulta, con otra semilla para que no
// coincidan con los datos.
func makeCenters(q int, seed int64) []punto {
	return makePoints(q, seed)
}

// haversine es la misma fórmula que usa el R-Tree, para que las tres técnicas
// midan exactamente la misma distancia.
func haversine(a, b rtree.Point) float64 {
	lat1 := a.Lat * math.Pi / 180
	lat2 := b.Lat * math.Pi / 180
	dLat := (b.Lat - a.Lat) * math.Pi / 180
	dLon := (b.Lon - a.Lon) * math.Pi / 180
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1)*math.Cos(lat2)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return tiendaKm * 2 * math.Atan2(math.Sqrt(h), math.Sqrt(1-h))
}

// ---------------------------------------------------------------------------
// Técnica 1: búsqueda secuencial, sin ningún índice
// ---------------------------------------------------------------------------

func medirSecuencial(puntos, centros []punto) ([]spatialRow, error) {
	// Sin índice no hay nada que construir: la técnica secuencial no aparece en
	// la tabla de construcción ni en la de espacio.
	var filas []spatialRow

	for _, radio := range []float64{1, 5, 10} {
		clave := "radio" + strconv.Itoa(int(radio))
		total := time.Duration(0)
		var res int64
		for _, c := range centros {
			cp := c.point()
			t0 := now()
			n := 0
			for _, p := range puntos {
				if haversine(cp, p.point()) <= radio {
					n++
				}
			}
			total += now().Sub(t0)
			res += int64(n)
		}
		filas = append(filas, spatialRow{
			tecnica: "Secuencial", consulta: clave,
			valor:      ms(total) / float64(len(centros)),
			resultados: float64(res) / float64(len(centros)),
		})
	}

	// k vecinos sin índice: recorrido entero quedándose con los k mejores. Es
	// la versión honesta de la consulta sin estructura de acceso.
	for _, k := range []int{10, 50, 100} {
		clave := "knn" + strconv.Itoa(k)
		total := time.Duration(0)
		var res int64
		for _, c := range centros {
			cp := c.point()
			t0 := now()
			mejor := make([]float64, 0, k)
			for _, p := range puntos {
				d := haversine(cp, p.point())
				if len(mejor) < k {
					mejor = append(mejor, d)
					continue
				}
				peor := 0
				for i := range mejor {
					if mejor[i] > mejor[peor] {
						peor = i
					}
				}
				if d < mejor[peor] {
					mejor[peor] = d
				}
			}
			total += now().Sub(t0)
			res += int64(len(mejor))
		}
		filas = append(filas, spatialRow{
			tecnica: "Secuencial", consulta: clave,
			valor:      ms(total) / float64(len(centros)),
			resultados: float64(res) / float64(len(centros)),
		})
	}
	return filas, nil
}

// ---------------------------------------------------------------------------
// Técnica 2: R-Tree del proyecto
// ---------------------------------------------------------------------------

func medirRTree(puntos, centros []punto) ([]spatialRow, error) {
	var tree *rtree.RTree
	var construccion float64
	memoria, err := medirMem(func() error {
		t0 := now()
		tree = rtree.New(rtreeOrder)
		for _, p := range puntos {
			tree.Insert(rtree.Entry{Point: p.point(), RID: storage.RID{Slot: p.id}})
		}
		construccion = ms(now().Sub(t0))
		return nil
	})
	if err != nil {
		return nil, err
	}

	// El R-Tree vive en memoria: su espacio es la memoria que ocupa.
	filas := []spatialRow{{
		tecnica: "R-Tree", consulta: "construccion",
		valor: construccion, espacio: int64(memoria),
	}}

	for _, radio := range []float64{1, 5, 10} {
		clave := "radio" + strconv.Itoa(int(radio))
		total := time.Duration(0)
		var res int64
		for _, c := range centros {
			t0 := now()
			got := tree.SearchRadius(c.point(), radio, rtree.Haversine)
			total += now().Sub(t0)
			res += int64(len(got))
		}
		filas = append(filas, spatialRow{
			tecnica: "R-Tree", consulta: clave,
			valor:      ms(total) / float64(len(centros)),
			resultados: float64(res) / float64(len(centros)),
		})
	}

	// k vecinos en grados euclídeos, que es la distancia nativa del operador
	// <-> de GiST: los dos índices compiten con su mejor distancia.
	for _, k := range []int{10, 50, 100} {
		clave := "knn" + strconv.Itoa(k)
		total := time.Duration(0)
		var res int64
		for _, c := range centros {
			t0 := now()
			vecinos := tree.KNN(c.point(), k, rtree.Euclidean)
			total += now().Sub(t0)
			res += int64(len(vecinos))
		}
		filas = append(filas, spatialRow{
			tecnica: "R-Tree", consulta: clave,
			valor:      ms(total) / float64(len(centros)),
			resultados: float64(res) / float64(len(centros)),
		})
	}

	// La construcción y el espacio se repiten en cada fila para que las gráficas
	// y el CSV los tengan a mano sin volver a mirar el árbol.
	for i := range filas {
		filas[i].construccion = construccion
		filas[i].espacio = int64(memoria)
	}
	return filas, nil
}

// ---------------------------------------------------------------------------
// Presentación
// ---------------------------------------------------------------------------

func resumenEspacial(filas []spatialRow) {
	var cuerpo [][]string
	for _, r := range filas {
		if r.consulta == "construccion" {
			continue
		}
		cuerpo = append(cuerpo, []string{
			i2s(r.n), r.consulta, r.tecnica,
			fmt.Sprintf("%.4f", r.valor),
			fmt.Sprintf("%.1f", r.resultados),
		})
	}
	tabla("2.2.4 · Tiempos por consulta (media de las consultas)", []string{
		"puntos", "consulta", "técnica", "ms por consulta", "filas devueltas",
	}, cuerpo)

	var construccion [][]string
	for _, r := range filas {
		if r.consulta != "construccion" {
			continue
		}
		espacio := "-"
		if r.espacio > 0 {
			espacio = humanBytes(r.espacio)
		}
		construccion = append(construccion, []string{
			i2s(r.n), r.tecnica, fmt.Sprintf("%.1f", r.valor), espacio,
		})
	}
	tabla("2.2.4 · Construcción y espacio", []string{
		"puntos", "técnica", "construcción (ms)", "espacio del índice",
	}, construccion)
	fmt.Println("   La búsqueda secuencial no aparece porque no construye ningún índice.")
	fmt.Println("   El R-Tree ocupa memoria; en GiST el espacio es el de los dos índices (cajas y puntos).")

	// Barras por tipo de consulta.
	for _, g := range []struct {
		clave, etiqueta string
	}{
		{"construccion", "Construcción del índice"},
		{"radio1", "Consulta por radio de 1 km"},
		{"radio5", "Consulta por radio de 5 km"},
		{"radio10", "Consulta por radio de 10 km"},
		{"knn10", "k vecinos más cercanos (k = 10)"},
		{"knn50", "k vecinos más cercanos (k = 50)"},
		{"knn100", "k vecinos más cercanos (k = 100)"},
	} {
		ns := uniqueN(filas, func(f spatialRow) int { return f.n })
		var et []string
		var v []float64
		for _, n := range ns {
			for _, t := range tecnicasPresentes(filas, n) {
				for _, f := range filas {
					if f.n == n && f.tecnica == t && f.consulta == g.clave {
						et = append(et, fmt.Sprintf("%s/%d", t, n))
						v = append(v, f.valor)
					}
				}
			}
		}
		if len(v) > 0 {
			barras(g.etiqueta+" (ms, menos es mejor)", et, v, "ms", true)
		}
	}
}

func tecnicasPresentes(filas []spatialRow, n int) []string {
	var out []string
	visto := map[string]bool{}
	for _, f := range filas {
		if f.n == n && !visto[f.tecnica] {
			visto[f.tecnica] = true
			out = append(out, f.tecnica)
		}
	}
	return out
}

// verificarResultados comprueba que las tres técnicas devuelven el mismo número
// de filas. Si no coinciden, la comparación de tiempos no sería válida.
func verificarResultados(n int, filas []spatialRow) error {
	for _, consulta := range []string{"radio1", "radio5", "radio10", "knn10", "knn50", "knn100"} {
		var tecnica string
		var esperado float64
		for _, f := range filas {
			if f.n != n || f.consulta != consulta {
				continue
			}
			if tecnica == "" {
				tecnica, esperado = f.tecnica, f.resultados
				continue
			}
			if diff := f.resultados - esperado; diff > 1e-6 || diff < -1e-6 {
				return fmt.Errorf("con %d puntos, la consulta %s devuelve %.2f filas en %s y %.2f en %s: las técnicas no están de acuerdo",
					n, consulta, esperado, tecnica, f.resultados, f.tecnica)
			}
		}
	}
	fmt.Printf("   verificado: %s devuelven exactamente el mismo número de filas\n",
		strings.Join(tecnicasPresentes(filas, n), ", "))
	return nil
}
