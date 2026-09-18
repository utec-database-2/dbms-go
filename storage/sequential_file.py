from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Iterator, Optional

from .page import InvalidSlotError, Page, PageFullError
from .rid import RID


Record = dict[str, Any]
ACTIVE = 1
DELETED = 0


class SequentialFileError(Exception):
    pass


class SequentialRecordTooLargeError(SequentialFileError):
    pass


class SequentialInvalidRIDError(SequentialFileError):
    pass


@dataclass
class _Entry:
    key: Any
    raw: bytes
    deleted: bool
    marker: object | None = None


class SequentialPagedFile:
    """
    Archivo secuencial paginado y persistente.

    Los registros se mantienen ordenados por ``key_field`` y tal como definimos nuestro page
    este tiene (page_id, slot_id). Cada entrada tiene un byte de estado:

        0x01 -> activa
        0x00 -> eliminada lógicamente (tombstone)

    La inserción reconstruye las páginas conservando los tombstones. algo tipoco de 
    archivo secuencial ordenado y favorablepara compararlo contra Heap File.

    La reorganización elimina físicamente los tombstones y vuelve a empaquetar
    únicamente los registros activos.

    Nota sobre RID:
        una inserción o reorganización puede mover registros entre páginas,
        por lo que los RID de este archivo NO son estables frente a esas
        operaciones. El índice B+ agrupado reconstruye sus referencias después
        de cada mutación del almacenamiento.
    """

    def __init__(
        self,
        path: str | Path,
        *,
        key_field: str,
        page_size: int = 4096,
        reorganize_threshold: float = 0.30,
        auto_reorganize: bool = True,
    ):
        if not key_field:
            raise ValueError("key_field no puede estar vacío")
        if page_size < 128:
            raise ValueError("page_size debe ser >= 128")
        if page_size > 65535:
            raise ValueError("page_size debe ser <= 65535")
        if not 0.0 < reorganize_threshold <= 1.0:
            raise ValueError("reorganize_threshold debe estar en (0, 1]")

        self.path = Path(path)
        self.key_field = key_field
        self.page_size = page_size
        self.reorganize_threshold = reorganize_threshold
        self.auto_reorganize = auto_reorganize

        self.path.parent.mkdir(parents=True, exist_ok=True)
        if not self.path.exists():
            self.path.touch()

        size = self.path.stat().st_size
        if size % self.page_size != 0:
            raise SequentialFileError(
                "El tamaño del archivo no es múltiplo de page_size; "
                "podría estar corrupto o usar otro tamaño de página"
            )

        # Detecta pronto un archivo válido pero desordenado o registros sin la
        # clave configurada. En un proyecto real esta validación podría ser
        # opcional por costo.
        self.validate_order()

    @property
    def page_count(self) -> int:
        return self.path.stat().st_size // self.page_size

    @property
    def file_size(self) -> int:
        return self.path.stat().st_size

    @property
    def active_count(self) -> int:
        return sum(1 for _ in self.scan())

    @property
    def tombstone_count(self) -> int:
        return sum(1 for _ in self.scan(include_deleted=True) if _[2])

    def _serialize_record(self, record: Record, *, deleted: bool) -> bytes:
        if not isinstance(record, dict):
            raise TypeError("Cada registro debe ser un dict de Python")
        if self.key_field not in record:
            raise KeyError(
                f"El registro no contiene la clave de ordenamiento "
                f"'{self.key_field}'"
            )

        try:
            payload = json.dumps(
                record,
                ensure_ascii=False,
                separators=(",", ":"),
                sort_keys=True,
            ).encode("utf-8")
        except (TypeError, ValueError) as exc:
            raise TypeError("El registro debe ser serializable a JSON") from exc

        raw = bytes([DELETED if deleted else ACTIVE]) + payload
        maximum_payload = self.page_size - 8 - 4
        if len(raw) > maximum_payload:
            raise SequentialRecordTooLargeError(
                f"Registro de {len(raw)} bytes excede el máximo de una "
                f"página de {self.page_size} bytes"
            )
        return raw

    def _decode_raw(self, raw: bytes) -> tuple[bool, Record]:
        if len(raw) < 2:
            raise SequentialFileError("Entrada secuencial corrupta")

        status = raw[0]
        if status not in (ACTIVE, DELETED):
            raise SequentialFileError("Estado de registro inválido")

        try:
            record = json.loads(raw[1:].decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise SequentialFileError("Registro secuencial corrupto") from exc

        if not isinstance(record, dict):
            raise SequentialFileError("El registro persistido no es un objeto")
        if self.key_field not in record:
            raise SequentialFileError(
                f"Registro persistido sin key_field='{self.key_field}'"
            )

        return status == DELETED, record

    def _load_page(self, page_id: int) -> Page:
        if page_id < 0 or page_id >= self.page_count:
            raise SequentialInvalidRIDError(f"page_id={page_id} no existe")

        with self.path.open("rb") as file:
            file.seek(page_id * self.page_size)
            raw = file.read(self.page_size)

        if len(raw) != self.page_size:
            raise SequentialFileError(
                f"No se pudo leer completa la página {page_id}"
            )

        return Page.from_bytes(page_id, raw)

    def _write_page(self, page: Page) -> None:
        if page.page_size != self.page_size:
            raise SequentialFileError("La página usa otro page_size")

        with self.path.open("r+b") as file:
            file.seek(page.page_id * self.page_size)
            file.write(page.to_bytes())
            file.flush()

    def _entries(self) -> list[_Entry]:
        entries: list[_Entry] = []

        for page_id in range(self.page_count):
            page = self._load_page(page_id)
            for raw in page.slots:
                if raw is None:
                    # SequentialPagedFile no usa None como tombstone. Si
                    # apareciera, se trata como espacio físico vacío heredado.
                    continue

                deleted, record = self._decode_raw(raw)
                entries.append(
                    _Entry(
                        key=record[self.key_field],
                        raw=raw,
                        deleted=deleted,
                    )
                )

        return entries

    def _rewrite_entries(
        self,
        entries: list[_Entry],
        *,
        tracked_marker: object | None = None,
    ) -> Optional[RID]:
        """Reescribe todo el archivo y opcionalmente localiza una entrada."""
        pages: list[Page] = []
        tracked_rid: Optional[RID] = None

        for entry in entries:
            if not pages:
                pages.append(Page(page_id=0, page_size=self.page_size))

            page = pages[-1]

            if not page.can_fit(entry.raw):
                page = Page(
                    page_id=len(pages),
                    page_size=self.page_size,
                )
                pages.append(page)

            try:
                slot_id = page.insert(entry.raw)
            except PageFullError as exc:
                raise SequentialRecordTooLargeError(
                    "El registro no cabe incluso en una página vacía"
                ) from exc

            if tracked_marker is not None and entry.marker is tracked_marker:
                tracked_rid = RID(page.page_id, slot_id)

        # Un archivo sin registros queda realmente vacío: 0 páginas.
        with self.path.open("wb") as file:
            for page in pages:
                file.write(page.to_bytes())
            file.flush()

        return tracked_rid

    def insert(self, record: Record) -> RID:
        raw = self._serialize_record(record, deleted=False)
        marker = object()

        entries = self._entries()
        entries.append(
            _Entry(
                key=record[self.key_field],
                raw=raw,
                deleted=False,
                marker=marker,
            )
        )

        try:
            # sort es estable: para claves repetidas preserva el orden previo y
            # coloca la nueva entrada después de las ya existentes.
            entries.sort(key=lambda entry: entry.key)
        except TypeError as exc:
            raise TypeError(
                "Todas las claves del archivo secuencial deben ser comparables "
                "entre sí"
            ) from exc

        rid = self._rewrite_entries(entries, tracked_marker=marker)
        if rid is None:
            raise SequentialFileError("No se pudo localizar el registro insertado")

        return rid

    def read(self, rid: RID, *, include_deleted: bool = False) -> Record:
        page = self._page_from_rid(rid)

        try:
            raw = page.read(rid.slot_id)
        except InvalidSlotError as exc:
            raise SequentialInvalidRIDError(str(exc)) from exc

        deleted, record = self._decode_raw(raw)
        if deleted and not include_deleted:
            raise SequentialInvalidRIDError(
                f"RID {rid} apunta a un registro eliminado lógicamente"
            )

        return record

    def delete(self, rid: RID) -> bool:
        """
        Marca un registro como tombstone.

        Retorna True si la eliminación provocó reorganización automática.
        """
        page = self._page_from_rid(rid)

        try:
            raw = page.read(rid.slot_id)
        except InvalidSlotError as exc:
            raise SequentialInvalidRIDError(str(exc)) from exc

        deleted, _ = self._decode_raw(raw)
        if deleted:
            raise SequentialInvalidRIDError(
                f"RID {rid} ya está eliminado lógicamente"
            )

        # Mismo tamaño, solo cambia el byte de estado. No se recupera espacio.
        page.slots[rid.slot_id] = bytes([DELETED]) + raw[1:]
        self._write_page(page)

        if (
            self.auto_reorganize
            and self.waste_ratio() > self.reorganize_threshold
        ):
            self.reorganize()
            return True

        return False

    def reorganize(self) -> None:
        """Elimina tombstones y vuelve a empaquetar los registros activos."""
        active_entries = [entry for entry in self._entries() if not entry.deleted]
        # Ya vienen ordenadas físicamente; ordenar nuevamente protege el
        # invariante si el archivo fue manipulado externamente.
        active_entries.sort(key=lambda entry: entry.key)
        self._rewrite_entries(active_entries)

    def scan(
        self,
        *,
        include_deleted: bool = False,
    ) -> Iterator[tuple[RID, Record] | tuple[RID, Record, bool]]:
        for page_id in range(self.page_count):
            page = self._load_page(page_id)
            for slot_id, raw in enumerate(page.slots):
                if raw is None:
                    continue

                deleted, record = self._decode_raw(raw)
                rid = RID(page_id, slot_id)

                if include_deleted:
                    yield rid, record, deleted
                elif not deleted:
                    yield rid, record

    def find_by_key(self, key: Any) -> list[tuple[RID, Record]]:
        result: list[tuple[RID, Record]] = []

        for rid, record in self.scan():
            record_key = record[self.key_field]

            if record_key < key:
                continue
            if record_key > key:
                break

            result.append((rid, record))

        return result

    def range_scan(
        self,
        start: Any,
        end: Any,
        *,
        include_start: bool = True,
        include_end: bool = True,
    ) -> list[tuple[RID, Record]]:
        result: list[tuple[RID, Record]] = []

        for rid, record in self.scan():
            key = record[self.key_field]

            if key < start or (key == start and not include_start):
                continue

            if key > end or (key == end and not include_end):
                break

            result.append((rid, record))

        return result

    def waste_ratio(self) -> float:
        """
        Porcentaje de bytes de entradas ocupados por tombstones.

        waste = deleted_entry_bytes / total_entry_bytes

        Se mide sobre bytes de registros y no sobre el archivo completo para
        que el espacio libre normal de la última página no se confunda con
        desperdicio causado por eliminaciones lazy.
        """
        total = 0
        deleted = 0

        for entry in self._entries():
            size = len(entry.raw)
            total += size
            if entry.deleted:
                deleted += size

        if total == 0:
            return 0.0

        return deleted / total

    def page_stats(self) -> list[dict[str, int]]:
        stats: list[dict[str, int]] = []

        for page_id in range(self.page_count):
            page = self._load_page(page_id)
            active = 0
            tombstones = 0

            for raw in page.slots:
                if raw is None:
                    continue
                deleted, _ = self._decode_raw(raw)
                if deleted:
                    tombstones += 1
                else:
                    active += 1

            stats.append(
                {
                    "page_id": page_id,
                    "slots": page.slot_count,
                    "active": active,
                    "tombstones": tombstones,
                    "free_bytes": page.free_bytes,
                }
            )

        return stats

    def validate_order(self) -> None:
        previous_key: Any = None
        has_previous = False

        for item in self.scan(include_deleted=True):
            _, record, _ = item
            key = record[self.key_field]

            if has_previous:
                try:
                    ordered = previous_key <= key
                except TypeError as exc:
                    raise SequentialFileError(
                        "Las claves persistidas no son comparables entre sí"
                    ) from exc

                if not ordered:
                    raise SequentialFileError(
                        "El archivo secuencial no está ordenado por "
                        f"'{self.key_field}'"
                    )

            previous_key = key
            has_previous = True

    def _page_from_rid(self, rid: RID) -> Page:
        if not isinstance(rid, RID):
            raise TypeError("Se esperaba una instancia de RID")
        return self._load_page(rid.page_id)
