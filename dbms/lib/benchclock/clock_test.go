package benchclock

import (
	"sort"
	"testing"
	"time"
)

func TestClock(t *testing.T) {
	// El coste observado incluye leer el reloj. No se resta de las consultas.
	const n = 10000
	samples := make([]time.Duration, n)
	var minPositive time.Duration
	zeros := 0
	for i := range samples {
		start := Now()
		d := Since(start)
		if d < 0 {
			t.Fatalf("reloj retrocedió: %s", d)
		}
		samples[i] = d
		if d == 0 {
			zeros++
		} else if minPositive == 0 || d < minPositive {
			minPositive = d
		}
	}
	if minPositive == 0 {
		t.Fatal("ninguna lectura positiva")
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	t.Logf("reloj=%s; lectura mínima positiva=%s; mediana lectura=%s; ceros=%d/%d", Source(), minPositive, samples[n/2], zeros, n)
	start := Now()
	time.Sleep(30 * time.Millisecond) // solamente prueba
	d := Since(start)
	if d < 25*time.Millisecond {
		t.Fatalf("intervalo de prueba demasiado corto: %s", d)
	}
}
