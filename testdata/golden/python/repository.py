"""In-memory persistence for items."""

from __future__ import annotations

from .models import Item


class Repository:
    def __init__(self) -> None:
        self._items: dict[str, Item] = {}

    def add(self, item: Item) -> None:
        self._items[item.sku] = item

    def get(self, sku: str) -> Item | None:
        return self._items.get(sku)
