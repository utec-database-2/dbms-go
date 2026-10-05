package main

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Conducción de PostgreSQL para medir el GiST. Sin PostGIS, una consulta por
// radio se resuelve con la misma estrategia que usa el R-Tree: el índice
// descarta por caja envolvente (operador && sobre una columna box, indexada con
// GiST) y encima se calcula el Haversine exacto, que es lo que decide qué filas
// entran. El k-NN usa el soporte nativo de GiST: ORDER BY pt <-> centro LIMIT k.

// comprobarPG verifica que hay un servidor escuchando en el socket indicado.
func comprobarPG(cfg *config) error {
	cmd := exec.Command("psql", "-h", cfg.pgSocket, "-U", cfg.pgUser, "-d", cfg.pgDB,
		"-tAc", "select 1")
	if out, err := cmd.Output(); err != nil || strings.TrimSpace(string(out)) != "1" {
		return fmt.Errorf("no hay PostgreSQL en el socket %s", cfg.pgSocket)
	}
	return nil
}

// medirGiST carga el dataset en PostgreSQL, crea los índices GiST y mide las
// mismas consultas que las otras dos técnicas.
func medirGiST(cfg *config, n int, puntos, centros []punto) ([]spatialRow, error) {
	if err := comprobarPG(cfg); err != nil {
		return nil, err
	}

	dir := filepath.Join(cfg.dir, "gist")
	if err := mustDir(dir); err != nil {
		return nil, err
	}
	tabla := fmt.Sprintf("pts_%d", n)

	// Los datos van en CSV: es la vía más rápida de meterlos en PostgreSQL.
	datos := filepath.Join(dir, tabla+".csv")
	f, err := os.Create(datos)
	if err != nil {
		return nil, err
	}
	for _, p := range puntos {
		fmt.Fprintf(f, "%d,%.10f,%.10f\n", p.id, p.lat, p.lon)
	}
	if err := f.Close(); err != nil {
		return nil, err
	}

	sql := buildGiSTSQL(tabla, datos, puntos, centros)
	sqlPath := filepath.Join(dir, tabla+".sql")
	if err := os.WriteFile(sqlPath, []byte(sql), 0o644); err != nil {
		return nil, err
	}

	t0 := now()
	out, err := runPSQL(cfg, sqlPath)
	if err != nil {
		return nil, err
	}
	pared := ms(now().Sub(t0))

	valores, resultados, err := parseGiST(out)
	if err != nil {
		return nil, err
	}

	espacio := int64(valores["espacio_bytes"])
	// Los dos GiST hacen falta: el de cajas para el radio y el de puntos para el
	// k-NN. se informa la suma de ambos.
	construccion := valores["construccion_bbox_ms"] + valores["construccion_pt_ms"]
	filas := []spatialRow{{
		n: n, tecnica: tecnicaGiST, consulta: "construccion",
		valor: construccion, espacio: espacio,
	}}
	// El lector entrega la suma de las filas devueltas por todas las consultas;
	// aquí se pasa a promedio por consulta, que es lo que se compara.
	prom := func(clave string) float64 { return resultados[clave] / float64(len(centros)) }

	for _, radio := range []int{1, 5, 10} {
		clave := fmt.Sprintf("radio%d", radio)
		filas = append(filas, spatialRow{
			tecnica: tecnicaGiST, consulta: clave,
			valor: valores[clave+"_ms"], resultados: prom(clave),
		})
	}
	for _, k := range []int{10, 50, 100} {
		clave := fmt.Sprintf("knn%d", k)
		filas = append(filas, spatialRow{
			tecnica: tecnicaGiST, consulta: clave,
			valor: valores[clave+"_ms"], resultados: prom(clave),
		})
	}
	for i := range filas {
		filas[i].n = n
		filas[i].construccion = construccion
		filas[i].espacio = espacio
	}
	fmt.Printf("   GiST: construcción %.0f ms, %d índices, pared completa %.0f ms\n",
		construccion, int(valores["indices"]), pared)
	return filas, nil
}

