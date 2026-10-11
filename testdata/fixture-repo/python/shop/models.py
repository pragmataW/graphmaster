"""Domain models."""

from dataclasses import dataclass


@dataclass
class Item:
    sku: str
    price: int
    quantity: int = 0

    def total(self) -> int:
        return self.price * self.quantity

    def describe(self) -> str:
        return f"{self.sku} x{self.quantity}"


@dataclass
class PerishableItem(Item):
    days_left: int = 7

    def total(self) -> int:
        base = super().total()
        return base // 2 if self.days_left < 2 else base
