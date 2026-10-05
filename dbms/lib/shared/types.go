// Package shared contiene los tipos que cruzan varias capas del motor.
//
// Record es una fila en memoria. RID vive en dbms/lib/storage; se expone
// aquí solo para no forzar a cada paquete a importar el storage.
package shared

import "github.com/dbms-go/v2/dbms/lib/storage"

// Record es una fila en memoria. Values respeta el orden de columnas
// del esquema de la tabla.
type Record struct {
	Values []any
	RID    storage.RID
}
