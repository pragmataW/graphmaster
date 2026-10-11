import unittest

import pytest

from misc import Box


@pytest.fixture
def box():
    return Box(1)


def test_box_size(box):
    assert box.size == 1


@pytest.mark.asyncio
async def test_fetch(box):
    await box.fetch("x")


class TestBox:
    def test_peek(self, box):
        assert box.peek() == [1]

    def helper(self):
        return None


class BoxCase(unittest.TestCase):
    def setUp(self):
        self.box = Box(2)

    def test_empty(self):
        self.assertIsNone(Box.empty().item)


def nested_test_holder():
    def test_not_collected():
        pass
