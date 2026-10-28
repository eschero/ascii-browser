package render

import (
	"github.com/eschero/ascii-browser/internal/ascii"
	"github.com/eschero/ascii-browser/internal/frame"
)

// Overlay writes text runs over the graphic cells. Runs are applied in
// order, so a later run (later in DOM order, hence painted on top) wins a
// contested cell. Each rune becomes exactly one byte; a run is clipped to
// its own W cells and to the grid; a space inside a run clears the cell so
// words stay readable over a textured background.
//
// With halo, the graphic cell just before and just after every run is
// cleared too, so text on a dark or textured background is separated from
// it by one blank cell. Cells holding text are never cleared.
// OverlayRun writes one run, clipped to its own cells and to the grid.
func OverlayRun(f *frame.Frame, r frame.TextRun, filler byte) {
	if r.Y < 0 || r.Y >= f.Rows || r.W <= 0 {
		return
	}
	col, end := r.X, r.X+r.W
	for _, ru := range r.Text {
		if col >= end {
			break
		}
		f.Set(col, r.Y, ascii.Rune(ru, filler))
		col++
	}
}

func Overlay(f *frame.Frame, runs []frame.TextRun, filler byte, halo bool) {
	var isText []bool
	if halo {
		isText = make([]bool, f.Cols*f.Rows)
	}
	for _, r := range runs {
		if r.Y < 0 || r.Y >= f.Rows || r.W <= 0 {
			continue
		}
		col, end := r.X, r.X+r.W
		for _, ru := range r.Text {
			if col >= end {
				break
			}
			f.Set(col, r.Y, ascii.Rune(ru, filler))
			if halo && col >= 0 && col < f.Cols {
				isText[r.Y*f.Cols+col] = true
			}
			col++
		}
	}
	if !halo {
		return
	}
	for _, r := range runs {
		if r.Y < 0 || r.Y >= f.Rows || r.W <= 0 || r.Text == "" {
			continue
		}
		for _, col := range []int{r.X - 1, r.X + r.W} {
			if col >= 0 && col < f.Cols && !isText[r.Y*f.Cols+col] {
				f.Set(col, r.Y, ' ')
			}
		}
	}
}
