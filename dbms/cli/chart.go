package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Gráficas comparativas en SVG, generadas sin dependencias externas: el objetivo
// es poder abrir los archivos en cualquier navegador o editor vectorial.
//
// Los tiempos abarcan de microsegundos a segundos, así que las gráficas de
// tiempo usan escala logarítmica; los espacios usan escala lineal.

type serie struct {
	nombre  string
	colores []string
}

var paleta = []string{"#2f6fb2", "#d1704a", "#4f9d69", "#8a5fa8", "#b8912f", "#7a7a7a"}

func colorDe(i int) string { return paleta[i%len(paleta)] }

// chartBarras dibuja un gráfico de barras agrupadas: una barra por serie dentro
// de cada categoría.
func chartBarras(path, titulo, ejeX, ejeY string, categorias []string, series []serie, valores [][]float64, log bool) error {
	if len(categorias) == 0 || len(series) == 0 {
		return nil
	}

	const (
		ancho = 900
		alto  = 500
		izq   = 110
		arr   = 70
		// base es el espacio bajo el eje X: etiquetas de categoría, título del
		// eje y leyenda, cada uno en su propia franja para que no se encimen.
		base   = 110
		fuente = 13
	)

	// Escala vertical.
	maxV := 0.0
	for _, fila := range valores {
		for _, v := range fila {
			if v > maxV {
				maxV = v
			}
		}
	}
	if maxV <= 0 {
		maxV = 1
	}
	minimo := 0.0
	if log {
		minV := math.Inf(1)
		for _, fila := range valores {
			for _, v := range fila {
				if v > 0 && v < minV {
					minV = v
				}
			}
		}
		if math.IsInf(minV, 1) {
			minV = 0.1
		}
		// El piso del eje es la potencia de 10 por debajo del menor valor: si
		// fuera el propio mínimo, esa barra quedaría con altura cero.
		minimo = math.Floor(math.Log10(minV))
		if minimo == math.Log10(minV) {
			minimo--
		}
		maxV = math.Log10(maxV)
		if maxV-minimo < 1 {
			maxV = minimo + 1
		}
	}

	anchoPlot := float64(ancho - izq - 40)
	altoPlot := float64(alto - arr - base)

	escalaY := func(v float64) float64 {
		if log {
			if v <= 0 {
				v = math.Pow(10, minimo)
			}
			v = math.Log10(v)
		}
		f := (v - minimo) / (maxV - minimo)
		if f < 0 {
			f = 0
		}
		if f > 1 {
			f = 1
		}
		return float64(alto-base) - f*altoPlot
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" font-family="DejaVu Sans, Helvetica, Arial, sans-serif">`+"\n", ancho, alto, ancho, alto)
	fmt.Fprintf(&b, "  <rect width=\"%d\" height=\"%d\" fill=\"#ffffff\"/>\n", ancho, alto)
	fmt.Fprintf(&b, "  <text x=\"%d\" y=\"28\" font-size=\"17\" font-weight=\"bold\" fill=\"#1c1c1c\">%s</text>\n", izq-60, escape(titulo))
	fmt.Fprintf(&b, "  <text x=\"%d\" y=\"48\" font-size=\"12\" fill=\"#555\">%s</text>\n", izq-60, escape(ejeY))

	// Rejilla y marcas del eje Y.
	pasos := 5
	for i := 0; i <= pasos; i++ {
		frac := float64(i) / float64(pasos)
		y := float64(alto-base) - frac*altoPlot
		v := minimo + frac*(maxV-minimo)
		etiqueta := ""
		if log {
			etiqueta = formatCorta(math.Pow(10, v))
		} else {
			etiqueta = formatCorta(v)
		}
		fmt.Fprintf(&b, "  <line x1=\"%d\" y1=\"%.1f\" x2=\"%d\" y2=\"%.1f\" stroke=\"#e3e3e3\"/>\n", izq, y, ancho-40, y)
		fmt.Fprintf(&b, "  <text x=\"%d\" y=\"%.1f\" font-size=\"11\" fill=\"#555\" text-anchor=\"end\">%s</text>\n", izq-8, y+4, etiqueta)
	}
	fmt.Fprintf(&b, "  <line x1=\"%d\" y1=\"%d\" x2=\"%d\" y2=\"%d\" stroke=\"#888\"/>\n", izq, alto-base, ancho-40, alto-base)

	// Barras agrupadas.
	anchoCat := anchoPlot / float64(len(categorias))
	n := len(series)
	anchoBar := anchoCat * 0.72 / float64(n)
	for ci, cat := range categorias {
		x0 := float64(izq) + float64(ci)*anchoCat + anchoCat*0.14
		for si := range series {
			v := 0.0
			if si < len(valores) && ci < len(valores[si]) {
				v = valores[si][ci]
			}
			y := escalaY(v)
			x := x0 + float64(si)*anchoBar
			fmt.Fprintf(&b, "  <rect x=\"%.1f\" y=\"%.1f\" width=\"%.1f\" height=\"%.1f\" fill=\"%s\" opacity=\"0.85\"/>\n",
				x, y, anchoBar-2, float64(alto-base)-y, colorDe(si))
			fmt.Fprintf(&b, "  <text x=\"%.1f\" y=\"%.1f\" font-size=\"10\" fill=\"#333\" text-anchor=\"middle\">%s</text>\n",
				x+(anchoBar-2)/2, y-4, formatCorta(v))
		}
		fmt.Fprintf(&b, "  <text x=\"%.1f\" y=\"%d\" font-size=\"12\" fill=\"#1c1c1c\" text-anchor=\"middle\">%s</text>\n",
			float64(izq)+float64(ci)*anchoCat+anchoCat/2, alto-base+22, escape(cat))
	}
	fmt.Fprintf(&b, "  <text x=\"%d\" y=\"%d\" font-size=\"12\" fill=\"#555\" text-anchor=\"middle\">%s</text>\n",
		ancho/2, alto-base+44, escape(ejeX))

	// Leyenda.
	lx := izq
	for si, s := range series {
		fmt.Fprintf(&b, "  <rect x=\"%d\" y=\"%d\" width=\"12\" height=\"12\" fill=\"%s\"/>\n", lx, alto-base+62, colorDe(si))
		fmt.Fprintf(&b, "  <text x=\"%d\" y=\"%d\" font-size=\"12\" fill=\"#333\">%s</text>\n", lx+17, alto-base+72, escape(s.nombre))
		lx += 22 + len(s.nombre)*7
		_ = si
	}
	fmt.Fprintf(&b, "  <text x=\"%d\" y=\"%d\" font-size=\"10\" fill=\"#888\">%s</text>\n", izq-60, alto-4, escape(ayudaEscala(log)))
	fmt.Fprintf(&b, "</svg>\n")

	if err := mustDir(filepath.Dir(path)); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func ayudaEscala(log bool) string {
	if log {
		return "escala logarítmica"
	}
	return "escala lineal"
}

func escape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;")
	return r.Replace(s)
}

// formatCorta acorta los números grandes para que quepan en las etiquetas.
func formatCorta(v float64) string {
	a := math.Abs(v)
	switch {
	case a == 0:
		return "0"
	case a >= 1e9:
		return strconv.FormatFloat(v/1e9, 'f', 1, 64) + "G"
	case a >= 1e6:
		return strconv.FormatFloat(v/1e6, 'f', 1, 64) + "M"
	case a >= 1e3:
		return strconv.FormatFloat(v/1e3, 'f', 1, 64) + "k"
	case a >= 1:
		return strconv.FormatFloat(v, 'f', 1, 64)
	case a >= 1e-3:
		return strconv.FormatFloat(v*1e3, 'f', 1, 64) + "m"
	default:
		return strconv.FormatFloat(v*1e6, 'f', 1, 64) + "µ"
	}
}

// ---------------------------------------------------------------------------
// Gráficas concretas de cada comparación
// ---------------------------------------------------------------------------

func dibujarGraficasArchivos(out string, filas []fileRow) error {
	dir := filepath.Join(out, "graficas")
	ns := uniqueN(filas, func(f fileRow) int { return f.n })
	if len(ns) == 0 {
		return nil
	}
	tec := []string{"Heap file", "Secuencial paginado"}
	cats := make([]string, len(ns))
	for i, n := range ns {
		cats[i] = strconv.Itoa(n) + " filas"
	}

	porTecnica := func(campo func(fileRow) float64) [][]float64 {
		out := make([][]float64, len(tec))
		for si, t := range tec {
			for _, n := range ns {
				for _, f := range filas {
					if f.n == n && f.tecnica == t {
						out[si] = append(out[si], campo(f))
					}
				}
			}
		}
		return out
	}
	series := make([]serie, len(tec))
	for i, t := range tec {
		series[i] = serie{nombre: t, colores: []string{colorDe(i)}}
	}

	for _, g := range []struct {
		nombre string
		titulo string
		campo  func(fileRow) float64
		ejeY   string
		log    bool
	}{
		{"insercion", "Inserción de registros", func(f fileRow) float64 { return f.insercion }, "ms", true},
		{"busqueda-pk", "Búsqueda por clave primaria", func(f fileRow) float64 { return f.pkBusqueda }, "µs", true},
		{"disco", "Espacio en disco ocupado", func(f fileRow) float64 { return f.discoKB * 1024 }, "bytes", false},
		{"reorganizacion", "Reorganización tras borrar el 20%", func(f fileRow) float64 { return f.reorg }, "ms", true},
	} {
		if err := chartBarras(filepath.Join(dir, "1-archivos-"+g.nombre+".svg"),
			g.titulo, "registros", g.ejeY, cats, series, porTecnica(g.campo), g.log); err != nil {
			return err
		}
	}
	fmt.Printf("Gráficas: %s\n", dir)
	return nil
}

func dibujarGraficasIndices(out string, filas []indexRow) error {
	dir := filepath.Join(out, "graficas")
	tec := []string{"B+ agrupado", "B+ no agrupado", "Hash dinámico"}
	ns := uniqueN(filas, func(f indexRow) int { return f.n })
	if len(ns) == 0 {
		return nil
	}
	cats := make([]string, len(ns))
	for i, n := range ns {
		cats[i] = strconv.Itoa(n) + " filas"
	}
	series := make([]serie, len(tec))
	for i, t := range tec {
		series[i] = serie{nombre: t, colores: []string{colorDe(i)}}
	}

	porTecnica := func(campo func(indexRow) float64) [][]float64 {
		res := make([][]float64, len(tec))
		for si, t := range tec {
			for _, n := range ns {
				for _, f := range filas {
					if f.n == n && f.tecnica == t {
						res[si] = append(res[si], campo(f))
					}
				}
			}
		}
		return res
	}

	for _, g := range []struct {
		nombre, titulo, ejeY string
		campo                func(indexRow) float64
		log                  bool
	}{
		{"construccion", "Construcción del índice (cargar n filas)", "ms de carga", func(f indexRow) float64 { return f.build }, true},
		{"igualdad", "Búsqueda por igualdad exacta", "µs por consulta", func(f indexRow) float64 { return f.igualdad }, true},
		{"rango", "Búsqueda por rango (~1% de las claves)", "µs por consulta", func(f indexRow) float64 { return f.rango }, true},
		{"ordenado", "Recorrido completo en orden de clave", "ms", func(f indexRow) float64 { return f.ordenado }, true},
		{"churn", "Altas y bajas frecuentes (5% + 5%)", "µs por operación", func(f indexRow) float64 { return f.churn }, true},
		{"memoria", "Memoria del índice", "bytes", func(f indexRow) float64 { return float64(f.memoria) }, false},
		{"disco", "Espacio en disco de la tabla", "bytes", func(f indexRow) float64 { return float64(f.disco) }, false},
	} {
		if err := chartBarras(filepath.Join(dir, "1-indices-"+g.nombre+".svg"),
			g.titulo, "registros", g.ejeY, cats, series, porTecnica(g.campo), g.log); err != nil {
			return err
		}
	}
	fmt.Printf("Gráficas: %s\n", dir)
	return nil
}

// ---------------------------------------------------------------------------
// Regenerar todas las gráficas desde los CSV guardados
// ---------------------------------------------------------------------------

func dibujarGraficasEspaciales(out string, filas []spatialRow) error {
	dir := filepath.Join(out, "graficas")
	tec := []string{"Secuencial", "R-Tree", "GiST (PostgreSQL)"}
	ns := uniqueN(filas, func(f spatialRow) int { return f.n })
	if len(ns) == 0 {
		return nil
	}
	cats := make([]string, len(ns))
	for i, n := range ns {
		cats[i] = strconv.Itoa(n) + " puntos"
	}
	series := make([]serie, len(tec))
	for i, t := range tec {
		series[i] = serie{nombre: t, colores: []string{colorDe(i)}}
	}

	// Un gráfico por (consulta, técnica): los resultados están en formato largo.
	tipos := []struct {
		clave    string
		etiqueta string
		titulo   string
	}{
		{"radio1", "radio 1 km", "Búsqueda por radio de 1 km"},
		{"radio5", "radio 5 km", "Búsqueda por radio de 5 km"},
		{"radio10", "radio 10 km", "Búsqueda por radio de 10 km"},
		{"knn10", "k=10", "k vecinos más cercanos (k = 10)"},
		{"knn50", "k=50", "k vecinos más cercanos (k = 50)"},
		{"knn100", "k=100", "k vecinos más cercanos (k = 100)"},
	}
	presentes := map[string]bool{}
	for _, f := range filas {
		presentes[f.consulta] = true
	}
	for _, t := range tipos {
		if !presentes[t.clave] {
			continue
		}
		vals := make([][]float64, len(tec))
		for si, tecnica := range tec {
			for _, n := range ns {
				v := 0.0
				for _, f := range filas {
					if f.n == n && f.tecnica == tecnica && f.consulta == t.clave {
						v = f.valor
					}
				}
				vals[si] = append(vals[si], v)
			}
		}
		if err := chartBarras(filepath.Join(dir, "2-espacial-"+t.clave+".svg"),
			t.titulo, "puntos", "ms por consulta (media de 100)", cats, series, vals, true); err != nil {
			return err
		}
	}

	// Construcción y espacio.
	for _, g := range []struct {
		nombre, titulo, ejeY string
		campo                func(spatialRow) float64
		log                  bool
	}{
		{"construccion", "Construcción del índice", "ms", func(f spatialRow) float64 { return f.construccion }, true},
		{"espacio", "Espacio del índice", "bytes", func(f spatialRow) float64 { return float64(f.espacio) }, false},
	} {
		vals := make([][]float64, len(tec))
		for si, tecnica := range tec {
			for _, n := range ns {
				v := 0.0
				for _, f := range filas {
					if f.n == n && f.tecnica == tecnica && f.consulta == "construccion" {
						v = g.campo(f)
					}
				}
				vals[si] = append(vals[si], v)
			}
		}
		if err := chartBarras(filepath.Join(dir, "2-espacial-"+g.nombre+".svg"),
			g.titulo, "puntos", g.ejeY, cats, series, vals, g.log); err != nil {
			return err
		}
	}
	fmt.Printf("Gráficas: %s\n", dir)
	return nil
}

func drawAllCharts(out string) error {
	if _, err := readCSV(filepath.Join(out, "archivos.csv")); err == nil {
		filas, err := leerArchivos(out)
		if err != nil {
			return err
		}
		if len(filas) > 0 {
			if err := dibujarGraficasArchivos(out, filas); err != nil {
				return err
			}
		}
	}
	if filas, err := leerIndices(out); err == nil && len(filas) > 0 {
		if err := dibujarGraficasIndices(out, filas); err != nil {
			return err
		}
	}
	if filas, err := leerEspaciales(out); err == nil && len(filas) > 0 {
		if err := dibujarGraficasEspaciales(out, filas); err != nil {
			return err
		}
	}
	return nil
}
