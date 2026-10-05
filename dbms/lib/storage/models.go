package storage

import (
	"encoding/binary"
	"fmt"
	"reflect"
)

// ---------------------------------------------------------------------------
// Codec genérico
// ---------------------------------------------------------------------------

// Codec convierte un valor T en bytes y viceversa. Ningún método hace panic
// ante datos mal formados: siempre devuelven error.
type Codec[T any] interface {
	Sizeof(model T) (int, error)
	Marshal(model T, buf []byte) (int, error)
	Unmarshal(buf []byte) (T, error)
}

// FileStorage es el contrato de un archivo de registros direccionables por RID.
type FileStorage[T any] interface {
	Insert(model T) (RID, error)
	Get(rid RID) (T, error)
	Update(rid RID, model T) error
	Delete(rid RID) error
	Open() error
	Close() error
}

// reader lee de un buffer con comprobación de límites.
type reader struct {
	buf []byte
	off int
}

func (r *reader) take(n int) ([]byte, error) {
	if n < 0 || r.off+n > len(r.buf) {
		return nil, ErrShortBuffer
	}
	b := r.buf[r.off : r.off+n]
	r.off += n
	return b, nil
}

// ---------------------------------------------------------------------------
// RID
// ---------------------------------------------------------------------------

type RID struct{ File, Page, Slot int32 }

type RIDCodec struct{}

func (c RIDCodec) Sizeof() int { return RIDSize }

func (c RIDCodec) Marshal(rid RID, buf []byte) error {
	if len(buf) < RIDSize {
		return fmt.Errorf("%w: RID", ErrShortBuffer)
	}
	binary.BigEndian.PutUint32(buf[0:4], uint32(rid.File))
	binary.BigEndian.PutUint32(buf[4:8], uint32(rid.Page))
	binary.BigEndian.PutUint32(buf[8:12], uint32(rid.Slot))
	return nil
}

func (c RIDCodec) Unmarshal(rid *RID, buf []byte) error {
	if len(buf) < RIDSize {
		return fmt.Errorf("%w: RID", ErrShortBuffer)
	}
	rid.File = int32(binary.BigEndian.Uint32(buf[0:4]))
	rid.Page = int32(binary.BigEndian.Uint32(buf[4:8]))
	rid.Slot = int32(binary.BigEndian.Uint32(buf[8:12]))
	return nil
}

// ---------------------------------------------------------------------------
// FileHeader (página lógica 0, ocupa FileHeaderSize bytes)
// ---------------------------------------------------------------------------

type FileHeader struct {
	Magic     uint32
	Version   uint32
	PageSize  int32
	SlotSize  int32
	MaxRows   int32 // slots por página
	Pages     int32 // páginas de datos asignadas
	FreePage  int32 // pista: ninguna página anterior a esta tiene espacio
	Strategy  int32 // estrategia de borrado con la que se creó el archivo
	Rows      int64 // filas vivas
	TotalSize int64 // bytes de páginas de datos (sin el header)
}

type FileHeaderCodec struct{}

func (c FileHeaderCodec) Sizeof() int { return FileHeaderSize }

func (c FileHeaderCodec) Marshal(fh FileHeader, buf []byte) error {
	if len(buf) < FileHeaderSize {
		return fmt.Errorf("%w: FileHeader", ErrShortBuffer)
	}
	clear(buf[:FileHeaderSize])
	binary.BigEndian.PutUint32(buf[0:], fh.Magic)
	binary.BigEndian.PutUint32(buf[4:], fh.Version)
	binary.BigEndian.PutUint32(buf[8:], uint32(fh.PageSize))
	binary.BigEndian.PutUint32(buf[12:], uint32(fh.SlotSize))
	binary.BigEndian.PutUint32(buf[16:], uint32(fh.MaxRows))
	binary.BigEndian.PutUint32(buf[20:], uint32(fh.Pages))
	binary.BigEndian.PutUint32(buf[24:], uint32(fh.FreePage))
	binary.BigEndian.PutUint32(buf[28:], uint32(fh.Strategy))
	binary.BigEndian.PutUint64(buf[32:], uint64(fh.Rows))
	binary.BigEndian.PutUint64(buf[40:], uint64(fh.TotalSize))
	return nil
}

