package storage

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"strings"
)

type Table struct {
	Name    string
	Columns []string
	Types   []Type
	MaxLen  []int // string/bytes: máximo declarado (VARCHAR(n)); ignorado en tipos fijos
	KeyCols []int // columnas de la PK, en orden
}

const (
	keyTagInt32  byte = 0x01
	keyTagInt64  byte = 0x02
	keyTagBool   byte = 0x03
	keyTagString byte = 0x04
	keyTagBytes  byte = 0x05
	keyTagTuple  byte = 0x06
)

// EncodeKey valida la clave contra la PK de la tabla y la codifica.
func (t Table) EncodeKey(key Tuple) ([]byte, error) {
	if err := t.ValidateKey(key); err != nil {
		return nil, err
	}
	return EncodeKey(key)
}

// maxColSize devuelve el tamaño serializado máximo de la columna i.
func (t Table) maxColSize(i int) int {
	switch t.Types[i] {
	case TypeInt32:
		return 4
	case TypeInt64:
		return 8
	case TypeBool:
		return 1
	default: // string, bytes: 4 bytes de longitud + MaxLen
		return 4 + t.MaxLen[i]
	}
}

// Validate comprueba que el esquema sirve para un archivo ordenado por PK.
func (t Table) Validate() error {
	if len(t.Types) == 0 {
		return fmt.Errorf("%w: el esquema no tiene columnas", ErrBadOptions)
	}
	if len(t.Columns) != 0 && len(t.Columns) != len(t.Types) {
		return fmt.Errorf("%w: Columns y Types tienen distinta longitud", ErrBadOptions)
	}
	for i, ty := range t.Types {
		switch ty {
		case TypeInt32, TypeInt64, TypeBool:
		case TypeString, TypeBytes:
			if len(t.MaxLen) != len(t.Types) || t.MaxLen[i] <= 0 {
				return fmt.Errorf("%w: la columna %d (%s) necesita MaxLen > 0", ErrBadOptions, i, ty)
			}
		default:
			return fmt.Errorf("%w: columna %d (%s)", ErrUnsupportedType, i, ty)
		}
	}
	if len(t.KeyCols) == 0 {
		return fmt.Errorf("%w: falta la clave primaria (KeyCols)", ErrBadOptions)
	}
	seen := map[int]bool{}
	for _, c := range t.KeyCols {
		if c < 0 || c >= len(t.Types) || seen[c] {
			return fmt.Errorf("%w: KeyCols inválido (%d)", ErrBadOptions, c)
		}
		seen[c] = true
	}
	return nil
}

// MaxPayload es el tamaño máximo de una tupla serializada con TupleCodec.
func (t Table) MaxPayload() int {
	n := 0
	for i := range t.Types {
		n += t.maxColSize(i)
	}
	return n
}

// CheckTuple valida tipos, aridad y que ninguna columna exceda su MaxLen.
func (t Table) CheckTuple(tp Tuple) error {
	if _, err := NewTupleCodec(t).Sizeof(tp); err != nil {
		return err
	}
	for i, ty := range t.Types {
		var n int
		switch ty {
		case TypeString:
			n = len(tp[i].(string))
		case TypeBytes:
			n = len(tp[i].([]byte))
		default:
			continue
		}
		if n > t.MaxLen[i] {
			return fmt.Errorf("%w: la columna %d mide %d y su MaxLen es %d", ErrTupleTooLarge, i, n, t.MaxLen[i])
		}
	}
	return nil
}

// KeyOf extrae la clave primaria de una tupla ya validada.
func (t Table) KeyOf(tp Tuple) Tuple {
	k := make(Tuple, len(t.KeyCols))
	for i, c := range t.KeyCols {
		k[i] = tp[c]
	}
	return k
}

// ValidateKey comprueba que k tenga un valor del tipo correcto por columna de la PK.
func (t Table) ValidateKey(k Tuple) error {
	if len(k) != len(t.KeyCols) {
		return fmt.Errorf("%w: la clave tiene %d valores y la PK %d columnas", ErrSchemaMismatch, len(k), len(t.KeyCols))
	}
	for i, c := range t.KeyCols {
		ok := false
		switch t.Types[c] {
		case TypeInt32:
			_, ok = k[i].(int32)
		case TypeInt64:
			_, ok = k[i].(int64)
		case TypeBool:
			_, ok = k[i].(bool)
		case TypeString:
			_, ok = k[i].(string)
		case TypeBytes:
			_, ok = k[i].([]byte)
		}
		if !ok {
			return fmt.Errorf("%w: la clave %d espera %s y recibió %T", ErrSchemaMismatch, i, t.Types[c], k[i])
		}
	}
	return nil
}

