from __future__ import annotations

from dataclasses import dataclass
from typing import Generic, TypeVar

from .core import BPlusTree

K = TypeVar("K")


@dataclass(frozen=True)
class RID:
    """
    page_id:
        Identificador de la página del archivo.

    slot_id:
        Posición del registro dentro de esa página.
    """

    page_id: int
    slot_id: int


class UnclusteredBPlusIndex(Generic[K]):
    """

    Las hojas NO almacenan el registro completo.
    Almacenan:

        clave -> uno o más RID(page_id, slot_id)

    El archivo de datos puede mantener un orden físico distinto.
    """

    def __init__(self, order: int = 4):
        self.tree: BPlusTree[K, RID] = BPlusTree(order)

    def insert(
        self,
        key: K,
        rid: RID,
    ) -> None:
        self.tree.insert(key, rid)

    def search(
        self,
        key: K,
    ) -> list[RID]:
        return self.tree.search(key)

    def range_search(
        self,
        start: K,
        end: K,
    ) -> list[tuple[K, RID]]:
        return self.tree.range_search(start, end)

    def delete(
        self,
        key: K,
        rid: RID | None = None,
    ) -> bool:
        return self.tree.delete(key, rid)
