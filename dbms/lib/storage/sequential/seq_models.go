package sequential

import (
	"encoding/binary"
	"fmt"

	"github.com/dbms-go/v2/dbms/lib/storage"
)

const (
	SeqHeaderSize = 64

	SeqMagic    = uint32(0x53455130) // "SEQ0": archivo principal
	SeqAuxMagic = uint32(0x53455141) // "SEQA": archivo auxiliar
	SeqVersion  = uint32(1)

	// Cada slot: [flag uint8][len uint32][payload][relleno hasta SlotSize]
	SeqFlagLive = byte(0)
	SeqFlagDead = byte(1)
	SeqSlotHead = 1 + 4

	SUFFIX_SEQ = "_seq.dat"
	SUFFIX_AUX = "_aux.dat"
)

// SeqHeader es la cabecera de los archivos principal y auxiliar del sequential
// file. Epoch se incrementa en cada reconstrucción: un auxiliar con un epoch
// distinto al del principal ya fue absorbido y se descarta al abrir.
type SeqHeader struct {
	Magic      uint32
	Version    uint32
	SlotSize   int32
	SchemaHash uint32
	Slots      int64 // slots físicos (vivos + borrados) del principal
	Dead       int64 // slots borrados del principal
	Epoch      uint64
}

type SeqHeaderCodec struct{}

func (c SeqHeaderCodec) Sizeof() int { return SeqHeaderSize }

func (c SeqHeaderCodec) Marshal(h SeqHeader, buf []byte) error {
	if len(buf) < SeqHeaderSize {
		return fmt.Errorf("%w: SeqHeader", storage.ErrShortBuffer)
	}
	clear(buf[:SeqHeaderSize])
	binary.BigEndian.PutUint32(buf[0:], h.Magic)
	binary.BigEndian.PutUint32(buf[4:], h.Version)
	binary.BigEndian.PutUint32(buf[8:], uint32(h.SlotSize))
	binary.BigEndian.PutUint32(buf[12:], h.SchemaHash)
	binary.BigEndian.PutUint64(buf[16:], uint64(h.Slots))
	binary.BigEndian.PutUint64(buf[24:], uint64(h.Dead))
	binary.BigEndian.PutUint64(buf[32:], h.Epoch)
	return nil
}

// Unmarshal valida el magic esperado (SeqMagic o SeqAuxMagic) y la versión.
func (c SeqHeaderCodec) Unmarshal(h *SeqHeader, buf []byte, wantMagic uint32) error {
	if len(buf) < SeqHeaderSize {
		return fmt.Errorf("%w: SeqHeader", storage.ErrShortBuffer)
	}
	h.Magic = binary.BigEndian.Uint32(buf[0:])
	h.Version = binary.BigEndian.Uint32(buf[4:])
	h.SlotSize = int32(binary.BigEndian.Uint32(buf[8:]))
	h.SchemaHash = binary.BigEndian.Uint32(buf[12:])
	h.Slots = int64(binary.BigEndian.Uint64(buf[16:]))
	h.Dead = int64(binary.BigEndian.Uint64(buf[24:]))
	h.Epoch = binary.BigEndian.Uint64(buf[32:])
	if h.Magic != wantMagic {
		return fmt.Errorf("%w: magic number incorrecto", storage.ErrCorruptFile)
	}
	if h.Version != SeqVersion {
		return fmt.Errorf("%w: versión %d no soportada", storage.ErrCorruptFile, h.Version)
	}
	if h.SlotSize <= SeqSlotHead || h.Slots < 0 || h.Dead < 0 || h.Dead > h.Slots {
		return fmt.Errorf("%w: header inconsistente", storage.ErrCorruptFile)
	}
	return nil
}

var seqCodec = SeqHeaderCodec{}

func GetSeqHeaderCodec() SeqHeaderCodec { return seqCodec }
