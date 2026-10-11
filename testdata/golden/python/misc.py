"""Covers the Python extraction surface not exercised by the fixture."""

import abc
import os.path
import numpy as np
from enum import Enum
from typing import Generic, Optional, Protocol, TypeVar
from .base import BaseModel as Model, Mixin
from ..util import *

T = TypeVar("T")
MAX_RETRIES = 3
default_name = "misc"


class Color(Enum):
    RED = 1
    GREEN = 2


class Readable(Protocol):
    def read(self, n: int) -> bytes: ...


class Box(Model, Mixin, Generic[T]):
    """A typed container."""

    count: int = 0
    label: Optional[str] = None

    class Meta:
        ordering = ["count"]

    def __init__(self, item: T, reader: Readable | None = None) -> None:
        self.item = item
        self.reader = reader

    @property
    def size(self) -> int:
        return len(self.peek())

    @staticmethod
    def empty() -> "Box":
        return Box(None)

    @classmethod
    def of(cls, item: T) -> "Box[T]":
        return cls(item)

    @abc.abstractmethod
    async def fetch(self, path: os.PathLike) -> bytes:
        data = await self.reader.read(10)
        return data

    def peek(self) -> list[T]:
        def helper(x):
            return [x]

        try:
            return helper(self.item)
        except (KeyError, ValueError) as err:
            raise LookupFailed(str(err))
        except np.AxisError:
            raise


class LookupFailed(Exception):
    pass


def _private_helper(box: Box[int]) -> Model:
    if isinstance(box, Box):
        box.peek()
    return Model.objects.get(id=box.count)


type Pair = tuple[int, int]
