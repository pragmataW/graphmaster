import pytest

from shop.models import Item
from shop.repository import Repository
from shop.services.orders import OrderService, OutOfStock


def make_service() -> OrderService:
    repo = Repository()
    repo.add(Item("a1", 500, 3))
    return OrderService(repo)


def test_place_order():
    svc = make_service()
    assert svc.place_order("a1", 2) == 1000


def test_place_order_out_of_stock():
    with pytest.raises(OutOfStock):
        make_service().place_order("a1", 9)


class TestPricing:
    def test_discount_applies(self):
        from shop.services.pricing import apply_discount

        assert apply_discount(20_000) == 18_000
