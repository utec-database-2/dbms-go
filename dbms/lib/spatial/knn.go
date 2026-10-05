package spatial

import (
	"fmt"
	"strings"

	"github.com/dbms-go/v2/dbms/lib/index/rtree"
)

// KNNRequest pide los K registros más cercanos a un punto.
type KNNRequest struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	K         int     `json:"k"`
	Metric    string  `json:"metric"`
}

// KNNResult es un vecino encontrado. Rank empieza en 1 (el más cercano).
type KNNResult struct {
	Rank     int     `json:"rank"`
	ID       int     `json:"id"`
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	Distance float64 `json:"distance"`
}

// KNNResponse es la respuesta de una consulta k-NN. MaxDistance es la
// distancia del vecino más lejano devuelto (0 si no hubo resultados), útil
// para dibujar en el mapa el círculo que contiene a los k vecinos.
type KNNResponse struct {
	Center struct {
		Lat float64 `json:"lat"`
		Lon float64 `json:"lon"`
	} `json:"center"`

	K           int         `json:"k"`
	Metric      string      `json:"metric"`
	MaxDistance float64     `json:"maxDistance"`
	Results     []KNNResult `json:"results"`
}

// SearchKNN devuelve los req.K registros más cercanos al punto pedido,
// ordenados de menor a mayor distancia (kilómetros con haversine, grados con
// euclidean). Si hay menos de K registros devuelve todos.
func (s *Store) SearchKNN(req KNNRequest) (KNNResponse, error) {

	// Primera consulta: construimos el índice.
	if !s.ready {
		if err := s.Refresh(); err != nil {
			return KNNResponse{}, err
		}
	}

	if req.K < 1 {
		return KNNResponse{}, fmt.Errorf("k debe ser al menos 1")
	}

	metricName := strings.ToLower(strings.TrimSpace(req.Metric))

	var metric rtree.DistanceMetric

	switch metricName {

	case "haversine":
		metric = rtree.Haversine

	case "euclidean":
		metric = rtree.Euclidean

	default:
		return KNNResponse{}, fmt.Errorf(
			"métrica inválida: use haversine o euclidean",
		)
	}

	center := rtree.Point{
		Lat: req.Latitude,
		Lon: req.Longitude,
	}

	neighbors := s.Tree.KNN(center, req.K, metric)

	response := KNNResponse{
		K:       req.K,
		Metric:  metricName,
		Results: make([]KNNResult, 0, len(neighbors)),
	}

	response.Center.Lat = req.Latitude
	response.Center.Lon = req.Longitude

	for _, n := range neighbors {

		location, ok := s.Locations[n.Entry.RID]

		if !ok {
			continue
		}

		response.Results = append(
			response.Results,
			KNNResult{
				Rank:     len(response.Results) + 1,
				ID:       location.ID,
				Name:     location.Name,
				Type:     location.Type,
				Lat:      location.Lat,
				Lon:      location.Lon,
				Distance: n.Distance,
			},
		)

		response.MaxDistance = n.Distance
	}

	return response, nil
}
