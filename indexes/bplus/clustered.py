from __future__ import annotations

from typing import Any, Generic, TypeVar

from storage import RID, SequentialPagedFile

from .core import BPlusTree

K = TypeVar("K")


class ClusteredBPlusIndex(Generic[K]):

    def __init__(
        self,
        order: int = 4,
        *,
        storage: SequentialPagedFile | None = None,
    ):
        self.order = order
        self.storage = storage

        if storage is None:
            self.tree: BPlusTree[K, dict[str, Any] | RID] = BPlusTree(order)
        else:
            self.tree = BPlusTree(order)
            self.rebuild()

    @property
    def integrated(self) -> bool:
        return self.storage is not None

    def rebuild(self) -> None:
        if self.storage is None:
            return

        self.tree = BPlusTree(self.order)

        for rid, record in self.storage.scan():
            key = record[self.storage.key_field]
            self.tree.insert(key, rid)

        self.tree.validate()

    def insert(
        self,
        key: K,
        record: dict[str, Any],
    ) -> None:
        if self.storage is None:
            self.tree.insert(key, record)
            return

        if self.storage.key_field not in record:
            raise KeyError(
                f"El registro no contiene '{self.storage.key_field}'"
            )

        record_key = record[self.storage.key_field]
        if record_key != key:
            raise ValueError(
                "La key entregada al índice no coincide con el campo de "
                "ordenamiento del archivo secuencial"
            )

        self.storage.insert(record)
        self.rebuild()

    def search(self, key: K) -> list[dict[str, Any]]:
        values = self.tree.search(key)

        if self.storage is None:
            return list(values)

        return [self.storage.read(rid) for rid in values]

    def range_search(
        self,
        start: K,
        end: K,
    ) -> list[dict[str, Any]]:
        entries = self.tree.range_search(start, end)

        if self.storage is None:
            return [record for _, record in entries]

        return [
            self.storage.read(rid)
            for _, rid in entries
        ]

    def delete(
        self,
        key: K,
        record: dict[str, Any] | None = None,
    ) -> bool:
        if self.storage is None:
            return self.tree.delete(key, record)

        rids = self.tree.search(key)

        for rid in rids:
            current = self.storage.read(rid)

            if record is None or current == record:
                self.storage.delete(rid)
                self.rebuild()
                return True

        return False
