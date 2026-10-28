package render

import (
	"sort"
	"strings"

	"github.com/eschero/ascii-browser/internal/ascii"
	"github.com/eschero/ascii-browser/internal/frame"
)

// Default smallest media box that is worth more than a solid block.
const (
	defaultMediaMinW = 4
	defaultMediaMinH = 2
)

// uiItem is one thing to paint, either a box or a run of text.
type uiItem struct {
	paint int64
	rank  int
	box   int // index into boxes, -1 for a text run
	run   int // index into runs, -1 for a box
}

// rank orders what shares a paint index: an opaque background blanks
// first, then frames and pictures, then widgets, then the text, which is
// why text is never swallowed by the box it sits in.
func kindRank(k frame.UIKind) int {
	switch k {
	case frame.UIClear:
		return 0
	case frame.UIFrame, frame.UIMedia:
		return 1
	case frame.UIRule, frame.UIMarker:
		return 3
	}
	return 2
}

// uiOrder lists boxes and text runs in the order they are painted.
func uiOrder(boxes []frame.UIBox, runs []frame.TextRun) []uiItem {
	items := make([]uiItem, 0, len(boxes)+len(runs))
	for i, b := range boxes {
		items = append(items, uiItem{paint: b.Paint, rank: kindRank(b.Kind), box: i, run: -1})
	}
	for i, r := range runs {
		items = append(items, uiItem{paint: r.Paint, rank: 4, box: -1, run: i})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].paint != items[j].paint {
			return items[i].paint < items[j].paint
		}
		return items[i].rank < items[j].rank
	})
	return items
}

// clearBox blanks every cell of a box, text included. This is what an
// opaque background does: whatever the page painted under a dropdown, a
// modal or a cookie banner is not on screen any more.
func clearBox(f *frame.Frame, b frame.UIBox) {
	for row := b.Y; row < b.Bottom(); row++ {
		for col := b.X; col < b.Right(); col++ {
			f.Set(col, row, ' ')
		}
	}
}

// DrawUI paints the interface layer: container frames, form widgets and
// media placeholders. runs is where the text will land: a frame edge that
// would fall on the same row or column as text is pushed out by one cell,
// so the text ends up inside the frame instead of interleaved with its
// line. It does not place the text itself; the renderer does, in the same
// paint order.
func DrawUI(f *frame.Frame, boxes []frame.UIBox, runs []frame.TextRun, o Options) {
	filler := o.Filler
	if filler == 0 {
		filler = '?'
	}
	minW, minH := o.MediaMinW, o.MediaMinH
	if minW <= 0 {
		minW = defaultMediaMinW
	}
	if minH <= 0 {
		minH = defaultMediaMinH
	}
	mask := textMask(f, runs)
	for _, it := range uiOrder(boxes, nil) {
		b := boxes[it.box]
		switch b.Kind {
		case frame.UIClear:
			clearBox(f, b)
		case frame.UIFrame:
			if coincidesWithWidget(b, boxes) {
				continue // the widget already draws this box
			}
			drawFrame(f, avoidText(b, mask, f.Cols, f.Rows), filler)
		case frame.UIMedia:
			drawMedia(f, avoidText(b, mask, f.Cols, f.Rows), filler, minW, minH)
		default:
			drawWidget(f, b, filler)
		}
	}
}

// drawWidget paints one form control.
func drawWidget(f *frame.Frame, b frame.UIBox, filler byte) {
	switch b.Kind {
	case frame.UIField:
		drawField(f, b)
	case frame.UIButton:
		drawBracket(f, b, '(', ')')
	case frame.UISelect:
		drawSelect(f, b)
	case frame.UICheckbox:
		drawToggle(f, b, '[', ']', 'x')
	case frame.UIRadio:
		drawToggle(f, b, '(', ')', 'o')
	case frame.UIRule:
		drawRule(f, b)
	case frame.UIMarker:
		writeText(f, b.X, b.Y, b.Text, filler)
	}
}

// join writes a line character, turning a crossing into '+'.
func join(f *frame.Frame, col, row int, c byte) {
	switch old := f.At(col, row); {
	case old == '-' && c == '|', old == '|' && c == '-', old == '+':
		f.Set(col, row, '+')
	default:
		f.Set(col, row, c)
	}
}

// textMask marks the cells the text layer will occupy.
func textMask(f *frame.Frame, runs []frame.TextRun) []bool {
	m := make([]bool, f.Cols*f.Rows)
	for _, r := range runs {
		if r.Y < 0 || r.Y >= f.Rows {
			continue
		}
		n := len([]rune(r.Text))
		if n > r.W {
			n = r.W
		}
		for k := 0; k < n; k++ {
			if col := r.X + k; col >= 0 && col < f.Cols {
				m[r.Y*f.Cols+col] = true
			}
		}
	}
	return m
}

