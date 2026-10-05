// Package transaction implementa el control de concurrencia y de fallos del
// motor: un write-ahead log (WAL) que registra cada modificación antes de que
// se aplique a los archivos de datos, y un gestor de transacciones que decide
// qué hacer con esas modificaciones cuando la transacción confirma, se
// deshace o el proceso se corta.
//
// El protocolo es steal / no-force con deshacer (undo):
//
//   - steal: la modificación se aplica al archivo de datos en cuanto se
//     ejecuta, así que una transacción abierta puede haber dejado cambios
//     visibles en disco.
//   - no-force: COMMIT no obliga a volcar las páginas, solo a forzar (fsync)
//     el registro de commit del WAL.
//   - undo: al arrancar, Recover deshace en orden inverso los registros de las
//     transacciones que no dejaron un commit, dejándolas como si nunca
//     hubieranexistido.
//
// Las transacciones confirmadas no necesitan redo porque sus cambios ya están
// en los archivos de datos, que se abren y leen al montar el motor.
package transaction

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"

	"github.com/dbms-go/v2/dbms/lib/storage"
)

// Op es el tipo de operación que describe un registro del WAL.
type Op uint8

const (
	// OpBegin abre una transacción.
	OpBegin Op = iota + 1
	// OpInsert registra una fila insertada.
	OpInsert
	// OpDelete registra una fila eliminada.
	OpDelete
	// OpUpdate registra una fila modificada (Prev es el valor anterior).
	OpUpdate
	// OpCommit confirma la transacción: sus cambios son definitivos.
	OpCommit
	// OpRollback deja constancia de que la transacción se deshizo.
	OpRollback
)

func (o Op) String() string {
	switch o {
	case OpBegin:
		return "BEGIN"
	case OpInsert:
		return "INSERT"
	case OpDelete:
		return "DELETE"
	case OpUpdate:
		return "UPDATE"
	case OpCommit:
		return "COMMIT"
	case OpRollback:
		return "ROLLBACK"
	}
	return fmt.Sprintf("OP(%d)", uint8(o))
}

// Record es una entrada del write-ahead log.
//
// La fila se guarda como valores sin esquema (storage.AppendRow) porque el log
// se escribe antes de que la transacción tenga una tabla asociada, y el RID se
// guarda tal cual: en tablas no agrupadas es la dirección física de la fila y en
// tablas agrupadas es el RID sintético que codifica la clave primaria.
type Record struct {
	// LSN es el número de secuencia del registro dentro del log.
	LSN uint64
	// TxID identifica la transacción autora.
	TxID uint64
	// Op es la operación registrada.
	Op Op
	// Table es la tabla afectada (vacío en BEGIN/COMMIT/ROLLBACK).
	Table string
	// RID localiza la fila en el almacenamiento.
	RID storage.RID
	// Row es la fila insertada, eliminada o el valor nuevo de un UPDATE.
	Row storage.Tuple
	// Prev es el valor anterior de la fila en un UPDATE.
	Prev storage.Tuple
	// HasPrev distingue un UPDATE con valor anterior vacío de un DELETE.
	HasPrev bool
}

// walMagic identifies el log y evita que se lea un archivo ajeno.
const walMagic = uint32(0x57414C31) // "WAL1"

// walRecordOverhead es el tamaño fijo de la cabecera de cada registro:
// [len uint32][crc uint32][lsn uint64][txid uint64][op uint8].
const walRecordOverhead = 4 + 4 + 8 + 8 + 1

// logFileHeaderSize es la cabecera del archivo: [magic uint32][version uint32].
const logFileHeaderSize = 8

// FormatVersion es la versión del formato del log.
const FormatVersion = uint32(1)

// Encode serializa el registro con su longitud y su CRC, de forma que una
// escritura a medias se detecte al releer el log.
func (r Record) Encode() ([]byte, error) {
	body, err := r.body()
	if err != nil {
		return nil, err
	}
	var b [walRecordOverhead]byte
	binary.LittleEndian.PutUint32(b[0:4], uint32(len(body)))
	binary.LittleEndian.PutUint32(b[4:8], crc32.ChecksumIEEE(body))
	binary.LittleEndian.PutUint64(b[8:16], r.LSN)
	binary.LittleEndian.PutUint64(b[16:24], r.TxID)
	b[24] = byte(r.Op)
	return append(b[:], body...), nil
}

