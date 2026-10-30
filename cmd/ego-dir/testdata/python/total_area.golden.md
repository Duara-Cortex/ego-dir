# Ego: `total_area`

- kind: function
- location: shapes.py:31-36
- depth: 2

## Signature

```python
def total_area(shapes: list[Shape]) -> float
```

## Doc

Sum the area of every shape.

## Body

```python
def total_area(shapes: list[Shape]) -> float:
    """Sum the area of every shape."""
    total = 0.0
    for s in shapes:
        total += s.area()
    return total
```

## Upstream (callers)

### Hop 1

- `report` (function) shapes.py:39-41
  - signature: `def report() -> float`
  - doc: Return the total area of two rectangles.

## Downstream (callees)

### Hop 1

- `area` (method) shapes.py:9-11
  - signature: `def area(self: Self@Shape) -> float`
  - doc: Return the shape's area.

## Implementations

_none_

## Members

_none_

## Type dependencies

_none_

## Edges

| from | kind | to | weight |
|------|------|----|--------|
| `total_area` shapes.py:31 | calls | `area` shapes.py:10 | 1 |
| `report` shapes.py:39 | calls | `total_area` shapes.py:31 | 1 |
