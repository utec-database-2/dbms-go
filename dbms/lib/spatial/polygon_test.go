package spatial

import (
	"reflect"
	"testing"
)

func resultIDs(resp PolygonResponse) []int {
	ids := make([]int, 0, len(resp.Results))
	for _, r := range resp.Results {
		ids = append(ids, r.ID)
	}
	return ids
}

func TestSearchPolygonReturnsPointsInside(t *testing.T) {
	store := newLimaStore(t)

	// Rectángulo que contiene a Tienda Centro (1) y Tienda Cercana (3).
	resp, err := store.SearchPolygon(PolygonRequest{Vertices: []PolygonVertex{
		{Lat: -12.0520, Lon: -77.0400},
		{Lat: -12.0520, Lon: -77.0150},
		{Lat: -12.0300, Lon: -77.0150},
		{Lat: -12.0300, Lon: -77.0400},
	}})
	if err != nil {
		t.Fatalf("SearchPolygon: %v", err)
	}

	if got, want := resultIDs(resp), []int{1, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ids = %v, esperaba %v", got, want)
	}
	if len(resp.Vertices) != 4 {
		t.Errorf("la respuesta debe repetir los 4 vértices consultados, trae %d", len(resp.Vertices))
	}
	if resp.Results[0].Name != "Tienda Centro" || resp.Results[0].Type != "tienda" {
		t.Errorf("datos del primer resultado incorrectos: %+v", resp.Results[0])
	}
}

func TestSearchPolygonCoveringAllAndNone(t *testing.T) {
	store := newLimaStore(t)

	all, err := store.SearchPolygon(PolygonRequest{Vertices: []PolygonVertex{
		{Lat: -13, Lon: -78}, {Lat: -13, Lon: -76}, {Lat: -11, Lon: -76}, {Lat: -11, Lon: -78},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resultIDs(all), []int{1, 2, 3, 4, 5, 6}; !reflect.DeepEqual(got, want) {
		t.Fatalf("polígono grande: ids = %v, esperaba %v", got, want)
	}

	none, err := store.SearchPolygon(PolygonRequest{Vertices: []PolygonVertex{
		{Lat: 10, Lon: 10}, {Lat: 10, Lon: 11}, {Lat: 11, Lon: 10},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(none.Results) != 0 {
		t.Fatalf("polígono vacío de datos devolvió %d resultados", len(none.Results))
	}
}

func TestSearchPolygonConcaveShapeExcludesTheNotch(t *testing.T) {
	store := newLimaStore(t)

	// Una "U" que deja afuera la zona central donde está Tienda Centro (-12.0432, -77.0282).
	resp, err := store.SearchPolygon(PolygonRequest{Vertices: []PolygonVertex{
		{Lat: -12.0800, Lon: -77.0700},
		{Lat: -12.0800, Lon: -77.0000},
		{Lat: -12.0150, Lon: -77.0000},
		{Lat: -12.0150, Lon: -77.0200},
		{Lat: -12.0600, Lon: -77.0200},
		{Lat: -12.0600, Lon: -77.0350},
		{Lat: -12.0150, Lon: -77.0350},
		{Lat: -12.0150, Lon: -77.0700},
	}})
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range resp.Results {
		if r.ID == 1 {
			t.Fatalf("Tienda Centro está en la muesca de la U y no debería aparecer: %v", resultIDs(resp))
		}
	}
	if len(resp.Results) == 0 {
		t.Fatal("la U debería contener al menos un punto")
	}
}

func TestSearchPolygonRejectsInvalidPolygons(t *testing.T) {
	store := newLimaStore(t)

	for name, vertices := range map[string][]PolygonVertex{
		"sin vértices": nil,
		"un vértice":   {{Lat: 1, Lon: 1}},
		"dos vértices": {{Lat: 1, Lon: 1}, {Lat: 2, Lon: 2}},
	} {
		if _, err := store.SearchPolygon(PolygonRequest{Vertices: vertices}); err == nil {
			t.Errorf("%s: debería dar error", name)
		}
	}
}
