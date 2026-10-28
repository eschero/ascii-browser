// Package frame holds the composed ASCII frame: a Cols x Rows matrix of
// printable ASCII bytes, plus the text runs that get overlaid on it.
package frame

import (
	"bytes"
	"io"
	"strings"
)

// TextRun is one horizontal run of page text snapped to the cell grid, as
// produced by the text walker. Text is raw page text (Unicode); the
// renderer transliterates it one rune per cell. The run occupies cells
// X..X+W-1 on row Y; H is always 1 for a run.
type TextRun struct {
	Text   string `json:"text"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	W      int    `json:"w"`
	H      int    `json:"h"`
	Link   bool   `json:"link"`             // inside an <a href>
	Href   string `json:"href,omitempty"`   // absolute URL of that anchor
	LinkID int    `json:"lid,omitempty"`    // tells anchors and buttons apart within one capture; 0 = none
	Button bool   `json:"button,omitempty"` // inside a <button> or role=button, clickable with Enter
	Field  bool   `json:"field,omitempty"`  // a form control (value or placeholder); may have empty Text
	Paint  int64  `json:"paint,omitempty"`  // paint order, for the interface renderer
}

// Frame is the rendered page: exactly Rows lines of Cols bytes, every byte
// in 0x20..0x7E.
type Frame struct {
	Cols, Rows int
	cells      []byte
}

const (
	minPrintable = 0x20
	maxPrintable = 0x7E
	// fallback guards the invariant when a caller passes a non printable
	// byte. Callers are expected to transliterate first.
	fallback = '?'
)

// New returns a frame filled with spaces.
func New(cols, rows int) *Frame {
	if cols < 0 {
		cols = 0
	}
	if rows < 0 {
		rows = 0
	}
	f := &Frame{Cols: cols, Rows: rows, cells: make([]byte, cols*rows)}
	for i := range f.cells {
		f.cells[i] = ' '
	}
	return f
}

// At returns the byte at a cell; space outside the grid.
func (f *Frame) At(col, row int) byte {
	if col < 0 || row < 0 || col >= f.Cols || row >= f.Rows {
		return ' '
	}
	return f.cells[row*f.Cols+col]
}

// Set writes one cell. Cells outside the grid are ignored; a byte outside
// printable ASCII is replaced by '?'.
func (f *Frame) Set(col, row int, c byte) {
	if col < 0 || row < 0 || col >= f.Cols || row >= f.Rows {
		return
	}
	if c < minPrintable || c > maxPrintable {
		c = fallback
	}
	f.cells[row*f.Cols+col] = c
}

// Row returns a view of one row without copying; nil outside the grid.
// Writers must keep the printable ASCII invariant.
func (f *Frame) Row(row int) []byte {
	if row < 0 || row >= f.Rows {
		return nil
	}
	return f.cells[row*f.Cols : (row+1)*f.Cols]
}

// Clone returns a deep copy.
func (f *Frame) Clone() *Frame {
	c := &Frame{Cols: f.Cols, Rows: f.Rows, cells: make([]byte, len(f.cells))}
	copy(c.cells, f.cells)
	return c
}

// Equal reports whether two frames have the same size and content.
func (f *Frame) Equal(o *Frame) bool {
	if o == nil || f.Cols != o.Cols || f.Rows != o.Rows {
		return false
	}
	return bytes.Equal(f.cells, o.cells)
}

// WriteTo writes the dump format: Rows lines of exactly Cols bytes, each
// terminated by '\n'. Trailing spaces are preserved.
func (f *Frame) WriteTo(w io.Writer) (int64, error) {
	var n int64
	buf := make([]byte, f.Cols+1)
	buf[f.Cols] = '\n'
	for r := 0; r < f.Rows; r++ {
		copy(buf, f.Row(r))
		k, err := w.Write(buf)
		n += int64(k)
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// String is the dump format as a string.
func (f *Frame) String() string {
	var b strings.Builder
	b.Grow(f.Rows * (f.Cols + 1))
	f.WriteTo(&b)
	return b.String()
}

// Change is one cell that differs between two frames.
type Change struct {
	Col, Row int
	C        byte
}

// Diff lists the cells of f that differ from prev. A nil prev, or one of a
// different size, yields every cell.
func (f *Frame) Diff(prev *Frame) []Change {
	if f.Cols == 0 {
		return nil
	}
	full := prev == nil || prev.Cols != f.Cols || prev.Rows != f.Rows
	var out []Change
	for i, c := range f.cells {
		if full || prev.cells[i] != c {
			out = append(out, Change{Col: i % f.Cols, Row: i / f.Cols, C: c})
		}
	}
	return out
}