// CompareKeys compara dos claves ya validadas, columna a columna.
// Devuelve <0, 0 o >0.
func (t Table) CompareKeys(a, b Tuple) int {
	for i, c := range t.KeyCols {
		if r := compareValues(t.Types[c], a[i], b[i]); r != 0 {
			return r
		}
	}
	return 0
}

func compareValues(ty Type, a, b any) int {
	switch ty {
	case TypeInt32:
		return cmpOrdered(a.(int32), b.(int32))
	case TypeInt64:
		return cmpOrdered(a.(int64), b.(int64))
	case TypeBool:
		x, y := a.(bool), b.(bool)
		switch {
		case x == y:
			return 0
		case !x:
			return -1
		}
		return 1
	case TypeString:
		return strings.Compare(a.(string), b.(string))
	case TypeBytes:
		return bytes.Compare(a.([]byte), b.([]byte))
	}
	return 0
}

func cmpOrdered[T int32 | int64](a, b T) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// SchemaHash identifica el esquema físico (tipos, máximos y PK) para detectar
// que un archivo se abre con una tabla distinta a la que lo creó.
func (t Table) SchemaHash() uint32 {
	h := fnv.New32a()
	w := func(v int) { fmt.Fprintf(h, "%d,", v) }
	for i, ty := range t.Types {
		w(int(ty))
		if ty == TypeString || ty == TypeBytes {
			w(t.MaxLen[i])
		}
	}
	h.Write([]byte("|"))
	for _, c := range t.KeyCols {
		w(c)
	}
	return h.Sum32()
}

// KeyedStorage es el contrato de un archivo direccionado por clave primaria
// (sin RIDs): la clave identifica la fila.
type KeyedStorage interface {
	Insert(t Tuple) error
	Search(key Tuple) (Tuple, error)
	Update(key Tuple, t Tuple) error
	Delete(key Tuple) error
	Open() error
	Close() error
}

func EncodeKey(key any) ([]byte, error) { return AppendKey(nil, key) }

// AppendKey es EncodeKey sobre un buffer existente.
func AppendKey(dst []byte, key any) ([]byte, error) {
	switch v := key.(type) {
	case int32:
		dst = append(dst, keyTagInt32)
		return binary.BigEndian.AppendUint32(dst, uint32(v)^0x80000000), nil
	case int64:
		return appendInt64(dst, v), nil
	case int:
		return appendInt64(dst, int64(v)), nil
	case bool:
		b := byte(0)
		if v {
			b = 1
		}
		return append(dst, keyTagBool, b), nil
	case string:
		return appendEscaped(append(dst, keyTagString), v), nil
	case []byte:
		return appendEscaped(append(dst, keyTagBytes), v), nil
	case Tuple:
		return appendTuple(dst, v)
	case []any:
		return appendTuple(dst, Tuple(v))
	}
	return nil, fmt.Errorf("%w: clave de tipo %T", ErrUnsupportedType, key)
}

func appendInt64(dst []byte, v int64) []byte {
	dst = append(dst, keyTagInt64)
	return binary.BigEndian.AppendUint64(dst, uint64(v)^0x8000000000000000)
}

// appendEscaped escribe los bytes escapando 0x00 como 0x00 0xFF y cerrando con
// 0x00 0x01, de modo que el orden lexicográfico se conserva aun con claves que
// son prefijo de otras o que contienen ceros.
func appendEscaped[S ~string | ~[]byte](dst []byte, s S) []byte {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == 0 {
			dst = append(dst, 0x00, 0xFF)
		} else {
			dst = append(dst, c)
		}
	}
	return append(dst, 0x00, 0x01)
}

func appendTuple(dst []byte, t Tuple) ([]byte, error) {
	switch len(t) {
	case 0:
		return nil, fmt.Errorf("%w: clave vacía", ErrSchemaMismatch)
	case 1:
		return AppendKey(dst, t[0])
	}
	dst = append(dst, keyTagTuple)
	var err error
	for _, v := range t {
		if dst, err = AppendKey(dst, v); err != nil {
			return nil, err
		}
	}
	return append(dst, 0x00), nil // ninguna etiqueta vale 0x00: fin inequívoco
}

type KeyExtractor func(t Tuple) (any, error)

// TableKey extrae la clave primaria de table (KeyCols) de cada fila.
func TableKey(table Table) KeyExtractor {
	return func(t Tuple) (any, error) {
		if len(table.KeyCols) == 0 {
			return nil, fmt.Errorf("%w: la tabla no define KeyCols", ErrBadOptions)
		}
		for _, c := range table.KeyCols {
			if c < 0 || c >= len(t) {
				return nil, fmt.Errorf("%w: la fila tiene %d columnas y la PK usa la %d", ErrSchemaMismatch, len(t), c)
			}
		}
		return table.KeyOf(t), nil
	}
}
