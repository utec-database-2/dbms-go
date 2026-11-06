package metric

import (
	"errors"
	"math"
	"testing"
)

func closeTo(got, want, tol float64) bool {
	return math.Abs(got-want) <= tol
}

func TestDistance(t *testing.T) {
	tests := []struct {
		name   string
		a, b   []float64
		metric Metric
		want   float64
	}{
		{"euclidiana", []float64{0, 0}, []float64{3, 4}, Euclidean, 5},
		{"coseno ortogonal", []float64{1, 0}, []float64{0, 1}, Cosine, 1},
		{"coseno idéntico", []float64{1, 0}, []float64{2, 0}, Cosine, 0},
		{"producto punto", []float64{1, 2}, []float64{3, 4}, DotProduct, -11},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Distance(tt.a, tt.b, tt.metric); !closeTo(got, tt.want, 1e-9) {
				t.Fatalf("Distance = %v, se esperaba %v", got, tt.want)
			}
		})
	}
}

func TestDistanceFloat32(t *testing.T) {
	got := Distance([]float32{0, 0}, []float32{3, 4}, Euclidean)
	if !closeTo(got, 5, 1e-6) {
		t.Fatalf("Distance float32 = %v, se esperaba 5", got)
	}
}

func TestRankingAndFinalDistance(t *testing.T) {
	if got := RankingDistance([]float64{0, 0}, []float64{3, 4}, Euclidean); !closeTo(got, 25, 1e-9) {
		t.Fatalf("ranking euclidiano = %v, se esperaba 25", got)
	}
	if got := FinalDistance(25, Euclidean); !closeTo(got, 5, 1e-9) {
		t.Fatalf("distancia final = %v, se esperaba 5", got)
	}
	if got := RankingDistance([]float64{1, 0}, []float64{0, 1}, Cosine); !closeTo(got, 1, 1e-9) {
		t.Fatalf("ranking coseno = %v, se esperaba 1", got)
	}
	if got := FinalDistance(1, Cosine); !closeTo(got, 1, 1e-9) {
		t.Fatalf("final coseno = %v, se esperaba 1", got)
	}
}

func TestValidate(t *testing.T) {
	for _, m := range []Metric{Euclidean, Cosine, DotProduct} {
		if err := Validate(m); err != nil {
			t.Fatalf("Validate(%v): %v", m, err)
		}
		if !IsValid(m) {
			t.Fatalf("IsValid(%v) = false", m)
		}
	}
	if err := Validate(Metric(99)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Validate(99) = %v, se esperaba ErrInvalid", err)
	}
	if IsValid(Metric(99)) {
		t.Fatal("IsValid(99) = true")
	}
}

func TestString(t *testing.T) {
	if got := Euclidean.String(); got != "euclidean" {
		t.Fatalf("Euclidean.String() = %q", got)
	}
	if got := Metric(99).String(); got != "metric(99)" {
		t.Fatalf("Metric(99).String() = %q", got)
	}
}