func (c FileHeaderCodec) Unmarshal(fh *FileHeader, buf []byte) error {
	if len(buf) < FileHeaderSize {
		return fmt.Errorf("%w: FileHeader", ErrShortBuffer)
	}
	fh.Magic = binary.BigEndian.Uint32(buf[0:])
	fh.Version = binary.BigEndian.Uint32(buf[4:])
	fh.PageSize = int32(binary.BigEndian.Uint32(buf[8:]))
	fh.SlotSize = int32(binary.BigEndian.Uint32(buf[12:]))
	fh.MaxRows = int32(binary.BigEndian.Uint32(buf[16:]))
	fh.Pages = int32(binary.BigEndian.Uint32(buf[20:]))
	fh.FreePage = int32(binary.BigEndian.Uint32(buf[24:]))
	fh.Strategy = int32(binary.BigEndian.Uint32(buf[28:]))
	fh.Rows = int64(binary.BigEndian.Uint64(buf[32:]))
	fh.TotalSize = int64(binary.BigEndian.Uint64(buf[40:]))
	if fh.Magic != FileMagic {
		return fmt.Errorf("%w: magic number incorrecto", ErrCorruptFile)
	}
	if fh.Version != FormatVersion {
		return fmt.Errorf("%w: versión %d no soportada", ErrCorruptFile, fh.Version)
	}
	if fh.PageSize <= 0 || fh.SlotSize < MinSlotSize || fh.MaxRows <= 0 || fh.Pages < 0 {
		return fmt.Errorf("%w: header inconsistente", ErrCorruptFile)
	}
	return nil
}

// ---------------------------------------------------------------------------
// PageHeader (tamaño fijo: los offsets de los slots no se mueven)
// ---------------------------------------------------------------------------

// PageHeader:
//   - Rows:      filas vivas en la página.
//   - HighWater: slots usados alguna vez (los slots >= HighWater nunca se escribieron).
//   - FreeHead:  primer slot libre de la free list intrusiva (NoSlot si no hay).
//     Con la estrategia MoveTheLast no se usa y HighWater == Rows.
type PageHeader struct {
	Rows      int32
	HighWater int32
	FreeHead  int32
}

type PageHeaderCodec struct{}

func (c PageHeaderCodec) Sizeof() int { return PageHeaderSize }

func (c PageHeaderCodec) Marshal(ph PageHeader, buf []byte) error {
	if len(buf) < PageHeaderSize {
		return fmt.Errorf("%w: PageHeader", ErrShortBuffer)
	}
	clear(buf[:PageHeaderSize])
	binary.BigEndian.PutUint32(buf[0:], uint32(ph.Rows))
	binary.BigEndian.PutUint32(buf[4:], uint32(ph.HighWater))
	binary.BigEndian.PutUint32(buf[8:], uint32(ph.FreeHead))
	return nil
}

func (c PageHeaderCodec) Unmarshal(ph *PageHeader, buf []byte) error {
	if len(buf) < PageHeaderSize {
		return fmt.Errorf("%w: PageHeader", ErrShortBuffer)
	}
	ph.Rows = int32(binary.BigEndian.Uint32(buf[0:]))
	ph.HighWater = int32(binary.BigEndian.Uint32(buf[4:]))
	ph.FreeHead = int32(binary.BigEndian.Uint32(buf[8:]))
	return nil
}

var (
	fhCodec = FileHeaderCodec{}
	phCodec = PageHeaderCodec{}
)

func GetFileHeaderCodec() FileHeaderCodec { return fhCodec }
func GetPageHeaderCodec() PageHeaderCodec { return phCodec }

// ---------------------------------------------------------------------------
// ReflectCodec: serializa structs con campos exportados
// ---------------------------------------------------------------------------

// ReflectCodec[T] implementa Codec[T] para structs cuyos campos exportados son
// int, int32, int64, uint8, bool, string o []byte. Los campos no exportados se
// ignoran de forma consistente en Sizeof, Marshal y Unmarshal.
type ReflectCodec[T any] struct{}

