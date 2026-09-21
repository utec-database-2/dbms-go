// Package common contiene el contrato Index compartido por todos los
// índices (B+ Tree agrupado/no agrupado y Extendible Hashing), para que
// el motor de consultas no necesite saber cuál implementación está usando.
//
// NO cambiar estas firmas sin avisar al resto del equipo (afecta a
// quien implemente B+ Tree también).
package common

import (
	"errors"

	"github.com/dbms-go/v2/dbms/lib/storage"
)

// ErrRangeNotSupported se devuelve en RangeSearch cuando el índice no
// soporta búsquedas por rango (es el caso de Extendible Hashing).
var ErrRangeNotSupported = errors.New("index: range search not supported")

// Index es el contrato común para todos los índices.
type Index interface {
	Insert(key any, rid storage.RID) error

	// Search devuelve todos los RID que hacen match exacto con key.
	// Slice vacío (no error) si no hay coincidencias.
	Search(key any) ([]storage.RID, error)

	// RangeSearch devuelve los RID en [keyMin, keyMax]. Debe devolver
	// ErrRangeNotSupported si el índice no soporta rangos.
	RangeSearch(keyMin, keyMax any) ([]storage.RID, error)

	Delete(key any, rid storage.RID) (bool, error)

	// SupportsRange: true para B+ Tree, false para Extendible Hashing.
	SupportsRange() bool
}
