package storage

import (
	"encoding/binary"
	"fmt"
)

// TupleCodec serializa Tuple según el esquema de Table. Implementa Codec[Tuple].
//
// Formato por columna (big endian):
//
//	int32 -> 4 bytes | int64 -> 8 | bool -> 1
//	string, bytes -> uint32 longitud + datos
type TupleCodec struct {
	Table Table
}

type Tuple []any

var _ Codec[Tuple] = TupleCodec{}

func NewTupleCodec(table Table) TupleCodec { return TupleCodec{Table: table} }

// Sizeof valida la tupla contra el esquema y devuelve su tamaño en bytes.
func (c TupleCodec) Sizeof(t Tuple) (int, error) {
	if len(t) != len(c.Table.Types) {
		return 0, fmt.Errorf("%w: %d valores para %d columnas", ErrSchemaMismatch, len(t), len(c.Table.Types))
	}
	size := 0
	for i, ty := range c.Table.Types {
		switch ty {
		case TypeInt32:
			if _, ok := t[i].(int32); !ok {
				return 0, mismatch(i, ty, t[i])
			}
			size += 4
		case TypeInt64:
			if _, ok := t[i].(int64); !ok {
				return 0, mismatch(i, ty, t[i])
			}
			size += 8
		case TypeBool:
			if _, ok := t[i].(bool); !ok {
				return 0, mismatch(i, ty, t[i])
			}
			size++
		case TypeString:
			s, ok := t[i].(string)
			if !ok {
				return 0, mismatch(i, ty, t[i])
			}
			size += 4 + len(s)
		case TypeBytes:
			b, ok := t[i].([]byte)
			if !ok {
				return 0, mismatch(i, ty, t[i])
			}
			size += 4 + len(b)
		default:
			return 0, fmt.Errorf("%w: columna %d (%s)", ErrUnsupportedType, i, ty)
		}
	}
	return size, nil
}

func mismatch(col int, want Type, got any) error {
	return fmt.Errorf("%w: columna %d espera %s y recibió %T", ErrSchemaMismatch, col, want, got)
}

// Marshal escribe la tupla en buf y devuelve los bytes escritos.
func (c TupleCodec) Marshal(t Tuple, buf []byte) (int, error) {
	need, err := c.Sizeof(t) // tras esto las aserciones de tipo son seguras
	if err != nil {
		return 0, err
	}
	if len(buf) < need {
		return 0, fmt.Errorf("%w: requiere %d bytes, tiene %d", ErrShortBuffer, need, len(buf))
	}
	off := 0
	for i, ty := range c.Table.Types {
		switch ty {
		case TypeInt32:
			binary.BigEndian.PutUint32(buf[off:], uint32(t[i].(int32)))
			off += 4
		case TypeInt64:
			binary.BigEndian.PutUint64(buf[off:], uint64(t[i].(int64)))
			off += 8
		case TypeBool:
			if t[i].(bool) {
				buf[off] = 1
			} else {
				buf[off] = 0
			}
			off++
		case TypeString:
			s := t[i].(string)
			binary.BigEndian.PutUint32(buf[off:], uint32(len(s)))
			off += 4
			off += copy(buf[off:], s)
		case TypeBytes:
			b := t[i].([]byte)
			binary.BigEndian.PutUint32(buf[off:], uint32(len(b)))
			off += 4
			off += copy(buf[off:], b)
		}
	}
	return off, nil
}

// Unmarshal reconstruye una tupla. Con bytes truncados devuelve ErrShortBuffer.
func (c TupleCodec) Unmarshal(buf []byte) (Tuple, error) {
	t := make(Tuple, len(c.Table.Types))
	r := reader{buf: buf}
	for i, ty := range c.Table.Types {
		switch ty {
		case TypeInt32:
			b, err := r.take(4)
			if err != nil {
				return nil, err
			}
			t[i] = int32(binary.BigEndian.Uint32(b))
		case TypeInt64:
			b, err := r.take(8)
			if err != nil {
				return nil, err
			}
			t[i] = int64(binary.BigEndian.Uint64(b))
		case TypeBool:
			b, err := r.take(1)
			if err != nil {
				return nil, err
			}
			t[i] = b[0] == 1
		case TypeString:
			lb, err := r.take(4)
			if err != nil {
				return nil, err
			}
			b, err := r.take(int(binary.BigEndian.Uint32(lb)))
			if err != nil {
				return nil, err
			}
			t[i] = string(b)
		case TypeBytes:
			lb, err := r.take(4)
			if err != nil {
				return nil, err
			}
			b, err := r.take(int(binary.BigEndian.Uint32(lb)))
			if err != nil {
				return nil, err
			}
			t[i] = append([]byte{}, b...)
		default:
			return nil, fmt.Errorf("%w: columna %d (%s)", ErrUnsupportedType, i, ty)
		}
	}
	return t, nil
}
