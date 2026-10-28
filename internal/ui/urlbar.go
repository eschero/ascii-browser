package ui

import (
	"github.com/gdamore/tcell/v2"
)

// BarAction is what a key did to the URL bar.
type BarAction int

const (
	BarNone   BarAction = iota // text or cursor changed, or nothing
	BarSubmit                  // Enter
	BarCancel                  // Escape
)

// URLBar is a one-line ASCII editor, in the spirit of Browsh's input box.
type URLBar struct {
	buf []rune
	cur int
}

// Set replaces the text and puts the cursor at its end.
func (u *URLBar) Set(s string) {
	u.buf = []rune(s)
	u.cur = len(u.buf)
}

// String returns the text.
func (u *URLBar) String() string { return string(u.buf) }

// View returns the part of the text that fits in cols with the cursor
// visible, and the cursor column within it.
func (u *URLBar) View(cols int) (text string, cursor int) {
	if cols <= 0 {
		return "", -1
	}
	start := 0
	if u.cur >= cols {
		start = u.cur - cols + 1
	}
	end := start + cols
	if end > len(u.buf) {
		end = len(u.buf)
	}
	return string(u.buf[start:end]), u.cur - start
}

// Handle applies a key event.
func (u *URLBar) Handle(ev *tcell.EventKey) BarAction {
	switch ev.Key() {
	case tcell.KeyEnter:
		return BarSubmit
	case tcell.KeyEscape:
		return BarCancel
	case tcell.KeyLeft:
		if u.cur > 0 {
			u.cur--
		}
	case tcell.KeyRight:
		if u.cur < len(u.buf) {
			u.cur++
		}
	case tcell.KeyHome, tcell.KeyCtrlA:
		u.cur = 0
	case tcell.KeyEnd, tcell.KeyCtrlE:
		u.cur = len(u.buf)
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if u.cur > 0 {
			u.buf = append(u.buf[:u.cur-1], u.buf[u.cur:]...)
			u.cur--
		}
	case tcell.KeyDelete:
		if u.cur < len(u.buf) {
			u.buf = append(u.buf[:u.cur], u.buf[u.cur+1:]...)
		}
	case tcell.KeyCtrlU:
		u.buf = u.buf[:0]
		u.cur = 0
	case tcell.KeyCtrlK:
		u.buf = u.buf[:u.cur]
	case tcell.KeyRune:
		r := ev.Rune()
		if r < 0x20 || r > 0x7E {
			return BarNone
		}
		u.buf = append(u.buf, 0)
		copy(u.buf[u.cur+1:], u.buf[u.cur:])
		u.buf[u.cur] = r
		u.cur++
	}
	return BarNone
}