func (c ReflectCodec[T]) Sizeof(model T) (int, error) {
	v := reflect.ValueOf(&model).Elem()
	if v.Kind() != reflect.Struct {
		return 0, fmt.Errorf("%w: el modelo debe ser un struct", ErrUnsupportedType)
	}
	size := 0
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		if !t.Field(i).IsExported() {
			continue
		}
		f := v.Field(i)
		switch f.Kind() {
		case reflect.Int, reflect.Int64:
			size += 8
		case reflect.Int32:
			size += 4
		case reflect.Uint8, reflect.Bool:
			size++
		case reflect.String:
			size += 4 + f.Len()
		case reflect.Slice:
			if f.Type().Elem().Kind() != reflect.Uint8 {
				return 0, fmt.Errorf("%w: campo %s (%s)", ErrUnsupportedType, t.Field(i).Name, f.Type())
			}
			size += 4 + f.Len()
		default:
			return 0, fmt.Errorf("%w: campo %s (%s)", ErrUnsupportedType, t.Field(i).Name, f.Kind())
		}
	}
	return size, nil
}

func (c ReflectCodec[T]) Marshal(model T, buf []byte) (int, error) {
	need, err := c.Sizeof(model)
	if err != nil {
		return 0, err
	}
	if len(buf) < need {
		return 0, fmt.Errorf("%w: requiere %d bytes, tiene %d", ErrShortBuffer, need, len(buf))
	}
	v := reflect.ValueOf(&model).Elem()
	t := v.Type()
	off := 0
	for i := 0; i < v.NumField(); i++ {
		if !t.Field(i).IsExported() {
			continue
		}
		f := v.Field(i)
		switch f.Kind() {
		case reflect.Int, reflect.Int64:
			binary.BigEndian.PutUint64(buf[off:], uint64(f.Int()))
			off += 8
		case reflect.Int32:
			binary.BigEndian.PutUint32(buf[off:], uint32(f.Int()))
			off += 4
		case reflect.Uint8:
			buf[off] = byte(f.Uint())
			off++
		case reflect.Bool:
			if f.Bool() {
				buf[off] = 1
			} else {
				buf[off] = 0
			}
			off++
		case reflect.String:
			s := f.String()
			binary.BigEndian.PutUint32(buf[off:], uint32(len(s)))
			off += 4
			off += copy(buf[off:], s)
		case reflect.Slice:
			b := f.Bytes()
			binary.BigEndian.PutUint32(buf[off:], uint32(len(b)))
			off += 4
			off += copy(buf[off:], b)
		}
	}
	return off, nil
}

func (c ReflectCodec[T]) Unmarshal(buf []byte) (T, error) {
	var model T
	v := reflect.ValueOf(&model).Elem()
	if v.Kind() != reflect.Struct {
		return model, fmt.Errorf("%w: el modelo debe ser un struct", ErrUnsupportedType)
	}
	t := v.Type()
	r := reader{buf: buf}
	for i := 0; i < v.NumField(); i++ {
		if !t.Field(i).IsExported() {
			continue
		}
		f := v.Field(i)
		switch f.Kind() {
		case reflect.Int, reflect.Int64:
			b, err := r.take(8)
			if err != nil {
				return model, err
			}
			f.SetInt(int64(binary.BigEndian.Uint64(b)))
		case reflect.Int32:
			b, err := r.take(4)
			if err != nil {
				return model, err
			}
			f.SetInt(int64(int32(binary.BigEndian.Uint32(b))))
		case reflect.Uint8:
			b, err := r.take(1)
			if err != nil {
				return model, err
			}
			f.SetUint(uint64(b[0]))
		case reflect.Bool:
			b, err := r.take(1)
			if err != nil {
				return model, err
			}
			f.SetBool(b[0] == 1)
		case reflect.String:
			lb, err := r.take(4)
			if err != nil {
				return model, err
			}
			b, err := r.take(int(binary.BigEndian.Uint32(lb)))
			if err != nil {
				return model, err
			}
			f.SetString(string(b))
		case reflect.Slice:
			if f.Type().Elem().Kind() != reflect.Uint8 {
				return model, fmt.Errorf("%w: campo %s (%s)", ErrUnsupportedType, t.Field(i).Name, f.Type())
			}
			lb, err := r.take(4)
			if err != nil {
				return model, err
			}
			b, err := r.take(int(binary.BigEndian.Uint32(lb)))
			if err != nil {
				return model, err
			}
			f.SetBytes(append([]byte{}, b...))
		default:
			return model, fmt.Errorf("%w: campo %s (%s)", ErrUnsupportedType, t.Field(i).Name, f.Kind())
		}
	}
	return model, nil
}
