package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// pointLiteralFromCell convierte una celda del CSV en el literal POINT(lat, lon)
// del SQL. Acepta "lat lon", "lat;lon", "lat|lon", "lat,lon" (la celda entre
// comillas en el CSV) y "POINT(lat, lon)".
func pointLiteralFromCell(raw string) (string, error) {
	s := strings.TrimSpace(raw)

	if len(s) >= 6 && strings.EqualFold(s[:5], "point") {
		inner := strings.TrimSpace(s[5:])
		if !strings.HasPrefix(inner, "(") || !strings.HasSuffix(inner, ")") {
			return "", fmt.Errorf("%q no es un POINT(lat, lon) válido", raw)
		}
		s = inner[1 : len(inner)-1]
	}

	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == '|' || r == ' ' || r == '\t'
	})
	if len(fields) != 2 {
		return "", fmt.Errorf("%q no es un punto: se esperaba \"lat lon\"", raw)
	}

	lat, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || math.IsNaN(lat) || math.IsInf(lat, 0) {
		return "", fmt.Errorf("latitud inválida %q", fields[0])
	}
	lon, err := strconv.ParseFloat(fields[1], 64)
	if err != nil || math.IsNaN(lon) || math.IsInf(lon, 0) {
		return "", fmt.Errorf("longitud inválida %q", fields[1])
	}
	if lat < -90 || lat > 90 {
		return "", fmt.Errorf("la latitud %v está fuera de [-90, 90]", lat)
	}
	if lon < -180 || lon > 180 {
		return "", fmt.Errorf("la longitud %v está fuera de [-180, 180]", lon)
	}

	return "POINT(" +
		strconv.FormatFloat(lat, 'f', -1, 64) + ", " +
		strconv.FormatFloat(lon, 'f', -1, 64) + ")", nil
}
