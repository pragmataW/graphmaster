"""Pricing rules."""

DISCOUNT_THRESHOLD = 10_000


def apply_discount(amount: int) -> int:
    if amount >= DISCOUNT_THRESHOLD:
        return round_cents(amount * 0.9)
    return amount


def round_cents(value: float) -> int:
    return int(round(value))
