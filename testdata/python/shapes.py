"""Shapes and their areas."""

from abc import ABC, abstractmethod


class Shape(ABC):
    """Shape is anything with an area."""

    @abstractmethod
    def area(self) -> float:
        """Return the shape's area."""


class Rect(Shape):
    """Rect is an axis-aligned rectangle."""

    def __init__(self, w: float, h: float) -> None:
        self.w = w
        self.h = h

    def area(self) -> float:
        """Return the rectangle's area."""
        return self.w * self.h


def new_rect(w: float, h: float) -> Rect:
    """Build a Rect, like new_rect(1, 2)."""
    return Rect(w, h)


def total_area(shapes: list[Shape]) -> float:
    """Sum the area of every shape."""
    total = 0.0
    for s in shapes:
        total += s.area()
    return total


def report() -> float:
    """Return the total area of two rectangles."""
    return total_area([new_rect(1, 2), new_rect(3, 4)])
