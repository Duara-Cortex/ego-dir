# Ego: `report`

- kind: module
- location: report.report
- depth: 2

## Signature

```python
module report.report
```

## Doc

Format shapes for people.

## Body

_none_

## Upstream (callers)

_none_

## Downstream (callees)

### Hop 1

- `string` (module) string
- `describe` (function) report/report.py:8-10
  - signature: `def describe(r: Rect) -> str`
  - doc: Format a rectangle's size.
- `label` (function) report/report.py:13-15
  - signature: `def label(s: str) -> str`
  - doc: Capitalise each word of a label.
- `shapes` (module) shapes
  - signature: `module shapes`
  - doc: Shapes and their areas.

### Hop 2

- `abc` (module) abc
- `Shape` (class) shapes.py:6-11
  - signature: `class Shape()`
  - doc: Shape is anything with an area.
- `Rect` (class) shapes.py:14-23
  - signature: `class Rect(w: float, h: float)`
  - doc: Rect is an axis-aligned rectangle.
- `new_rect` (function) shapes.py:26-28
  - signature: `def new_rect(w: float, h: float) -> Rect`
  - doc: Build a Rect, like new_rect(1, 2).
- `total_area` (function) shapes.py:31-36
  - signature: `def total_area(shapes: list[Shape]) -> float`
  - doc: Sum the area of every shape.
- `report` (function) shapes.py:39-41
  - signature: `def report() -> float`
  - doc: Return the total area of two rectangles.

## Implementations

_none_

## Members

- `describe` (function) report/report.py:8-10
  - signature: `def describe(r: Rect) -> str`
  - doc: Format a rectangle's size.
- `label` (function) report/report.py:13-15
  - signature: `def label(s: str) -> str`
  - doc: Capitalise each word of a label.

## Type dependencies

- `string` (module) string
- `shapes` (module) shapes
  - signature: `module shapes`
  - doc: Shapes and their areas.

## Edges

| from | kind | to | weight |
|------|------|----|--------|
| `report` report.report | imports | `string` string | 1 |
| `report` report.report | contains | `describe` report/report.py:8 | 1 |
| `report` report.report | contains | `label` report/report.py:13 | 1 |
| `report` report.report | imports | `shapes` shapes | 1 |
| `shapes` shapes | imports | `abc` abc | 1 |
| `shapes` shapes | contains | `Shape` shapes.py:6 | 1 |
| `shapes` shapes | contains | `Rect` shapes.py:14 | 1 |
| `shapes` shapes | contains | `new_rect` shapes.py:26 | 1 |
| `shapes` shapes | contains | `total_area` shapes.py:31 | 1 |
| `shapes` shapes | contains | `report` shapes.py:39 | 1 |
