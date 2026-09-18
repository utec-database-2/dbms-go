from __future__ import annotations

import struct
from dataclasses import dataclass, field
from typing import Optional


PAGE_MAGIC = b"PG01"
PAGE_HEADER = struct.Struct("<4sHH")
## little endiam, page magic, slot count, reserved // page_header.size() = 8
SLOT_ENTRY = struct.Struct("<HH") #para cada slot, offset y length // slot_entry.size() = 4 // offset= donde comienza


class PageError(Exception):
    pass


class PageFullError(PageError):
    pass


class InvalidSlotError(PageError):
    pass


@dataclass
class Page:
    """Página de tamaño fijo con organización tipo slotted-page."""

    page_id: int
    page_size: int = 4096
    slots: list[Optional[bytes]] = field(default_factory=list)

    def __post_init__(self) -> None:
        if self.page_id < 0:
            raise ValueError("page_id debe ser >= 0")
        if self.page_size < 128:
            raise ValueError("page_size debe ser >= 128 bytes")
        if self.page_size > 65535:
            raise ValueError("page_size debe ser <= 65535 bytes")

    @property
    def slot_count(self) -> int:
        return len(self.slots)

    @property
    def active_count(self) -> int:
        return sum(record is not None for record in self.slots)

    @property
    def deleted_count(self) -> int:
        return self.slot_count - self.active_count

    @property
    def payload_bytes(self) -> int:
        return sum(len(record) for record in self.slots if record is not None) # bytes ocupados por los registros

    @property
    def directory_bytes(self) -> int:
        return PAGE_HEADER.size + self.slot_count * SLOT_ENTRY.size # bytes ocupados por los slots y la cabecera de la página

    @property
    def free_bytes(self) -> int:
        return self.page_size - self.directory_bytes - self.payload_bytes # bytes libres en la pagina

    def _deleted_slot(self) -> Optional[int]:
        for slot_id, record in enumerate(self.slots):
            if record is None:
                return slot_id # busca el primer slot eliminado/vacio
        return None

    def required_bytes_for_insert(self, record: bytes) -> int:
        extra_directory = 0 if self._deleted_slot() is not None else SLOT_ENTRY.size
        return len(record) + extra_directory # tam en bytes + slot 

    def can_fit(self, record: bytes) -> bool:
        return self.required_bytes_for_insert(record) <= self.free_bytes

    def insert(self, record: bytes) -> int:
        if not isinstance(record, (bytes, bytearray)):
            raise TypeError("record debe estar serializado como bytes")
        record = bytes(record)
        if not record:
            raise ValueError("No se permiten registros serializados vacíos")
        if not self.can_fit(record):
            raise PageFullError(f"El registro no cabe en la página {self.page_id}")

        reusable = self._deleted_slot()
        if reusable is not None:
            self.slots[reusable] = record
            return reusable

        self.slots.append(record)
        return len(self.slots) - 1

    def read(self, slot_id: int) -> bytes:
        self._validate_slot_id(slot_id)
        record = self.slots[slot_id]
        if record is None:
            raise InvalidSlotError(
                f"El slot {slot_id} de la página {self.page_id} está eliminado"
            )
        return record

    def delete(self, slot_id: int) -> None:
        self._validate_slot_id(slot_id)
        if self.slots[slot_id] is None:
            raise InvalidSlotError(
                f"El slot {slot_id} de la página {self.page_id} ya está eliminado"
            )
        self.slots[slot_id] = None

    def _validate_slot_id(self, slot_id: int) -> None:
        if slot_id < 0 or slot_id >= len(self.slots):
            raise InvalidSlotError(
                f"slot_id={slot_id} no existe en página {self.page_id}"
            )

    def to_bytes(self) -> bytes:
        if self.directory_bytes + self.payload_bytes > self.page_size:
            raise PageFullError(f"Página {self.page_id} excede su tamaño")

        raw = bytearray(self.page_size)
        PAGE_HEADER.pack_into(raw, 0, PAGE_MAGIC, self.slot_count, 0)

        payload_cursor = self.page_size
        directory_cursor = PAGE_HEADER.size

        for record in self.slots:
            if record is None:
                SLOT_ENTRY.pack_into(raw, directory_cursor, 0, 0)
            else:
                payload_cursor -= len(record)
                raw[payload_cursor:payload_cursor + len(record)] = record
                SLOT_ENTRY.pack_into(
                    raw,
                    directory_cursor,
                    payload_cursor,
                    len(record),
                )
            directory_cursor += SLOT_ENTRY.size

        if directory_cursor > payload_cursor:
            raise PageFullError("Directorio y payload se solapan")

        return bytes(raw)

    @classmethod
    def from_bytes(cls, page_id: int, raw: bytes) -> "Page":
        if len(raw) < PAGE_HEADER.size:
            raise PageError("Datos insuficientes para una página")

        magic, slot_count, _ = PAGE_HEADER.unpack_from(raw, 0)
        if magic != PAGE_MAGIC:
            raise PageError(f"Página {page_id} tiene una cabecera inválida")

        page = cls(page_id=page_id, page_size=len(raw))
        directory_cursor = PAGE_HEADER.size

        if directory_cursor + slot_count * SLOT_ENTRY.size > len(raw):
            raise PageError(f"Directorio corrupto en página {page_id}")

        entries = []
        for _ in range(slot_count):
            offset, length = SLOT_ENTRY.unpack_from(raw, directory_cursor)
            entries.append((offset, length))
            directory_cursor += SLOT_ENTRY.size

        directory_end = PAGE_HEADER.size + slot_count * SLOT_ENTRY.size
        for offset, length in entries:
            if offset == 0 and length == 0:
                page.slots.append(None)
                continue
            end = offset + length
            if offset < directory_end or end > len(raw):
                raise PageError(f"Slot corrupto en página {page_id}")
            page.slots.append(bytes(raw[offset:end]))

        return page
