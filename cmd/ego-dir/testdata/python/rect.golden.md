# Ego: `Rect`

- kind: class
- location: shapes.py:14-23
- depth: 1

## Signature

```python
class Rect(w: float, h: float)
```

## Doc

Rect is an axis-aligned rectangle.

## Body

```python
class Rect(Shape):
    """Rect is an axis-aligned rectangle."""

    def __init__(self, w: float, h: float) -> None:
        self.w = w
        self.h = h

    def area(self) -> float:
        """Return the rectangle's area."""
        return self.w * self.h
```

## Upstream (callers)

### Hop 1

- `describe` (function) report/report.py:8-10
  - signature: `def describe(r: Rect) -> str`
  - doc: Format a rectangle's size.

  ```python
  def describe(r: Rect) -> str:
      """Format a rectangle's size."""
      return "%gx%g" % (r.w, r.h)
  ```
- `shapes` (module) shapes
  - signature: `module shapes`
  - doc: Shapes and their areas.
- `new_rect` (function) shapes.py:26-28
  - signature: `def new_rect(w: float, h: float) -> Rect`
  - doc: Build a Rect, like new_rect(1, 2).

  ```python
  def new_rect(w: float, h: float) -> Rect:
      """Build a Rect, like new_rect(1, 2)."""
      return Rect(w, h)
  ```

## Downstream (callees)

### Hop 1

- `__init__` (method) shapes.py:17-19
  - signature: `def __init__(self: Self@Rect, w: float, h: float) -> None`

  ```python
  def __init__(self, w: float, h: float) -> None:
      self.w = w
      self.h = h
  ```
- `w` (field) shapes.py:18
  - signature: `w: float`

  ```python
  w
  ```
- `h` (field) shapes.py:19
  - signature: `h: float`

  ```python
  h
  ```
- `area` (method) shapes.py:21-23
  - signature: `def area(self: Self@Rect) -> float`
  - doc: Return the rectangle's area.

  ```python
  def area(self) -> float:
      """Return the rectangle's area."""
      return self.w * self.h
  ```

## Implementations

_none_

## Members

- parent: `shapes` (module) shapes
  - signature: `module shapes`
  - doc: Shapes and their areas.
- `__init__` (method) shapes.py:17-19
  - signature: `def __init__(self: Self@Rect, w: float, h: float) -> None`

  ```python
  def __init__(self, w: float, h: float) -> None:
      self.w = w
      self.h = h
  ```
- `w` (field) shapes.py:18
  - signature: `w: float`

  ```python
  w
  ```
- `h` (field) shapes.py:19
  - signature: `h: float`

  ```python
  h
  ```
- `area` (method) shapes.py:21-23
  - signature: `def area(self: Self@Rect) -> float`
  - doc: Return the rectangle's area.

  ```python
  def area(self) -> float:
      """Return the rectangle's area."""
      return self.w * self.h
  ```

## Type dependencies

- `new_rect` (function) shapes.py:26-28
  - signature: `def new_rect(w: float, h: float) -> Rect`
  - doc: Build a Rect, like new_rect(1, 2).

  ```python
  def new_rect(w: float, h: float) -> Rect:
      """Build a Rect, like new_rect(1, 2)."""
      return Rect(w, h)
  ```

## Edges

| from | kind | to | weight |
|------|------|----|--------|
| `describe` report/report.py:8 | references | `Rect` shapes.py:14 | 1 |
| `shapes` shapes | contains | `Rect` shapes.py:14 | 1 |
| `Rect` shapes.py:14 | contains | `__init__` shapes.py:17 | 1 |
| `Rect` shapes.py:14 | contains | `w` shapes.py:18 | 1 |
| `Rect` shapes.py:14 | contains | `h` shapes.py:19 | 1 |
| `Rect` shapes.py:14 | contains | `area` shapes.py:21 | 1 |
| `new_rect` shapes.py:26 | instantiates | `Rect` shapes.py:14 | 1 |
| `new_rect` shapes.py:26 | references | `Rect` shapes.py:14 | 2 |