// avoidText grows a box by one cell on each side whose edge would land on
// text, so the frame encloses the text instead of running through it.
func avoidText(b frame.UIBox, mask []bool, cols, rows int) frame.UIBox {
	rowBusy := func(row, x0, x1 int) bool {
		if row < 0 || row >= rows {
			return false
		}
		for col := x0; col < x1; col++ {
			if col >= 0 && col < cols && mask[row*cols+col] {
				return true
			}
		}
		return false
	}
	colBusy := func(col, y0, y1 int) bool {
		if col < 0 || col >= cols {
			return false
		}
		for row := y0; row < y1; row++ {
			if row >= 0 && row < rows && mask[row*cols+col] {
				return true
			}
		}
		return false
	}
	if rowBusy(b.Y, b.X, b.Right()) && b.Y > 0 && !rowBusy(b.Y-1, b.X, b.Right()) {
		b.Y--
		b.H++
	}
	if rowBusy(b.Bottom()-1, b.X, b.Right()) && b.Bottom() < rows && !rowBusy(b.Bottom(), b.X, b.Right()) {
		b.H++
	}
	if colBusy(b.X, b.Y, b.Bottom()) && b.X > 0 && !colBusy(b.X-1, b.Y, b.Bottom()) {
		b.X--
		b.W++
	}
	if colBusy(b.Right()-1, b.Y, b.Bottom()) && b.Right() < cols && !colBusy(b.Right(), b.Y, b.Bottom()) {
		b.W++
	}
	return b
}

// DrawMediaBorder outlines a media box that already holds its crop.
func DrawMediaBorder(f *frame.Frame, b frame.UIBox) { drawFrame(f, b, 0) }

func drawFrame(f *frame.Frame, b frame.UIBox, _ byte) {
	if b.W < 3 || b.H < 2 {
		return
	}
	l, t, r, bot := b.X, b.Y, b.Right()-1, b.Bottom()-1
	for col := l; col <= r; col++ {
		join(f, col, t, '-')
		join(f, col, bot, '-')
	}
	for row := t; row <= bot; row++ {
		join(f, l, row, '|')
		join(f, r, row, '|')
	}
	for _, c := range [][2]int{{l, t}, {r, t}, {l, bot}, {r, bot}} {
		f.Set(c[0], c[1], '+')
	}
}

func drawMedia(f *frame.Frame, b frame.UIBox, filler byte, minW, minH int) {
	if b.W < minW || b.H < minH {
		for row := b.Y; row < b.Bottom(); row++ {
			for col := b.X; col < b.Right(); col++ {
				f.Set(col, row, '#')
			}
		}
		return
	}
	drawFrame(f, frame.UIBox{X: b.X, Y: b.Y, W: b.W, H: b.H}, filler)
	label := "[ " + strings.TrimSpace(b.Text) + " ]"
	if b.Text == "" {
		label = "[ IMG ]"
	}
	if max := b.W - 2; len(label) > max {
		label = label[:max-2] + " ]"
	}
	col := b.X + (b.W-len(label))/2
	row := b.Y + b.H/2
	writeText(f, col, row, label, filler)
}

// coincidesWithWidget reports whether a frame says the same thing as a
// widget drawn in the same place, within one cell per side.
func coincidesWithWidget(b frame.UIBox, all []frame.UIBox) bool {
	for _, o := range all {
		switch o.Kind {
		case frame.UIFrame, frame.UIRule, frame.UIMarker, frame.UIClear:
			continue
		}
		if abs(o.X-b.X) <= 1 && abs(o.Y-b.Y) <= 1 && abs(o.Right()-b.Right()) <= 1 && abs(o.Bottom()-b.Bottom()) <= 1 {
			return true
		}
	}
	return false
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func drawField(f *frame.Frame, b frame.UIBox) {
	if b.W < 2 {
		return
	}
	row := b.Y + (b.H-1)/2
	f.Set(b.X, row, '[')
	f.Set(b.Right()-1, row, ']')
	for col := b.X + 1; col < b.Right()-1; col++ {
		f.Set(col, row, '_')
	}
}

func drawBracket(f *frame.Frame, b frame.UIBox, open, close byte) {
	if b.W < 2 {
		return
	}
	row := b.Y + (b.H-1)/2
	f.Set(b.X, row, open)
	f.Set(b.Right()-1, row, close)
}

func drawSelect(f *frame.Frame, b frame.UIBox) {
	if b.W < 3 {
		return
	}
	row := b.Y + (b.H-1)/2
	f.Set(b.X, row, '[')
	f.Set(b.Right()-2, row, 'v')
	f.Set(b.Right()-1, row, ']')
}

func drawToggle(f *frame.Frame, b frame.UIBox, open, close, mark byte) {
	row := b.Y + (b.H-1)/2
	f.Set(b.X, row, open)
	inner := byte(' ')
	if b.On {
		inner = mark
	}
	f.Set(b.X+1, row, inner)
	f.Set(b.X+2, row, close)
}

func drawRule(f *frame.Frame, b frame.UIBox) {
	c := byte('=')
	if b.Level > 1 {
		c = '-'
	}
	for col := b.X; col < b.Right(); col++ {
		if f.At(col, b.Y) == ' ' {
			f.Set(col, b.Y, c)
		}
	}
}

func writeText(f *frame.Frame, col, row int, s string, filler byte) {
	for _, r := range s {
		f.Set(col, row, ascii.Rune(r, filler))
		col++
	}
}
