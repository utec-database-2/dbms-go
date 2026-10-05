package spatial

import (
	"errors"
	"fmt"
	"sort"

	"github.com/dbms-go/v2/dbms/lib/index/rtree"
)

// PolygonVertex es un vértice del polígono consultado.
type PolygonVertex struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

// PolygonRequest pide los registros que caen dentro de un polígono.
type PolygonRequest struct {
	Vertices []PolygonVertex `json:"vertices"`
}

// PolygonResult es un registro dentro del polígono.
type PolygonResult struct {
	ID   int     `json:"id"`
	Name string  `json:"name"`
	Type string  `json:"type"`
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
}

// PolygonResponse repite el polígono consultado y lista lo encontrado, ordenado por id.
type PolygonResponse struct {
	Vertices []PolygonVertex `json:"vertices"`
	Results  []PolygonResult `json:"results"`
}

// SearchPolygon devuelve los registros cuyo punto está dentro del polígono
// (borde incluido). El polígono se une solo: no hace falta repetir el primer vértice.
func (s *Store) SearchPolygon(req PolygonRequest) (PolygonResponse, error) {

	if !s.ready {
		if err := s.Refresh(); err != nil {
			return PolygonResponse{}, err
		}
	}

	polygon := make(rtree.Polygon, len(req.Vertices))
	for i, v := range req.Vertices {
		polygon[i] = rtree.Point{Lat: v.Lat, Lon: v.Lon}
	}

	entries, err := s.Tree.SearchPolygon(polygon)
	if err != nil {
		if errors.Is(err, rtree.ErrInvalidPolygon) {
			return PolygonResponse{}, fmt.Errorf("el polígono necesita al menos 3 vértices con latitud y longitud válidas")
		}
		return PolygonResponse{}, err
	}

	response := PolygonResponse{
		Vertices: req.Vertices,
		Results:  make([]PolygonResult, 0, len(entries)),
	}

	for _, entry := range entries {

		location, ok := s.Locations[entry.RID]

		if !ok {
			continue
		}

		response.Results = append(response.Results, PolygonResult{
			ID:   location.ID,
			Name: location.Name,
			Type: location.Type,
			Lat:  location.Lat,
			Lon:  location.Lon,
		})
	}

	sort.Slice(response.Results, func(i, j int) bool {
		return response.Results[i].ID < response.Results[j].ID
	})

	return response, nil
}
