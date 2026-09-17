from __future__ import annotations

import json
from pathlib import Path
from typing import Any, Iterator

from .page import InvalidSlotError, Page, PageFullError
from .rid import RID


Record = dict[str, Any]


class HeapFileError(Exception):
    pass


class InvalidRIDError(HeapFileError):
    pass


class RecordTooLargeError(HeapFileError):
    pass


class HeapFile:
    """Heap File paginado y persistente con política de inserción first-fit."""

    def __init__(self, path: str | Path, *, page_size: int = 4096):
        self.path = Path(path)
        self.page_size = page_size

        if page_size < 128:
            raise ValueError("page_size debe ser >= 128")
        if page_size > 65535:
            raise ValueError("page_size debe ser <= 65535")

        self.path.parent.mkdir(parents=True, exist_ok=True)
        if not self.path.exists():
            self.path.touch()

        size = self.path.stat().st_size
        if size % self.page_size != 0:
            raise HeapFileError(
                "El tamaño del archivo no es múltiplo de page_size; "
                "podría estar corrupto o usar otro tamaño de página"
            )

    @property
    def page_count(self) -> int:
        return self.path.stat().st_size // self.page_size

    @property
    def file_size(self) -> int:
        return self.path.stat().st_size

    def _serialize(self, record: Record) -> bytes:
        if not isinstance(record, dict):
            raise TypeError("Cada registro debe ser un dict de Python")

        try:
            encoded = json.dumps(
                record,
                ensure_ascii=False,
                separators=(",", ":"),
                sort_keys=True,
            ).encode("utf-8")
        except (TypeError, ValueError) as exc:
            raise TypeError("El registro debe ser serializable a JSON") from exc

        maximum_payload = self.page_size - 8 - 4
        if len(encoded) > maximum_payload:
            raise RecordTooLargeError(
                f"Registro de {len(encoded)} bytes excede el máximo "
                f"de una página de {self.page_size} bytes"
            )
        return encoded

    def _deserialize(self, raw: bytes) -> Record:
        value = json.loads(raw.decode("utf-8"))
        if not isinstance(value, dict):
            raise HeapFileError("El registro persistido no es un objeto")
        return value

    def _load_page(self, page_id: int) -> Page:
        if page_id < 0 or page_id >= self.page_count:
            raise InvalidRIDError(f"page_id={page_id} no existe")

        with self.path.open("rb") as file:
            file.seek(page_id * self.page_size)
            raw = file.read(self.page_size)

        if len(raw) != self.page_size:
            raise HeapFileError(f"No se pudo leer completa la página {page_id}")

        return Page.from_bytes(page_id, raw)

    def _write_page(self, page: Page) -> None:
        if page.page_size != self.page_size:
            raise HeapFileError("La página usa otro page_size")

        raw = page.to_bytes()
        with self.path.open("r+b") as file:
            file.seek(page.page_id * self.page_size)
            file.write(raw)
            file.flush()

    def _append_empty_page(self) -> Page:
        page = Page(page_id=self.page_count, page_size=self.page_size)
        with self.path.open("ab") as file:
            file.write(page.to_bytes())
            file.flush()
        return page

    def insert(self, record: Record) -> RID:
        raw = self._serialize(record)

        for page_id in range(self.page_count):
            page = self._load_page(page_id)
            if page.can_fit(raw):
                slot_id = page.insert(raw)
                self._write_page(page)
                return RID(page_id=page_id, slot_id=slot_id)

        page = self._append_empty_page()
        try:
            slot_id = page.insert(raw)
        except PageFullError as exc:
            raise RecordTooLargeError(
                "El registro no cabe incluso en una página vacía"
            ) from exc

        self._write_page(page)
        return RID(page_id=page.page_id, slot_id=slot_id)

    def read(self, rid: RID) -> Record:
        page = self._page_from_rid(rid)
        try:
            raw = page.read(rid.slot_id)
        except InvalidSlotError as exc:
            raise InvalidRIDError(str(exc)) from exc
        return self._deserialize(raw)

    def delete(self, rid: RID) -> None:
        page = self._page_from_rid(rid)
        try:
            page.delete(rid.slot_id)
        except InvalidSlotError as exc:
            raise InvalidRIDError(str(exc)) from exc
        self._write_page(page)

    def exists(self, rid: RID) -> bool:
        try:
            self.read(rid)
        except InvalidRIDError:
            return False
        return True

    def scan(self) -> Iterator[tuple[RID, Record]]:
        for page_id in range(self.page_count):
            page = self._load_page(page_id)
            for slot_id, raw in enumerate(page.slots):
                if raw is None:
                    continue
                yield RID(page_id, slot_id), self._deserialize(raw)

    def page_stats(self) -> list[dict[str, int]]:
        stats = []
        for page_id in range(self.page_count):
            page = self._load_page(page_id)
            stats.append({
                "page_id": page_id,
                "slots": page.slot_count,
                "active": page.active_count,
                "deleted": page.deleted_count,
                "free_bytes": page.free_bytes,
            })
        return stats

    def _page_from_rid(self, rid: RID) -> Page:
        if not isinstance(rid, RID):
            raise TypeError("Se esperaba una instancia de RID")
        return self._load_page(rid.page_id)
