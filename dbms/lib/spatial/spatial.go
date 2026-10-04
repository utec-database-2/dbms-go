package spatial

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dbms-go/v2/dbms/lib/index/rtree"
	"github.com/dbms-go/v2/dbms/lib/storage"
)

// Location representa un punto geográfico.
type Location struct {
	ID   int     `json:"id"`
	Name string  `json:"name"`
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
}

// RangeRequest representa una consulta espacial por radio.
type RangeRequest struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Radius    float64 `json:"radius"`
	Metric    string  `json:"metric"`
}

// RangeResult representa un resultado.
type RangeResult struct {
	ID       int     `json:"id"`
	Name     string  `json:"name"`
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	Distance float64 `json:"distance"`
}

// RangeResponse es la respuesta completa de la consulta.
type RangeResponse struct {
	Center struct {
		Lat float64 `json:"lat"`
		Lon float64 `json:"lon"`
	} `json:"center"`

	Radius  float64       `json:"radius"`
	Metric  string        `json:"metric"`
	Results []RangeResult `json:"results"`
}

// Store administra los datos espaciales.
type Store struct {
	Tree      *rtree.RTree
	Locations map[storage.RID]Location
}

// NewDemoStore crea un R-Tree con puntos de prueba.
//
// Más adelante estos puntos pueden reemplazarse
// por datos provenientes de una tabla.
func NewDemoStore() *Store {
	store := &Store{
		Tree:      rtree.New(4),
		Locations: make(map[storage.RID]Location),
	}

	locations := []Location{
		{
			ID:   1,
			Name: "Tienda Centro",
			Lat:  -12.0432,
			Lon:  -77.0282,
		},
		{
			ID:   2,
			Name: "Tienda Norte",
			Lat:  -12.0200,
			Lon:  -77.0282,
		},
		{
			ID:   3,
			Name: "Tienda Cercana",
			Lat:  -12.0500,
			Lon:  -77.0282,
		},
		{
			ID:   4,
			Name: "Tienda Sur",
			Lat:  -12.0700,
			Lon:  -77.0282,
		},
		{
			ID:   5,
			Name: "Tienda Lejana",
			Lat:  -12.1060,
			Lon:  -77.0282,
		},
		{
			ID:   6,
			Name: "Tienda Oeste",
			Lat:  -12.0432,
			Lon:  -77.0600,
		},
	}

	for _, location := range locations {

		rid := storage.RID{
			PageID: uint32(location.ID),
			SlotID: 0,
		}

		store.Locations[rid] = location

		store.Tree.Insert(rtree.Entry{
			Point: rtree.Point{
				Lat: location.Lat,
				Lon: location.Lon,
			},
			RID: rid,
		})
	}

	return store
}

// SearchRange realiza una consulta por radio.
func (s *Store) SearchRange(req RangeRequest) (RangeResponse, error) {

	if req.Radius < 0 {
		return RangeResponse{}, fmt.Errorf(
			"el radio no puede ser negativo",
		)
	}

	metricName := strings.ToLower(strings.TrimSpace(req.Metric))

	var metric rtree.DistanceMetric

	switch metricName {

	case "haversine":
		metric = rtree.Haversine

	case "euclidean":
		metric = rtree.Euclidean

	default:
		return RangeResponse{}, fmt.Errorf(
			"métrica no válida: use \"haversine\" o \"euclidean\"",
		)
	}

	center := rtree.Point{
		Lat: req.Latitude,
		Lon: req.Longitude,
	}

	entries := s.Tree.SearchRadius(
		center,
		req.Radius,
		metric,
	)

	response := RangeResponse{
		Radius: req.Radius,
		Metric: metricName,
		Results: make([]RangeResult, 0, len(entries)),
	}

	response.Center.Lat = req.Latitude
	response.Center.Lon = req.Longitude

	for _, entry := range entries {

		location, ok := s.Locations[entry.RID]

		if !ok {
			continue
		}

		distance := rtree.Distance(
			center,
			entry.Point,
			metric,
		)

		response.Results = append(
			response.Results,
			RangeResult{
				ID:       location.ID,
				Name:     location.Name,
				Lat:      location.Lat,
				Lon:      location.Lon,
				Distance: distance,
			},
		)
	}

	// Ordenamos para que el resultado sea fácil de leer.
	sort.Slice(
		response.Results,
		func(i, j int) bool {
			return response.Results[i].Distance <
				response.Results[j].Distance
		},
	)

	return response, nil
}