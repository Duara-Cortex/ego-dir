// Package report describes the sample's shapes.
package report

import (
	"fmt"
	"strings"

	"example.com/sample"
)

// Describe formats a rectangle's size.
func Describe(r sample.Rect) string {
	return fmt.Sprintf("%gx%g", r.W, r.H)
}

// Label upper-cases a label.
func Label(s string) string { return strings.ToUpper(s) }
