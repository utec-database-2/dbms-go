package storage

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Códec de valores para serializar filas sin esquema.
//
// TupleCodec (ver tuple.go) conoce el esquema de la tabla, así que solo sabe
// codificar los tipos que ese esquema declara. En cambio el log de
// transacciones (write-ahead log) y los runs spilled del external sorting
// necesitan guardar valores cuyo tipo solo conoce el propio dato, porque el
// registro se escribe sin tener una tabla a la que preguntarle. Este códec
// resuelve eso: cada valor se etiqueta con su tipo, de modo que al decodificar
// se obtiene exactamente el mismo valor de Go.

// ValueTag identifica el tipo de un valor codificado.
type ValueTag byte

const (
	TagNil    ValueTag = 0
	TagBool   ValueTag = 1
	TagInt32  ValueTag = 2
	TagInt64  ValueTag = 3
	TagFloat  ValueTag = 4
	TagString ValueTag = 5
	TagBytes  ValueTag = 6
)

// ErrCodec indica un valor o una secuencia de bytes que no se puede decodificar.
var ErrCodec = fmt.Errorf("storage: valor no codificable")

// AppendValue escribe v al final de dst y devuelve el buffer extendido. Tener
// un único punto de codificación evita que el log y los runs del sorter
// discrepen sobre el formato.
func AppendValue(dst []byte, v any) ([]byte, error) {
	switch val := v.(type) {
	case nil:
		return append(dst, byte(TagNil)), nil
	case bool:
		if val {
			return append(dst, byte(TagBool), 1), nil
		}
		return append(dst, byte(TagBool), 0), nil
	case int32:
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(val))
		return append(append(dst, byte(TagInt32)), b[:]...), nil
	case int64:
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], uint64(val))
		return append(append(dst, byte(TagInt64)), b[:]...), nil
	case float64:
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], math.Float64bits(val))
		return append(append(dst, byte(TagFloat)), b[:]...), nil
	case string:
		return appendBlob(dst, byte(TagString), []byte(val)), nil
	case []byte:
		return appendBlob(dst, byte(TagBytes), val), nil
	}
	return nil, fmt.Errorf("%w: %T no tiene codificación", ErrCodec, v)
}

// DecodeValue lee un valor de buf y devuelve el valor con los bytes consumidos.
func DecodeValue(buf []byte) (any, int, error) {
	if len(buf) == 0 {
		return nil, 0, fmt.Errorf("%w: registro truncado antes del tag", ErrCodec)
	}
	tag := ValueTag(buf[0])
	rest := buf[1:]
	switch tag {
	case TagNil:
		return nil, 1, nil
	case TagBool:
		if len(rest) < 1 {
			return nil, 0, truncated(tag)
		}
		return rest[0] != 0, 2, nil
	case TagInt32:
		if len(rest) < 4 {
			return nil, 0, truncated(tag)
		}
		return int32(binary.LittleEndian.Uint32(rest[:4])), 5, nil
	case TagInt64:
		if len(rest) < 8 {
			return nil, 0, truncated(tag)
		}
		return int64(binary.LittleEndian.Uint64(rest[:8])), 9, nil
	case TagFloat:
		if len(rest) < 8 {
			return nil, 0, truncated(tag)
		}
		return math.Float64frombits(binary.LittleEndian.Uint64(rest[:8])), 9, nil
	case TagString, TagBytes:
		raw, n, err := readBlob(rest)
		if err != nil {
			return nil, 0, err
		}
		if tag == TagString {
			return string(raw), 1 + n, nil
		}
		// Se copia para que el llamante no dependa del buffer del log.
		out := make([]byte, len(raw))
		copy(out, raw)
		return out, 1 + n, nil
	}
	return nil, 0, fmt.Errorf("%w: tag %d desconocido", ErrCodec, tag)
}

// AppendRow codifica una tupla (fila de valores) completa en dst.
func AppendRow(dst []byte, row Tuple) ([]byte, error) {
	var err error
	if dst, err = appendLen(dst, len(row)); err != nil {
		return nil, err
	}
	for _, v := range row {
		if dst, err = AppendValue(dst, v); err != nil {
			return nil, err
		}
	}
	return dst, nil
}

// DecodeRow lee una tupla codificada con AppendRow.
func DecodeRow(buf []byte) (Tuple, int, error) {
	n, used, err := readLen(buf)
	if err != nil {
		return nil, 0, err
	}
	rest := buf[used:]
	row := make(Tuple, 0, n)
	for i := 0; i < n; i++ {
		v, size, err := DecodeValue(rest)
		if err != nil {
			return nil, 0, err
		}
		row = append(row, v)
		rest = rest[size:]
	}
	// rest se ha ido avanzando valor a valor, así que lo que queda del buffer
	// es exactamente lo que se consumió de la tupla (incluida su longitud).
	return row, len(buf) - len(rest), nil
}

// EncodeRow devuelve la tupla codificada en un buffer nuevo.
func EncodeRow(row Tuple) ([]byte, error) { return AppendRow(nil, row) }

// DecodeRowBytes decodifica un buffer que contiene exactamente una tupla.
func DecodeRowBytes(buf []byte) (Tuple, error) {
	row, n, err := DecodeRow(buf)
	if err != nil {
		return nil, err
	}
	if n != len(buf) {
		return nil, fmt.Errorf("%w: %d bytes de sobra tras la tupla", ErrCodec, len(buf)-n)
	}
	return row, nil
}

func appendLen(dst []byte, n int) ([]byte, error) {
	if n < 0 || uint64(n) > math.MaxUint32 {
		return nil, fmt.Errorf("%w: longitud %d fuera de rango", ErrCodec, n)
	}
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], uint32(n))
	return append(dst, b[:]...), nil
}

func readLen(buf []byte) (int, int, error) {
	if len(buf) < 4 {
		return 0, 0, fmt.Errorf("%w: falta la longitud", ErrCodec)
	}
	return int(binary.LittleEndian.Uint32(buf[:4])), 4, nil
}

// appendBlob escribe un string o []byte precedido por su longitud. El bloque
// va en línea: los registros se escriben en archivos locales y una fila con
// muchos datos sigue siendo mucho más barata que un puntero que hay que
// resolver después.
func appendBlob(dst []byte, tag byte, data []byte) []byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], uint32(len(data)))
	dst = append(dst, tag)
	dst = append(dst, b[:]...)
	return append(dst, data...)
}

func readBlob(buf []byte) ([]byte, int, error) {
	if len(buf) < 4 {
		return nil, 0, fmt.Errorf("%w: falta la longitud del bloque", ErrCodec)
	}
	n := int(binary.LittleEndian.Uint32(buf[:4]))
	if n < 0 || len(buf[4:]) < n {
		return nil, 0, fmt.Errorf("%w: bloque de %d bytes truncado", ErrCodec, n)
	}
	return buf[4 : 4+n], 4 + n, nil
}

func truncated(tag ValueTag) error {
	return fmt.Errorf("%w: registro truncado en un valor de tipo %d", ErrCodec, tag)
}
