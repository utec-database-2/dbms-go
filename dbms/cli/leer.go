package main

import (
	"path/filepath"
	"strconv"
)

// Lectura de los CSV guardados, para poder volver a imprimir o graficar las
// mediciones sin repetirlas.

func leerArchivos(out string) ([]fileRow, error) {
	raw, err := readCSV(filepath.Join(out, "archivos.csv"))
	if err != nil || raw == nil {
		return nil, err
	}
	var out2 []fileRow
	for _, r := range raw[1:] {
		if len(r) < 7 {
			continue
		}
		n, _ := strconv.Atoi(r[0])
		out2 = append(out2, fileRow{
			n:          n,
			tecnica:    r[1],
			insercion:  parseF(r[2]),
			pkBusqueda: parseF(r[3]),
			discoKB:    parseF(r[4]) / 1024,
			reorg:      parseF(r[5]),
			reorgDisco: parseF(r[6]),
		})
	}
	return out2, nil
}

func leerIndices(out string) ([]indexRow, error) {
	raw, err := readCSV(filepath.Join(out, "indices.csv"))
	if err != nil || raw == nil {
		return nil, err
	}
	var res []indexRow
	for _, r := range raw[1:] {
		if len(r) < 13 {
			continue
		}
		n, _ := strconv.Atoi(r[0])
		altura, _ := strconv.Atoi(r[11])
		nodos, _ := strconv.Atoi(r[12])
		res = append(res, indexRow{
			n: n, tecnica: r[1],
			build: parseF(r[2]), sinIdx: parseF(r[3]), costeIdx: parseF(r[4]),
			igualdad: parseF(r[5]), rango: parseF(r[6]), ordenado: parseF(r[7]),
			churn: parseF(r[8]), disco: int64(parseF(r[9])), memoria: uint64(parseF(r[10])),
			altura: altura, nodos: nodos,
		})
	}
	return res, nil
}

func leerEspaciales(out string) ([]spatialRow, error) {
	raw, err := readCSV(filepath.Join(out, "espacial.csv"))
	if err != nil || raw == nil {
		return nil, err
	}
	var res []spatialRow
	for _, r := range raw[1:] {
		if len(r) < 7 {
			continue
		}
		n, _ := strconv.Atoi(r[0])
		res = append(res, spatialRow{
			n: n, tecnica: r[1], consulta: r[2],
			valor: parseF(r[3]), resultados: parseF(r[4]),
			construccion: parseF(r[5]), espacio: int64(parseF(r[6])),
		})
	}
	return res, nil
}

func parseF(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}
