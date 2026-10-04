package rtree

import "testing"


func TestSearchRadiusHaversine(t *testing.T) {

}

func TestEuclideanDistance(t *testing.T) {

	a := Point{
		Lat: 0,
		Lon: 0,
	}

	b := Point{
		Lat: 3,
		Lon: 4,
	}

	d := Distance(a, b, Euclidean)

	if d != 5 {
		t.Errorf("Esperado 5, obtenido %f", d)
	}
}