from __future__ import annotations

from bisect import bisect_left, bisect_right
from dataclasses import dataclass, field
from math import ceil
from typing import Generic, Iterator, Optional, TypeVar

K = TypeVar("K")
V = TypeVar("V")


@dataclass
class BPlusNode(Generic[K, V]):
    is_leaf: bool
    keys: list[K] = field(default_factory=list)
    parent: Optional["BPlusNode[K, V]"] = None

    children: list["BPlusNode[K, V]"] = field(default_factory=list)

    # Solo se usa en hojas.
    # values[i] es el bucket asociado a keys[i].
    values: list[list[V]] = field(default_factory=list)

    # Encadenamiento entre hojas para consultas por rango.
    next: Optional["BPlusNode[K, V]"] = None
    prev: Optional["BPlusNode[K, V]"] = None


class BPlusTree(Generic[K, V]):
    """
    order = máximo número de hijos de un nodo interno.
        max_keys = order - 1.

    Los nodos internos NO guardan registros.
    Las hojas guardan clave -> bucket de valores.
    """

    def __init__(self, order: int = 4):
        if order < 3:
            raise ValueError("order debe ser >= 3")

        self.order = order
        self.root: BPlusNode[K, V] = BPlusNode(is_leaf=True)

    @property
    def max_keys(self) -> int:
        return self.order - 1

    @property
    def min_leaf_keys(self) -> int:
        return ceil(self.max_keys / 2)

    @property
    def min_internal_children(self) -> int:
        return ceil(self.order / 2)

    def _leftmost_leaf(self) -> BPlusNode[K, V]:
        node = self.root

        while not node.is_leaf:
            node = node.children[0]

        return node

    def _subtree_min(self, node: BPlusNode[K, V]) -> K:
        while not node.is_leaf:
            node = node.children[0]

        if not node.keys:
            raise RuntimeError(
                "No se puede obtener el mínimo de un subárbol vacío."
            )

        return node.keys[0]

    def _rebuild_keys(self, node: BPlusNode[K, V]) -> None:
        """
        En un nodo interno, keys[i] es la menor clave del hijo i+1.
        """
        if not node.is_leaf:
            node.keys = [
                self._subtree_min(child)
                for child in node.children[1:]
            ]

    def _refresh_upwards(
        self,
        node: Optional[BPlusNode[K, V]],
    ) -> None:
        while node is not None:
            if not node.is_leaf:
                self._rebuild_keys(node)

            node = node.parent

    def _find_leaf(self, key: K) -> BPlusNode[K, V]:
        node = self.root

        while not node.is_leaf:
            child_index = bisect_right(node.keys, key)
            node = node.children[child_index]

        return node

    def insert(self, key: K, value: V) -> None:
        leaf = self._find_leaf(key)
        index = bisect_left(leaf.keys, key)

        if index < len(leaf.keys) and leaf.keys[index] == key:
            leaf.values[index].append(value)
            return

        leaf.keys.insert(index, key)
        leaf.values.insert(index, [value])

        if len(leaf.keys) > self.max_keys:
            self._split_leaf(leaf)
        else:
            self._refresh_upwards(leaf.parent)

    def _split_leaf(self, leaf: BPlusNode[K, V]) -> None:
        split_index = ceil(len(leaf.keys) / 2)

        right = BPlusNode[K, V](
            is_leaf=True,
            parent=leaf.parent,
        )

        right.keys = leaf.keys[split_index:]
        right.values = leaf.values[split_index:]

        leaf.keys = leaf.keys[:split_index]
        leaf.values = leaf.values[:split_index]

        right.next = leaf.next

        if right.next is not None:
            right.next.prev = right

        leaf.next = right
        right.prev = leaf

        self._insert_right_sibling(leaf, right)

    def _insert_right_sibling(
        self,
        left: BPlusNode[K, V],
        right: BPlusNode[K, V],
    ) -> None:
        if left.parent is None:
            new_root = BPlusNode[K, V](is_leaf=False)

            new_root.children = [left, right]

            left.parent = new_root
            right.parent = new_root

            self._rebuild_keys(new_root)
            self.root = new_root
            return

        parent = left.parent
        left_index = parent.children.index(left)

        parent.children.insert(left_index + 1, right)
        right.parent = parent

        self._rebuild_keys(parent)

        if len(parent.children) > self.order:
            self._split_internal(parent)
        else:
            self._refresh_upwards(parent.parent)

    def _split_internal(self, node: BPlusNode[K, V]) -> None:
        split_index = ceil(len(node.children) / 2)

        right = BPlusNode[K, V](
            is_leaf=False,
            parent=node.parent,
        )

        right.children = node.children[split_index:]
        node.children = node.children[:split_index]

        for child in right.children:
            child.parent = right

        self._rebuild_keys(node)
        self._rebuild_keys(right)

        self._insert_right_sibling(node, right)

    def search(self, key: K) -> list[V]:
        leaf = self._find_leaf(key)
        index = bisect_left(leaf.keys, key)

        if index < len(leaf.keys) and leaf.keys[index] == key:
            return list(leaf.values[index])

        return []

    def range_search(
        self,
        start: Optional[K] = None,
        end: Optional[K] = None,
        *,
        include_start: bool = True,
        include_end: bool = True,
    ) -> list[tuple[K, V]]:
        if start is None:
            leaf = self._leftmost_leaf()
            index = 0
        else:
            leaf = self._find_leaf(start)

            if include_start:
                index = bisect_left(leaf.keys, start)
            else:
                index = bisect_right(leaf.keys, start)

        result: list[tuple[K, V]] = []

        while leaf is not None:
            while index < len(leaf.keys):
                key = leaf.keys[index]

                if end is not None:
                    if key > end:
                        return result

                    if key == end and not include_end:
                        return result

                for value in leaf.values[index]:
                    result.append((key, value))

                index += 1

            leaf = leaf.next
            index = 0

        return result

    def delete(
        self,
        key: K,
        value: Optional[V] = None,
    ) -> bool:
        leaf = self._find_leaf(key)
        index = bisect_left(leaf.keys, key)

        if index >= len(leaf.keys) or leaf.keys[index] != key:
            return False

        # Si se especifica un valor, elimina solo esa entrada
        # dentro del bucket de la clave.
        if value is not None:
            try:
                leaf.values[index].remove(value)
            except ValueError:
                return False

            if leaf.values[index]:
                return True

        # Si el bucket quedó vacío, se elimina la clave completa.
        leaf.keys.pop(index)
        leaf.values.pop(index)

        if leaf is self.root:
            return True

        if len(leaf.keys) < self.min_leaf_keys:
            self._rebalance_leaf(leaf)
        else:
            self._refresh_upwards(leaf.parent)

        return True

    def _rebalance_leaf(self, leaf: BPlusNode[K, V]) -> None:
        parent = leaf.parent

        if parent is None:
            return

        index = parent.children.index(leaf)

        left = (
            parent.children[index - 1]
            if index > 0
            else None
        )

        right = (
            parent.children[index + 1]
            if index + 1 < len(parent.children)
            else None
        )

        # 1. Intentar pedir prestado al hermano izquierdo.
        if (
            left is not None
            and len(left.keys) > self.min_leaf_keys
        ):
            leaf.keys.insert(0, left.keys.pop())
            leaf.values.insert(0, left.values.pop())

            self._refresh_upwards(parent)
            return

        # 2. Intentar pedir prestado al hermano derecho.
        if (
            right is not None
            and len(right.keys) > self.min_leaf_keys
        ):
            leaf.keys.append(right.keys.pop(0))
            leaf.values.append(right.values.pop(0))

            self._refresh_upwards(parent)
            return

        # 3. Si nadie puede prestar, fusionar.
        if left is not None:
            left.keys.extend(leaf.keys)
            left.values.extend(leaf.values)

            left.next = leaf.next

            if leaf.next is not None:
                leaf.next.prev = left

            parent.children.pop(index)

        elif right is not None:
            leaf.keys.extend(right.keys)
            leaf.values.extend(right.values)

            leaf.next = right.next

            if right.next is not None:
                right.next.prev = leaf

            parent.children.pop(index + 1)

        else:
            raise RuntimeError("La hoja no tiene hermanos.")

        self._after_child_removed(parent)

    def _after_child_removed(
        self,
        node: BPlusNode[K, V],
    ) -> None:
        if node is self.root:
            if len(node.children) == 1:
                self.root = node.children[0]
                self.root.parent = None
            else:
                self._rebuild_keys(node)

            return

        self._rebuild_keys(node)

        if len(node.children) < self.min_internal_children:
            self._rebalance_internal(node)
        else:
            self._refresh_upwards(node.parent)

    def _rebalance_internal(
        self,
        node: BPlusNode[K, V],
    ) -> None:
        parent = node.parent

        if parent is None:
            return

        index = parent.children.index(node)

        left = (
            parent.children[index - 1]
            if index > 0
            else None
        )

        right = (
            parent.children[index + 1]
            if index + 1 < len(parent.children)
            else None
        )

        if (
            left is not None
            and len(left.children) > self.min_internal_children
        ):
            child = left.children.pop()
            child.parent = node
            node.children.insert(0, child)

            self._rebuild_keys(left)
            self._rebuild_keys(node)
            self._refresh_upwards(parent)
            return

        if (
            right is not None
            and len(right.children) > self.min_internal_children
        ):
            child = right.children.pop(0)
            child.parent = node
            node.children.append(child)

            self._rebuild_keys(right)
            self._rebuild_keys(node)
            self._refresh_upwards(parent)
            return

        if left is not None:
            for child in node.children:
                child.parent = left

            left.children.extend(node.children)
            self._rebuild_keys(left)

            parent.children.pop(index)

        elif right is not None:
            for child in right.children:
                child.parent = node

            node.children.extend(right.children)
            self._rebuild_keys(node)

            parent.children.pop(index + 1)

        else:
            raise RuntimeError(
                "El nodo interno no tiene hermanos."
            )

        self._after_child_removed(parent)

    def items(self) -> Iterator[tuple[K, V]]:
        leaf = self._leftmost_leaf()

        while leaf is not None:
            for key, bucket in zip(
                leaf.keys,
                leaf.values,
            ):
                for value in bucket:
                    yield key, value

            leaf = leaf.next

    def validate(self) -> None:
        """
        Validador de invariantes.
        """
        leaf_depths: set[int] = set()

        def walk(
            node: BPlusNode[K, V],
            depth: int,
        ):
            assert node.keys == sorted(node.keys)

            if node.is_leaf:
                assert len(node.keys) == len(node.values)
                assert all(bucket for bucket in node.values)

                if node is not self.root:
                    assert (
                        self.min_leaf_keys
                        <= len(node.keys)
                        <= self.max_keys
                    )
                else:
                    assert len(node.keys) <= self.max_keys

                leaf_depths.add(depth)

                if not node.keys:
                    return None, None

                return node.keys[0], node.keys[-1]

            assert len(node.children) == len(node.keys) + 1

            if node is self.root:
                assert 2 <= len(node.children) <= self.order
            else:
                assert (
                    self.min_internal_children
                    <= len(node.children)
                    <= self.order
                )

            ranges = []

            for child in node.children:
                assert child.parent is node
                ranges.append(walk(child, depth + 1))

            expected_keys = [
                ranges[i][0]
                for i in range(1, len(ranges))
            ]

            assert node.keys == expected_keys

            for i in range(len(ranges) - 1):
                assert ranges[i][1] <= ranges[i + 1][0]

            return ranges[0][0], ranges[-1][1]

        walk(self.root, 0)

        assert len(leaf_depths) == 1

        previous = None
        leaf = self._leftmost_leaf()
        seen_keys: list[K] = []

        while leaf is not None:
            assert leaf.prev is previous
            seen_keys.extend(leaf.keys)

            previous = leaf
            leaf = leaf.next

        assert seen_keys == sorted(seen_keys)
