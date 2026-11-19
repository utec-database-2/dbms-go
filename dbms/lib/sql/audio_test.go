package sql

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTestWav genera un WAV PCM mono de 16 bits (16 kHz, 1.0 s) donde la
// fracción pctA de los frames contiene frecuencia fA y el resto frecuencia fB.
// Esto produce histogramas BoAW proporcionales a (pctA, 1-pctA) bajo K-Means.
func writeTestWav(t *testing.T, dir, name string, fA, fB float64, pctA int) string {
	t.Helper()

	const (
		sampleRate = 16000
		numSamples = sampleRate // 1.0 segundo (~60 frames MFCC)
		dataSize   = numSamples * 2
	)

	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var header [44]byte
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], uint32(36+dataSize))
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16)
	binary.LittleEndian.PutUint16(header[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(header[22:24], 1) // Mono
	binary.LittleEndian.PutUint32(header[24:28], sampleRate)
	binary.LittleEndian.PutUint32(header[28:32], sampleRate*2)
	binary.LittleEndian.PutUint16(header[32:34], 2)
	binary.LittleEndian.PutUint16(header[34:36], 16)
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], uint32(dataSize))

	if _, err := f.Write(header[:]); err != nil {
		t.Fatal(err)
	}

	split := (numSamples * pctA) / 100
	buf := make([]byte, dataSize)
	for i := 0; i < numSamples; i++ {
		freq := fA
		if i >= split {
			freq = fB
		}
		tm := float64(i) / float64(sampleRate)
		sample := int16(0.7 * math.Sin(2*math.Pi*freq*tm) * 32767)
		binary.LittleEndian.PutUint16(buf[i*2:i*2+2], uint16(sample))
	}
	if _, err := f.Write(buf); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAudioIndexKNN(t *testing.T) {
	audios := t.TempDir()
	dbDir := t.TempDir()

	// Dos familias espectrales separadas: graves (180 Hz, 750 Hz) vs agudos (2600 Hz, 6200 Hz)
	a1 := writeTestWav(t, audios, "a1.wav", 180, 750, 100)
	a2 := writeTestWav(t, audios, "a2.wav", 180, 750, 0)
	b1 := writeTestWav(t, audios, "b1.wav", 2600, 6200, 100)
	b2 := writeTestWav(t, audios, "b2.wav", 2600, 6200, 0)
	q := writeTestWav(t, audios, "q.wav", 180, 750, 65)

	e, err := Open("app", Options{Dir: dbDir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if e != nil {
			e.Close()
		}
	}()

	run(t, e, "CREATE TABLE canciones (id INT PRIMARY KEY, ruta VARCHAR(255))")

	// Sin filas no hay con qué entrenar el codebook.
	if _, err := e.Exec("CREATE INDEX idx_aud ON canciones USING AUDIO (ruta) WITH (k = 8)"); err == nil {
		t.Fatal("CREATE INDEX AUDIO sobre una tabla vacía debería fallar")
	}

	for i, p := range []string{a1, a2, b1, b2} {
		run(t, e, fmt.Sprintf("INSERT INTO canciones (id, ruta) VALUES (%d, '%s')", i+1, p))
	}

	res := run(t, e, "CREATE INDEX idx_aud ON canciones USING AUDIO (ruta) WITH (k = 4, nlist = 2, nprobe = 2)")
	if !strings.Contains(strings.ToLower(res.Message), "audio") {
		t.Fatalf("CREATE INDEX no creó un índice AUDIO: %s", res.Message)
	}
	codebook := audioCodebookPath(dbDir, "canciones", "idx_aud")
	if _, err := os.Stat(codebook); err != nil {
		t.Fatalf("falta el archivo del codebook: %v", err)
	}

	query := fmt.Sprintf("SELECT id FROM canciones ORDER BY vector_distance(ruta, AUDIO_VECTOR('%s')) LIMIT 2", q)
	res = run(t, e, query)
	if got := ids(res); !equalStrings(got, []string{"1", "2"}) {
		t.Fatalf("kNN de audio: se esperaba [1 2], se obtuvo %v", got)
	}
	if !hasKind(res, StepIndexScan) || !strings.Contains(planText(res), "AUDIO") {
		t.Fatalf("el kNN no usó el índice AUDIO:\n%s", planText(res))
	}

	// Un audio insertado después del índice entra con el codebook existente.
	c1 := writeTestWav(t, audios, "c1.wav", 180, 750, 65)
	run(t, e, fmt.Sprintf("INSERT INTO canciones (id, ruta) VALUES (5, '%s')", c1))
	res = run(t, e, fmt.Sprintf("SELECT id FROM canciones ORDER BY vector_distance(ruta, AUDIO_VECTOR('%s')) LIMIT 1", q))
	if got := ids(res); !equalStrings(got, []string{"5"}) {
		t.Fatalf("el audio nuevo debía ser el más cercano: %v", got)
	}

	// Una ruta inexistente se rechaza.
	if _, err := e.Exec("INSERT INTO canciones (id, ruta) VALUES (6, '/no/existe.wav')"); err == nil {
		t.Fatal("INSERT con un audio ilegible debería fallar")
	}

	// vector_distance fuera del atajo de índice (con WHERE) también entiende rutas.
	res = run(t, e, fmt.Sprintf(
		"SELECT id FROM canciones WHERE id <= 4 ORDER BY vector_distance(ruta, AUDIO_VECTOR('%s')) LIMIT 2", q))
	if got := ids(res); !equalStrings(got, []string{"1", "2"}) {
		t.Fatalf("kNN con WHERE: %v", got)
	}

	// Reabrir reutiliza el codebook guardado en disco.
	e.Close()
	e = nil
	e, err = Open("app", Options{Dir: dbDir})
	if err != nil {
		t.Fatal(err)
	}
	res = run(t, e, query)
	if got := ids(res); len(got) != 2 || !(got[0] == "5" || got[0] == "1" || got[0] == "2") {
		t.Fatalf("kNN tras reabrir: %v", got)
	}

	run(t, e, "DROP INDEX idx_aud ON canciones")
	if _, err := os.Stat(codebook); !os.IsNotExist(err) {
		t.Fatalf("el codebook debía borrarse con el índice (err=%v)", err)
	}
}

func TestAudioVectorNeedsIndex(t *testing.T) {
	e := newTestEngine(t, Options{})
	run(t, e, "CREATE TABLE canciones (id INT PRIMARY KEY, ruta VARCHAR(255))")
	run(t, e, "INSERT INTO canciones (id, ruta) VALUES (1, 'x')")
	if _, err := e.Exec("SELECT AUDIO_VECTOR('x') FROM canciones"); err == nil ||
		!strings.Contains(err.Error(), "AUDIO") {
		t.Fatalf("AUDIO_VECTOR sin índice debía explicar qué falta: %v", err)
	}
}

func TestAudioIndexRequiresPathColumn(t *testing.T) {
	e := newTestEngine(t, Options{})
	run(t, e, "CREATE TABLE t (id INT PRIMARY KEY, n INT)")
	run(t, e, "INSERT INTO t (id, n) VALUES (1, 1)")
	if _, err := e.Exec("CREATE INDEX i ON t USING AUDIO (n)"); err == nil {
		t.Fatal("AUDIO sobre una columna INT debería fallar")
	}
}

func TestAudioIndexRejectsUnique(t *testing.T) {
	e := newTestEngine(t, Options{})
	run(t, e, "CREATE TABLE t (id INT PRIMARY KEY, ruta VARCHAR(255))")
	if _, err := e.Exec("CREATE UNIQUE INDEX i ON t USING AUDIO (ruta)"); err == nil {
		t.Fatal("AUDIO no debería permitir UNIQUE")
	}
}
