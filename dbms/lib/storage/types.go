package storage

import "fmt"

type Type int

const (
	TypeInt32 Type = iota + 1
	TypeInt64
	TypeBool
	TypeString
	TypeBytes
	// TypeVector es un vector de dimensión fija. MaxLen guarda la cantidad de
	// componentes y el valor de la tupla es un []float32.
	TypeVector
)

func (t Type) String() string {
	switch t {
	case TypeInt32:
		return "int32"
	case TypeInt64:
		return "int64"
	case TypeBool:
		return "bool"
	case TypeString:
		return "string"
	case TypeBytes:
		return "bytes"
	case TypeVector:
		return "vector"
	}
	return fmt.Sprintf("type(%d)", int(t))
}
