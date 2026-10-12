# Ego: `TotalArea`

- kind: function
- location: shapes.go:18-24
- depth: 2

## Signature

```go
func TotalArea(shapes []Shape) float64
```

## Doc

TotalArea sums the area of every shape.

## Body

```go
func TotalArea(shapes []Shape) float64 {
	var total float64
	for _, s := range shapes {
		total += s.Area()
	}
	return total
}
```

## Upstream (callers)

### Hop 1

- `Report` (function) shapes.go:27-29
  - signature: `func Report() float64`
  - doc: Report returns the total area of two rectangles.

## Downstream (callees)

### Hop 1

- `Area` (method) shapes.go:5
  - signature: `func (Shape) Area() float64`

### Hop 2

- `Area` (method) shapes.go:12
  - signature: `func (r Rect) Area() float64`
  - doc: Area returns the rectangle's area.

## Implementations

_none_

## Type dependencies

_none_

## Edges

| from | kind | to | weight |
|------|------|----|--------|
| `Area` shapes.go:12 | implements | `Area` shapes.go:5 | 1 |
| `TotalArea` shapes.go:18 | calls | `Area` shapes.go:5 | 1 |
| `Report` shapes.go:27 | calls | `TotalArea` shapes.go:18 | 1 |
