// Package ui draws frames on the terminal with tcell. Only runes are
// written: no color, no bold. Reverse video is the single attribute in
// use, for the URL bar cursor and the current link.
package ui

import (
	"github.com/gdamore/tcell/v2"

	"github.com/eschero/ascii-browser/internal/ascii"
	"github.com/eschero/ascii-browser/internal/frame"
)

// Screen wraps a tcell screen.
type Screen struct {
	s      tcell.Screen
	events chan tcell.Event
	quit   chan struct{}
}

// Open initialises the terminal, with mouse reporting on.
func Open() (*Screen, error) {
	s, err := tcell.NewScreen()
	if err != nil {
		return nil, err
	}
	if err := s.Init(); err != nil {
		return nil, err
	}
	s.SetStyle(tcell.StyleDefault)
	s.EnableMouse(tcell.MouseButtonEvents)
	s.Clear()
	sc := &Screen{s: s, events: make(chan tcell.Event, 32), quit: make(chan struct{})}
	go s.ChannelEvents(sc.events, sc.quit)
	return sc, nil
}

// Close restores the terminal.
func (sc *Screen) Close() {
	close(sc.quit)
	sc.s.Fini()
}

// Size is the terminal size in cells.
func (sc *Screen) Size() (cols, rows int) { return sc.s.Size() }

// Events delivers terminal events.
func (sc *Screen) Events() <-chan tcell.Event { return sc.events }

var (
	plain   = tcell.StyleDefault
	reverse = tcell.StyleDefault.Reverse(true)
)

// DrawFrame shows a window of a frame: the frame cell (ox, oy) lands on
// terminal cell (0, top), and the terminal area of w x h cells is filled,
// with spaces where the frame ends. Cells flagged in hl (nil for none,
// else Cols*Rows entries) are drawn in reverse video.
func (sc *Screen) DrawFrame(f *frame.Frame, top int, hl []bool, ox, oy, w, h int) {
	for y := 0; y < h; y++ {
		row := oy + y
		for x := 0; x < w; x++ {
			col := ox + x
			c, st := byte(' '), plain
			if col >= 0 && row >= 0 && col < f.Cols && row < f.Rows {
				c = f.At(col, row)
				if hl != nil && hl[row*f.Cols+col] {
					st = reverse
				}
			}
			sc.s.SetContent(x, y+top, rune(c), nil, st)
		}
	}
}

// SetCell writes one terminal cell, in reverse video when hl is set.
func (sc *Screen) SetCell(x, y int, c byte, hl bool) {
	st := plain
	if hl {
		st = reverse
	}
	sc.s.SetContent(x, y, rune(c), nil, st)
}

// DrawBar writes one line of text on a terminal row, transliterated to
// ASCII and padded to cols. cursor is the column drawn in reverse video,
// or -1 for none.
func (sc *Screen) DrawBar(row int, text string, cols int, cursor int) {
	b := ascii.Bytes(text, ascii.DefaultFiller)
	for col := 0; col < cols; col++ {
		c := byte(' ')
		if col < len(b) {
			c = b[col]
		}
		st := plain
		if col == cursor {
			st = reverse
		}
		sc.s.SetContent(col, row, rune(c), nil, st)
	}
}

// ShowCursorAt puts the terminal's own cursor on a cell, so it blinks
// where the reader is about to type.
func (sc *Screen) ShowCursorAt(col, row int) { sc.s.ShowCursor(col, row) }

// HideCursor takes the terminal cursor off the screen.
func (sc *Screen) HideCursor() { sc.s.HideCursor() }

// Show flushes pending changes to the terminal.
func (sc *Screen) Show() { sc.s.Show() }

// Sync redraws everything, after a resize.
func (sc *Screen) Sync() { sc.s.Sync() }
