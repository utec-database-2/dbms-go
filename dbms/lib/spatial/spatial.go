package spatial

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dbms-go/v2/dbms/lib/index/rtree"
	"github.com/dbms-go/v2/dbms/lib/sql"
	"github.com/dbms-go/v2/dbms/lib/storage"
)

type Location struct {
	ID   int
	Name string
	Type string
	Lat  float64
	Lon  float64
}

type RangeRequest struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Radius    float64 `json:"radius"`
	Metric    string  `json:"metric"`
}

type RangeResult struct {
	ID       int     `json:"id"`
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	Distance float64 `json:"distance"`
}

type RangeResponse struct {
	Center struct {
		Lat float64 `json:"lat"`
		Lon float64 `json:"lon"`
	} `json:"center"`

	Radius  float64       `json:"radius"`
	Metric  string        `json:"metric"`
	Results []RangeResult `json:"results"`
}

type Store struct {
	DB        *sql.Database
	TableName string

	Tree      *rtree.RTree
	Locations map[storage.RID]Location

	ready bool
}

func NewStore(db *sql.Database, tableName string) *Store {
	return &Store{
		DB:        db,
		TableName: tableName,
	}
}

// Refresh vuelve a construir el R-Tree a partir de la tabla.
func (s *Store) Refresh() error {

	info, ok := s.DB.TableInfo(s.TableName)

	if !ok {
		s.ready = false
		return fmt.Errorf(
			"la tabla %q no existe",
			s.TableName,
		)
	}

	// Buscar posiciones de las columnas.
	idPos := -1
	namePos := -1
	typePos := -1
	latPos := -1
	lonPos := -1

	for i, col := range info.Columns {

		switch strings.ToLower(col.Name) {

		case "id":
			idPos = i

		case "nombre":
			namePos = i

		case "tipo":
			typePos = i

		case "latitud":
			latPos = i

		case "longitud":
			lonPos = i
		}
	}

	if idPos == -1 ||
		namePos == -1 ||
		typePos == -1 ||
		latPos == -1 ||
		lonPos == -1 {

		return fmt.Errorf(
			"la tabla %q debe tener las columnas: id, nombre, tipo, latitud, longitud",
			s.TableName,
		)
	}

	records, err := s.DB.ScanTable(s.TableName)

	if err != nil {
		return err
	}

	tree := rtree.New(4)

	locations := make(map[storage.RID]Location)

	for _, record := range records {

		if len(record.Values) <= lonPos {
			continue
		}

		id, ok := record.Values[idPos].(int)
		if !ok {
			continue
		}

		name, ok := record.Values[namePos].(string)
		if !ok {
			continue
		}

		typ, ok := record.Values[typePos].(string)
		if !ok {
			continue
		}

		lat, ok := numberValue(record.Values[latPos])
		if !ok {
			continue
		}

		lon, ok := numberValue(record.Values[lonPos])
		if !ok {
			continue
		}

		location := Location{
			ID:   id,
			Name: name,
			Type: typ,
			Lat:  lat,
			Lon:  lon,
		}

		locations[record.RID] = location

		tree.Insert(rtree.Entry{
			Point: rtree.Point{
				Lat: lat,
				Lon: lon,
			},
			RID: record.RID,
		})
	}

	s.Tree = tree
	s.Locations = locations
	s.ready = true

	return nil
}

func numberValue(value any) (float64, bool) {

	switch v := value.(type) {

	case int:
		return float64(v), true

	case float64:
		return v, true

	case float32:
		return float64(v), true

	default:
		return 0, false
	}
}

// SearchRange ejecuta una consulta espacial por radio.
func (s *Store) SearchRange(
	req RangeRequest,
) (RangeResponse, error) {

	// Primera consulta:
	// construimos el índice.
	if !s.ready {
		if err := s.Refresh(); err != nil {
			return RangeResponse{}, err
		}
	}

	if req.Radius < 0 {
		return RangeResponse{}, fmt.Errorf(
			"el radio no puede ser negativo",
		)
	}

	var metric rtree.DistanceMetric

	switch strings.ToLower(req.Metric) {

	case "haversine":
		metric = rtree.Haversine

	case "euclidean":
		metric = rtree.Euclidean

	default:
		return RangeResponse{}, fmt.Errorf(
			"métrica inválida: use haversine o euclidean",
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
		Radius:  req.Radius,
		Metric:  strings.ToLower(req.Metric),
		Results: make([]RangeResult, 0),
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
				Type:     location.Type,
				Lat:      location.Lat,
				Lon:      location.Lon,
				Distance: distance,
			},
		)
	}

	sort.Slice(
		response.Results,
		func(i, j int) bool {
			return response.Results[i].Distance <
				response.Results[j].Distance
		},
	)

	return response, nil
}