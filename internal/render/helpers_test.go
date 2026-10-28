package render

import "github.com/eschero/ascii-browser/internal/frame"

type frameRun struct {
	text string
	x, y int
}

type frameRuns []frameRun

func (rs frameRuns) runs() []frame.TextRun {
	out := make([]frame.TextRun, len(rs))
	for i, r := range rs {
		out[i] = frame.TextRun{Text: r.text, X: r.x, Y: r.y, W: len(r.text), H: 1}
	}
	return out
}
