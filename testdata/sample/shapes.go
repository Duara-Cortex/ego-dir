package sample

// Shape is anything with an area.
type Shape interface {
	Area() float64
}

// Rect is an axis-aligned rectangle.
type Rect struct{ W, H float64 }

// Area returns the rectangle's area.
func (r Rect) Area() float64 { return r.W * r.H }

// NewRect builds a Rect.
func NewRect(w, h float64) Rect { return Rect{W: w, H: h} }

// TotalArea sums the area of every shape.
func TotalArea(shapes []Shape) float64 {
	var total float64
	for _, s := range shapes {
		total += s.Area()
	}
	return total
}

// Report returns the total area of two rectangles.
func Report() float64 {
	return TotalArea([]Shape{NewRect(1, 2), NewRect(3, 4)})
}
