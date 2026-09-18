// Package storage contiene los tipos compartidos por los motores de
// almacenamiento (Heap File y Archivo Secuencial) y por los índices.
package storage

import "fmt"

// RID identifica físicamente un registro: página + slot dentro de la página.
type RID struct {
	PageID uint32
	SlotID uint16
}

func (r RID) String() string {
	return fmt.Sprintf("(%d,%d)", r.PageID, r.SlotID)
}