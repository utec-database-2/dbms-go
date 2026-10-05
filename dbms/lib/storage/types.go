package storage

import "fmt"

type Type int

const (
	TypeInt32 Type = iota + 1
	TypeInt64
	TypeBool
	TypeString
	TypeBytes
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
	}
	return fmt.Sprintf("type(%d)", int(t))
}
