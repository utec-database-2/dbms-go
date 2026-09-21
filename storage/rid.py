from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True, order=True)
class RID:
    """Identificador de un slot dentro de una página."""

    page_id: int
    slot_id: int

    def __post_init__(self) -> None:
        if self.page_id < 0:
            raise ValueError("page_id debe ser >= 0")
        if self.slot_id < 0:
            raise ValueError("slot_id debe ser >= 0")
