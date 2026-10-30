"""Format shapes for people."""

import string

from shapes import Rect


def describe(r: Rect) -> str:
    """Format a rectangle's size."""
    return "%gx%g" % (r.w, r.h)


def label(s: str) -> str:
    """Capitalise each word of a label."""
    return string.capwords(s)