func runPSQL(cfg *config, archivo string) (string, error) {
	cmd := exec.Command("psql",
		"-h", cfg.pgSocket,
		"-U", cfg.pgUser,
		"-d", cfg.pgDB,
		"-v", "ON_ERROR_STOP=1",
		"-q",
		"-A", "-t",
		"-F", "|",
		"-f", archivo,
	)
	// Con LC_ALL=C psql responde en inglés y con punto decimal, que es lo que
	// espera el lector de tiempos.
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("psql falló (%v): %s", err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// buildGiSTSQL genera el script de una medición: carga los datos, construye los
// índices y lanza cada consulta las veces que se le pidan.
//
// Para que los tiempos no se puedan atribuir al enunciado equivocado, cada
// enunciado que se mide va solo entre \timing on y \timing off, detrás de un
// \echo con su marca. \echo no es una consulta, así que no consume tiempo. El
// resto del script (carga, ANALYZE, tamaño) va con \timing off.
func buildGiSTSQL(tabla, csv string, puntos, centros []punto) string {
	var b strings.Builder
	medir := func(clave, sql string) {
		fmt.Fprintf(&b, "\\timing on\n\\echo marca|%s\n%s\n\\timing off\n", clave, sql)
	}

	fmt.Fprintf(&b, `\timing off
\set ON_ERROR_STOP on
SET jit = off;
SET max_parallel_workers_per_gather = 0;
SET synchronous_commit = off;
DROP TABLE IF EXISTS %s;
CREATE TABLE %s (id int, lat double precision, lon double precision,
                 pt point, bbox box);
\copy %s (id, lat, lon) FROM '%s' WITH (FORMAT csv)
UPDATE %s SET pt = point(lon, lat),
              bbox = box(point(lon - 0.001, lat - 0.001), point(lon + 0.001, lat + 0.001));
`, tabla, tabla, tabla, csv, tabla)

	// Construcción: cada índice GiST se mide por separado porque uno sirve para
	// el radio y el otro para el k-NN, y la suma es lo comparable con el R-Tree.
	medir("construccion_bbox_ms",
		fmt.Sprintf("CREATE INDEX %s_bbox_gist ON %s USING gist (bbox);", tabla, tabla))
	medir("construccion_pt_ms",
		fmt.Sprintf("CREATE INDEX %s_pt_gist ON %s USING gist (pt);", tabla, tabla))

	fmt.Fprintf(&b, "ANALYZE %s;\n", tabla)

	// Espacio en disco de los dos índices.
	fmt.Fprintf(&b, "SELECT 'dato|espacio_bytes|' || (pg_relation_size('%s_bbox_gist') + pg_relation_size('%s_pt_gist'));\n", tabla, tabla)
	fmt.Fprintf(&b, "SELECT 'dato|indices|2';\n")

	// Consultas por radio: caja envolvente (usa el índice) más Haversine exacto.
	for _, radio := range []float64{1, 5, 10} {
		clave := fmt.Sprintf("radio%d_ms", int(radio))
		for _, c := range centros {
			dLat, dLon := cajaRadio(c, radio)
			medir(clave, fmt.Sprintf(
				"SELECT 'filas|%s|' || count(*) FROM %s WHERE bbox && box(point(%.10f, %.10f), point(%.10f, %.10f)) AND 6371.0 * 2 * asin(sqrt(least(1.0, power(sin(radians(lat - %.10f) / 2), 2) + cos(radians(%.10f)) * cos(radians(lat)) * power(sin(radians(lon - %.10f) / 2), 2)))) <= %.10f;",
				clave[:len(clave)-3], tabla,
				c.lon-dLon, c.lat-dLat, c.lon+dLon, c.lat+dLat,
				c.lat, c.lat, c.lon, radio))
		}
	}

	// k vecinos más cercanos con el operador nativo de GiST.
	for _, k := range []int{10, 50, 100} {
		clave := fmt.Sprintf("knn%d_ms", k)
		for _, c := range centros {
			medir(clave, fmt.Sprintf(
				"SELECT 'filas|%s|' || count(*) FROM (SELECT id FROM %s ORDER BY pt <-> point(%.10f, %.10f) LIMIT %d) q;",
				clave[:len(clave)-3], tabla, c.lon, c.lat, k))
		}
	}

	fmt.Fprintf(&b, "DROP TABLE %s;\n", tabla)
	return b.String()
}

// cajaRadio es la proyección del círculo a grados: sirve de prefiltrado, el
// Haversine exacto decide. Es la misma idea que la caja que usa SearchRadius.
func cajaRadio(c punto, radioKm float64) (dLat, dLon float64) {
	dLat = radioKm / 111.32
	cosLat := c.lat * math.Pi / 180
	denom := math.Cos(cosLat)
	if denom < 0.01 {
		denom = 0.01
	}
	dLon = dLat / denom
	if dLon > 180 {
		dLon = 180
	}
	return dLat, dLon
}

// parseGiST lee la salida de psql. El protocolo es deliberadamente simple: una
// línea marca|K, después el tiempo de ese enunciado y opcionalmente una línea
// filas|K|N o dato|K|N. Se comprueba que cada tiempo tenga su marca, para que un
// cambio en el script no pase desapercibido con tiempos mal atribuidos.
func parseGiST(salida string) (valores map[string]float64, resultados map[string]float64, err error) {
	valores = map[string]float64{}
	resultados = map[string]float64{}
	acumulado := map[string]float64{}
	veces := map[string]int{}

	reMarca := regexp.MustCompile(`^marca\|([a-z0-9_]+)$`)
	reFila := regexp.MustCompile(`^filas\|([a-z0-9_]+)\|([0-9]+)$`)
	reDato := regexp.MustCompile(`^dato\|([a-z0-9_]+)\|([0-9.]+)$`)
	// Acepta el formato de psql en inglés y en español, con punto o coma.
	reTiempo := regexp.MustCompile(`^(?:Time|Duraci..n): ([0-9]+)[.,]([0-9]+) ms`)

	pendiente := ""
	tiempoVisto := false

	for _, linea := range strings.Split(salida, "\n") {
		linea = strings.TrimSpace(strings.TrimSuffix(linea, "\r"))
		switch {
		case reMarca.MatchString(linea):
			pendiente = reMarca.FindStringSubmatch(linea)[1]
			tiempoVisto = false

		case reTiempo.MatchString(linea):
			if pendiente == "" {
				// Tiempo de la preparación (SET, CREATE TABLE, \copy, UPDATE).
				// No forma parte de ninguna medición, así que se descarta.
				continue
			}
			if tiempoVisto {
				return nil, nil, fmt.Errorf("la marca %q tiene más de un tiempo: el script y el lector no coinciden", pendiente)
			}
			m := reTiempo.FindStringSubmatch(linea)
			entero, _ := strconv.ParseFloat(m[1], 64)
			decimal, _ := strconv.ParseFloat("0."+m[2], 64)
			acumulado[pendiente] += entero + decimal
			veces[pendiente]++
			tiempoVisto = true

		case reFila.MatchString(linea):
			m := reFila.FindStringSubmatch(linea)
			v, _ := strconv.ParseFloat(m[2], 64)
			if _, ok := resultados[m[1]]; !ok {
				resultados[m[1]] = v
			} else {
				resultados[m[1]] += v
			}

		case reDato.MatchString(linea):
			m := reDato.FindStringSubmatch(linea)
			v, _ := strconv.ParseFloat(m[2], 64)
			valores[m[1]] = v
		}
	}

	// Las consultas se repiten: lo que se informa es el promedio por consulta.
	// La clave ya trae su unidad (radio1_ms), así que se guarda tal cual.
	for clave, total := range acumulado {
		if veces[clave] == 0 {
			continue
		}
		valores[clave] = total / float64(veces[clave])
	}
	if valores["espacio_bytes"] == 0 {
		return nil, nil, fmt.Errorf("PostgreSQL no devolvió el tamaño de los índices (revisa la salida de psql)")
	}
	if len(veces) == 0 {
		return nil, nil, fmt.Errorf("PostgreSQL no devolvió ningún tiempo medido (revisa que \timing esté activo)")
	}
	return valores, resultados, nil
}
