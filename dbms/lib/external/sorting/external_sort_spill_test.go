package sorting

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/dbms-go/v2/dbms/lib/storage"
)

// fila es el tipo que se ordena y se spilled: obliga a que las claves viajen
// junto a la fila y a que la fila vuelva intacta tras el merge.
type fila struct {
	clave  int32
	nombre string
}

func keyOfFila(f fila) (any, error) { return f.clave, nil }

func encodeFila(f fila) ([]byte, error) {
	return storage.AppendRow(nil, storage.Tuple{f.clave, f.nombre})
}

func decodeFila(buf []byte) (fila, error) {
	row, err := storage.DecodeRowBytes(buf)
	if err != nil {
		return fila{}, err
	}
	clave, _ := row[0].(int32)
	nombre, _ := row[1].(string)
	return fila{clave: clave, nombre: nombre}, nil
}

func filasDesordenadas(n int) []fila {
	out := make([]fila, n)
	for i := range out {
		// Claves repetidas a propósito: el merge tiene que ser estable.
		out[i] = fila{clave: int32((n - i) % 7), nombre: fmt.Sprintf("f%04d", i)}
	}
	return out
}

func TestSpillOrdenaIgualQueEnMemoria(t *testing.T) {
	in := filasDesordenadas(500)

	res, err := ExternalSortSpilling(in, keyOfFila, encodeFila, decodeFila,
		SpillOptions{BufferSlots: 32, Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("spill: %v", err)
	}
	if !res.Spilled {
		t.Fatal("no se volcó ningún run a disco")
	}
	if res.Runs < 8 {
		t.Fatalf("runs: %d", res.Runs)
	}
	if len(res.Rows) != len(in) {
		t.Fatalf("filas: %d != %d", len(res.Rows), len(in))
	}

	// Orden no descendente y estable respecto del orden de entrada.
	for i := 1; i < len(res.Rows); i++ {
		if res.Rows[i-1].clave > res.Rows[i].clave {
			t.Fatalf("desordenado en %d: %v", i, res.Rows[i-1:i+1])
		}
		if res.Rows[i-1].clave == res.Rows[i].clave &&
			res.Rows[i-1].nombre > res.Rows[i].nombre {
			t.Fatalf("inestable en %d: %v", i, res.Rows[i-1:i+1])
		}
	}
	if res.BytesWritten <= 0 || res.BytesRead <= 0 {
		t.Fatalf("no se contabilizó la E/S: %+v", res)
	}
}

func TestSpillDescendente(t *testing.T) {
	in := filasDesordenadas(200)
	res, err := ExternalSortSpilling(in, keyOfFila, encodeFila, decodeFila,
		SpillOptions{BufferSlots: 16, Desc: true, Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("spill: %v", err)
	}
	for i := 1; i < len(res.Rows); i++ {
		if res.Rows[i-1].clave < res.Rows[i].clave {
			t.Fatalf("no descendente en %d: %v", i, res.Rows[i-1:i+1])
		}
	}
}

// TestSpillUnSoloRunNoTocaDisco: con todo en un run no hay nada que volcar.
func TestSpillUnSoloRunNoTocaDisco(t *testing.T) {
	dir := t.TempDir()
	res, err := ExternalSortSpilling(filasDesordenadas(20), keyOfFila, encodeFila, decodeFila,
		SpillOptions{BufferSlots: 100, Dir: dir})
	if err != nil {
		t.Fatalf("spill: %v", err)
	}
	if res.Runs != 1 || res.Spilled || res.TempFiles != 0 {
		t.Fatalf("resultado: %+v", res)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("dejó archivos: %#v", ents)
	}
	for i := 1; i < len(res.Rows); i++ {
		if res.Rows[i-1].clave > res.Rows[i].clave {
			t.Fatal("no quedó ordenado")
		}
	}
}

// TestSpillBorraTemporales es esencial: un external sort no puede dejar basura.
func TestSpillBorraTemporales(t *testing.T) {
	dir := t.TempDir()
	res, err := ExternalSortSpilling(filasDesordenadas(300), keyOfFila, encodeFila, decodeFila,
		SpillOptions{BufferSlots: 10, Dir: dir, Prefix: "miorden"})
	if err != nil {
		t.Fatalf("spill: %v", err)
	}
	if res.TempFiles < 2 {
		t.Fatalf("temporales: %d", res.TempFiles)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("dir: %v", err)
	}
	if len(ents) != 0 {
		names := make([]string, 0, len(ents))
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Fatalf("quedaron temporales: %v", names)
	}
}

// TestSpillBorraTemporalesSiFalla comprueba que un error en medio del merge no
// deja archivos en el directorio.
func TestSpillBorraTemporalesSiFalla(t *testing.T) {
	dir := t.TempDir()
	_, err := ExternalSortSpilling(filasDesordenadas(100), keyOfFila, encodeFila,
		func([]byte) (fila, error) { return fila{}, fmt.Errorf("decodificador roto") },
		SpillOptions{BufferSlots: 10, Dir: dir})
	if err == nil {
		t.Fatal("debía fallar al decodificar")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("quedaron temporales tras el error: %#v", ents)
	}
}

func TestSpillSinCdec(t *testing.T) {
	_, err := ExternalSortSpilling(filasDesordenadas(50), keyOfFila, nil, nil,
		SpillOptions{BufferSlots: 10, Dir: t.TempDir()})
	if err == nil {
		t.Fatal("volcó a disco sin códec")
	}
}

// TestSpillNoSpillCoincideConExternalSort verifica que el modo en memoria del
// algoritmo spilled da exactamente el mismo resultado que ExternalSort.
func TestSpillNoSpillCoincideConExternalSort(t *testing.T) {
	in := filasDesordenadas(257)
	mem, runs, err := ExternalSort(in, keyOfFila, false, 16)
	if err != nil {
		t.Fatalf("memoria: %v", err)
	}
	res, err := ExternalSortSpilling(in, keyOfFila, encodeFila, decodeFila,
		SpillOptions{BufferSlots: 16, NoSpill: true})
	if err != nil {
		t.Fatalf("spill nospill: %v", err)
	}
	if res.Spilled || res.Runs != runs {
		t.Fatalf("runs: %d vs %d", res.Runs, runs)
	}
	if len(mem) != len(res.Rows) {
		t.Fatalf("longitudes: %d vs %d", len(mem), len(res.Rows))
	}
	for i := range mem {
		if mem[i] != res.Rows[i] {
			t.Fatalf("fila %d: %v vs %v", i, mem[i], res.Rows[i])
		}
	}
}

// TestSpillDetectaRunCorrupto: un temporal truncado a mitad debe dar error, no
// devolver filas inventadas.
func TestSpillDetectaRunCorrupto(t *testing.T) {
	dir := t.TempDir()
	res, err := ExternalSortSpilling(filasDesordenadas(100), keyOfFila, encodeFila, decodeFila,
		SpillOptions{BufferSlots: 10, Dir: dir, Prefix: "roto"})
	if err != nil {
		t.Fatalf("spill: %v", err)
	}
	if res.TempFiles == 0 {
		t.Fatal("no se volcó nada")
	}

	// Se escribe a mano un run con una entrada incompleta.
	path := filepath.Join(dir, "roto-999.tmp")
	var buf []byte
	var head [8]byte
	binary.LittleEndian.PutUint32(head[0:4], 4)
	binary.LittleEndian.PutUint32(head[4:8], 100) // dice 100 bytes pero no los hay
	buf = append(buf, head[:]...)
	buf = append(buf, 1, 2, 3)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatalf("escribir: %v", err)
	}

	c, err := openRun[fila](path, 0, decodeFila)
	if err != nil {
		t.Fatalf("abrir: %v", err)
	}
	defer c.close()
	if err := c.next(); err == nil {
		t.Fatal("aceptó un run truncado")
	}
}

func TestSpillVacio(t *testing.T) {
	res, err := ExternalSortSpilling([]fila{}, keyOfFila, encodeFila, decodeFila,
		SpillOptions{BufferSlots: 4, Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("spill: %v", err)
	}
	if res.Runs != 0 || len(res.Rows) != 0 {
		t.Fatalf("resultado: %+v", res)
	}
}

func TestSpillClavesDeTexto(t *testing.T) {
	in := []fila{{clave: 1, nombre: "zeta"}, {clave: 2, nombre: "alfa"}, {clave: 3, nombre: "mike"}}
	sortByName := func(f fila) (any, error) { return f.nombre, nil }
	res, err := ExternalSortSpilling(in, sortByName, encodeFila, decodeFila,
		SpillOptions{BufferSlots: 1, Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("spill: %v", err)
	}
	if res.Rows[0].nombre != "alfa" || res.Rows[1].nombre != "mike" || res.Rows[2].nombre != "zeta" {
		t.Fatalf("orden: %#v", res.Rows)
	}
}
