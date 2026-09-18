// Package shared contiene los tipos que cruzan varias capas del motor.
//
// IMPORTANTE: RID y Record son un placeholder de trabajo hasta que el
// dueño del storage (Heap File / Archivo Secuencial) defina la versión
// real — probablemente algo en dbms/lib/storage. Cuando eso exista,
// avisar al equipo para migrar todos los imports de dbms/lib/shared a
// ese paquete; la lógica de los algoritmos no debería cambiar.
package shared

// RID identifica físicamente un registro: página + slot dentro de la página.
type RID struct {
	PageID int
	SlotID int
}

// Record es una fila en memoria. Values respeta el orden de columnas
// del esquema de la tabla.
type Record struct {
	Values []any
	RID    RID
}
