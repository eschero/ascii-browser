// Package grid defines the cell geometry shared by the terminal, the
// Chromium viewport and the renderer: one terminal cell covers exactly
// CellW x CellH pixels of the page.
package grid

import "math"

// Default cell size in pixels. The 1:2 ratio matches the aspect of a
// terminal character cell, so the page is not squashed.
const (
	DefaultCellW = 8
	DefaultCellH = 16
)

// Grid is a Cols x Rows matrix of cells covering the page viewport.
// The URL bar is not part of the grid.
type Grid struct {
	Cols, Rows   int
	CellW, CellH int
}

// New returns a grid with the default cell size.
func New(cols, rows int) Grid {
	return Grid{Cols: cols, Rows: rows, CellW: DefaultCellW, CellH: DefaultCellH}
}

// PixelSize is the Chromium viewport size that maps 1:1 onto the grid.
func (g Grid) PixelSize() (w, h int) { return g.Cols * g.CellW, g.Rows * g.CellH }

// Cells is the number of cells in the grid.
func (g Grid) Cells() int { return g.Cols * g.Rows }

// Center returns the pixel at the center of a cell, used for mouse events.
func (g Grid) Center(col, row int) (x, y float64) {
	return float64(col*g.CellW) + float64(g.CellW)/2, float64(row*g.CellH) + float64(g.CellH)/2
}

// Cell returns the cell containing a pixel position.
func (g Grid) Cell(x, y float64) (col, row int) {
	return int(math.Floor(x / float64(g.CellW))), int(math.Floor(y / float64(g.CellH)))
}

// InBounds reports whether the cell is inside the grid.
func (g Grid) InBounds(col, row int) bool {
	return col >= 0 && row >= 0 && col < g.Cols && row < g.Rows
}
