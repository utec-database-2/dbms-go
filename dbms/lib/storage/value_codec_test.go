package storage

import (
	"bytes"
	"testing"
)

func TestValueCodecRoundTrip(t *testing.T) {
	row := Tuple{nil, true, false, int32(-7), int64(1 << 40), 3.5, "hola", []byte{1, 2, 3}}

	buf, err := AppendRow(nil, row)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	got, used, err := DecodeRow(buf)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if used != len(buf) {
		t.Fatalf("consumió %d de %d bytes", used, len(buf))
	}
	if len(got) != len(row) {
		t.Fatalf("longitud: %d != %d", len(got), len(row))
	}
	for i := range row {
		if !sameValue(got[i], row[i]) {
			t.Fatalf("columna %d: %#v != %#v", i, got[i], row[i])
		}
	}
}

// TestValueCodecConservaTipos es la razón de existir del códec: un int32 no
// debe volver como int64 ni un string como []byte, porque el motor compara
// valores por tipo al indexarlos.
func TestValueCodecConservaTipos(t *testing.T) {
	cases := []any{int32(1), int64(1), float64(1), "1", []byte("1"), true, nil}
	for _, want := range cases {
		buf, err := AppendValue(nil, want)
		if err != nil {
			t.Fatalf("%T: %v", want, err)
		}
		got, n, err := DecodeValue(buf)
		if err != nil {
			t.Fatalf("%T: %v", want, err)
		}
		if n != len(buf) {
			t.Fatalf("%T: consumió %d de %d", want, n, len(buf))
		}
		if reflectType(got) != reflectType(want) {
			t.Fatalf("%T volvió como %T", want, got)
		}
	}
}

func TestValueCodecTipoDesconocido(t *testing.T) {
	if _, err := AppendValue(nil, int16(3)); err == nil {
		t.Fatal("aceptó un tipo sin codificación")
	}
	if _, _, err := DecodeValue([]byte{99}); err == nil {
		t.Fatal("aceptó un tag desconocido")
	}
	if _, _, err := DecodeRow([]byte{2, 0, 0, 0, byte(TagInt64)}); err == nil {
		t.Fatal("aceptó una tupla truncada")
	}
}

// TestValueCodecVariosBuffers comprueba que Append funciona sobre un buffer
// existente sin pisar lo anterior (lo usa el log y los runs del sorter).
func TestValueCodecVariosBuffers(t *testing.T) {
	buf := []byte("cabecera")
	buf, err := AppendRow(buf, Tuple{int32(1), "x"})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if !bytes.HasPrefix(buf, []byte("cabecera")) {
		t.Fatal("perdió el prefijo")
	}
	row, used, err := DecodeRow(buf[len("cabecera"):])
	if err != nil || len(row) != 2 {
		t.Fatalf("decode: %v %v", row, err)
	}
	if used != len(buf)-len("cabecera") {
		t.Fatalf("consumió %d bytes", used)
	}
}

func sameValue(a, b any) bool {
	switch av := a.(type) {
	case []byte:
		bv, ok := b.([]byte)
		return ok && bytes.Equal(av, bv)
	default:
		return a == b
	}
}

func reflectType(v any) string {
	if v == nil {
		return "nil"
	}
	switch v.(type) {
	case int32:
		return "int32"
	case int64:
		return "int64"
	case float64:
		return "float64"
	case string:
		return "string"
	case []byte:
		return "bytes"
	case bool:
		return "bool"
	}
	return "otro"
}