// body serializa la parte variable del registro: tabla, RID y filas.
func (r Record) body() ([]byte, error) {
	buf := make([]byte, 0, 64)
	if len(r.Table) > 0xFFFF {
		return nil, fmt.Errorf("transaction: nombre de tabla demasiado largo: %d bytes", len(r.Table))
	}
	var n [2]byte
	binary.LittleEndian.PutUint16(n[:], uint16(len(r.Table)))
	buf = append(buf, n[:]...)
	buf = append(buf, r.Table...)

	var rid [12]byte
	binary.LittleEndian.PutUint32(rid[0:4], uint32(r.RID.File))
	binary.LittleEndian.PutUint32(rid[4:8], uint32(r.RID.Page))
	binary.LittleEndian.PutUint32(rid[8:12], uint32(r.RID.Slot))
	buf = append(buf, rid[:]...)

	var flag byte
	if r.HasPrev {
		flag = 1
	}
	buf = append(buf, flag)

	var err error
	if buf, err = storage.AppendRow(buf, r.Row); err != nil {
		return nil, err
	}
	if r.HasPrev {
		if buf, err = storage.AppendRow(buf, r.Prev); err != nil {
			return nil, err
		}
	}
	return buf, nil
}

// DecodeRecord lee un registro (con longitud y CRC) del buffer.
func DecodeRecord(buf []byte) (Record, int, error) {
	var r Record
	if len(buf) < walRecordOverhead {
		return r, 0, fmt.Errorf("%w: registro incompleto (%d bytes)", ErrWALCorrupt, len(buf))
	}
	size := int(binary.LittleEndian.Uint32(buf[0:4]))
	sum := binary.LittleEndian.Uint32(buf[4:8])
	if size < 0 || walRecordOverhead+size > len(buf) {
		return r, 0, fmt.Errorf("%w: longitud %d no cabe en %d bytes", ErrWALCorrupt, size, len(buf))
	}
	body := buf[walRecordOverhead : walRecordOverhead+size]
	if crc32.ChecksumIEEE(body) != sum {
		return r, 0, fmt.Errorf("%w: CRC no coincide", ErrWALCorrupt)
	}
	r.LSN = binary.LittleEndian.Uint64(buf[8:16])
	r.TxID = binary.LittleEndian.Uint64(buf[16:24])
	r.Op = Op(buf[24])
	if err := r.decodeBody(body); err != nil {
		return Record{}, 0, err
	}
	return r, walRecordOverhead + size, nil
}

func (r *Record) decodeBody(body []byte) error {
	rest := body
	if len(rest) < 2 {
		return fmt.Errorf("%w: falta el nombre de la tabla", ErrWALCorrupt)
	}
	nameLen := int(binary.LittleEndian.Uint16(rest[:2]))
	rest = rest[2:]
	if len(rest) < nameLen {
		return fmt.Errorf("%w: nombre de tabla truncado", ErrWALCorrupt)
	}
	r.Table = string(rest[:nameLen])
	rest = rest[nameLen:]

	if len(rest) < 12+1 {
		return fmt.Errorf("%w: falta el RID", ErrWALCorrupt)
	}
	r.RID = storage.RID{
		File: int32(binary.LittleEndian.Uint32(rest[0:4])),
		Page: int32(binary.LittleEndian.Uint32(rest[4:8])),
		Slot: int32(binary.LittleEndian.Uint32(rest[8:12])),
	}
	rest = rest[12:]
	r.HasPrev = rest[0] == 1
	rest = rest[1:]

	row, used, err := storage.DecodeRow(rest)
	if err != nil {
		return err
	}
	r.Row = row
	rest = rest[used:]
	if r.HasPrev {
		prev, _, err := storage.DecodeRow(rest)
		if err != nil {
			return err
		}
		r.Prev = prev
	}
	return nil
}

// ReadLog devuelve los registros válidos del archivo path.
//
// Un registro a medias (el proceso se cortó escribiendo) simplemente termina la
// lectura: es el comportamiento habitual de un WAL, porque lo que no llegó a
// escribirse es una operación que tampoco se aplicó a los datos.
func ReadLog(path string) ([]Record, error) {
	buf, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(buf) < logFileHeaderSize {
		return nil, nil
	}
	if magic := binary.LittleEndian.Uint32(buf[0:4]); magic != walMagic {
		return nil, fmt.Errorf("%w: el archivo no es un WAL (magic %x)", ErrWALCorrupt, magic)
	}
	if v := binary.LittleEndian.Uint32(buf[4:8]); v != FormatVersion {
		return nil, fmt.Errorf("%w: versión de log %d, se esperaba %d", ErrWALCorrupt, v, FormatVersion)
	}

	var out []Record
	rest := buf[logFileHeaderSize:]
	for len(rest) > 0 {
		rec, used, err := DecodeRecord(rest)
		if err != nil {
			break // cola truncada: el final real del log
		}
		out = append(out, rec)
		rest = rest[used:]
	}
	return out, nil
}
