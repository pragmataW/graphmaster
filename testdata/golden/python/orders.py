"""Order placement."""

from ..models import Item
from ..repository import Repository
from . import pricing


class OutOfStock(Exception):
    pass


class OrderService:
    def __init__(self, repo: Repository) -> None:
        self.repo = repo

    def place_order(self, sku: str, qty: int) -> int:
        item = self.repo.get(sku)
        if item is None or not self._can_fulfil(item, qty):
            raise OutOfStock(sku)
        item.quantity -= qty
        return pricing.apply_discount(item.price * qty)

    def _can_fulfil(self, item: Item, qty: int) -> bool:
        return 0 < qty <= item.quantity
