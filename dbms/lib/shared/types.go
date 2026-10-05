// Package shared contiene los tipos que cruzan varias capas del motor.
//
// Record es una fila en memoria. RID vive en dbms/lib/storage; se expone
// aquí solo para no forzar a cada paquete a importar el storage.
package shared

import (
	"encoding/gob"
	"encoding/json"
	"strconv"

	"github.com/dbms-go/v2/dbms/lib/storage"
)

// Record es una fila en memoria. Values respeta el orden de columnas
// del esquema de la tabla.
type Record struct {
	Values []any
	RID    storage.RID
}

// Point es el valor de una columna POINT: latitud y longitud en grados.
type Point struct {
	Lat float64
	Lon float64
}

func (p Point) String() string {
	return "POINT(" +
		strconv.FormatFloat(p.Lat, 'f', -1, 64) + ", " +
		strconv.FormatFloat(p.Lon, 'f', -1, 64) + ")"
}

// MarshalJSON expone el punto como texto, que es como lo escribe el SQL.
func (p Point) MarshalJSON() ([]byte, error) {
	return json.Marshal(p.String())
}

func init() {
	gob.Register(Point{})
}
