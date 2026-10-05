package storage

const (
	SUFFIX_DATA  = "_data.dat"
	SUFFIX_INDEX = "_index.dat"

	RIDSize = 12 // 3 * int32

	// Región reservada al inicio del archivo para el FileHeader. Es fija para
	// que el offset de las páginas nunca dependa del contenido del header.
	FileHeaderSize = 64
	// Cabecera fija de cada página (con espacio reservado para LSN/checksum).
	PageHeaderSize = 16

	// Cada slot guarda [len uint32][payload]. Un slot libre guarda
	// [FreeMarker uint32][siguiente slot libre int32].
	SlotLenSize = 4
	MinSlotSize = 8
	FreeMarker  = uint32(0xFFFFFFFF)
	NoSlot      = int32(-1)

	DefaultPageSize = 4096
	DefaultSlotSize = 128

	FileMagic     = uint32(0x48454150) // "HEAP"
	FormatVersion = uint32(1)
)
