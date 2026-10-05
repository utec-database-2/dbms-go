package storage

import "errors"

var (
	ErrClosed          = errors.New("storage: archivo cerrado")
	ErrAlreadyOpen     = errors.New("storage: archivo ya abierto")
	ErrInvalidRID      = errors.New("storage: RID inválido")
	ErrRecordNotFound  = errors.New("storage: registro no encontrado")
	ErrTupleTooLarge   = errors.New("storage: la tupla no cabe en un slot")
	ErrCorruptFile     = errors.New("storage: archivo corrupto")
	ErrShortBuffer     = errors.New("storage: buffer insuficiente")
	ErrSchemaMismatch  = errors.New("storage: la tupla no coincide con el esquema")
	ErrUnsupportedType = errors.New("storage: tipo no soportado")
	ErrBadOptions      = errors.New("storage: opciones inválidas")

	ErrDuplicateKey        = errors.New("storage: clave primaria duplicada")
	ErrIteratorInvalidated = errors.New("storage: el iterador quedó invalidado por una reconstrucción")

	ErrUnstableRIDs = errors.New("extendible: el heap no mantiene RIDs estables (MoveTheLast)")
)
