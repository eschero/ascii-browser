package render

import (
	"strings"
	"testing"

	"github.com/eschero/ascii-browser/internal/frame"
)

func drawn(boxes []frame.UIBox, runs []frame.TextRun, cols, rows int) string {
	f := frame.New(cols, rows)
	DrawUI(f, boxes, runs, Options{Filler: '?'})
	Overlay(f, runs, '?', false)
	return f.String()
}

func TestDrawFrameAndJunctions(t *testing.T) {
	// Two frames sharing an edge: the crossing becomes '+'.
	got := drawn([]frame.UIBox{
		{Kind: frame.UIFrame, X: 0, Y: 0, W: 6, H: 3},
		{Kind: frame.UIFrame, X: 5, Y: 0, W: 5, H: 3},
	}, nil, 10, 3)
	want := "+----+---+\n|    |   |\n+----+---+\n"
	if got != want {
		t.Fatalf("frames =\n%swant\n%s", got, want)
	}
}

func TestFrameAvoidsTextRow(t *testing.T) {
	// The frame's top edge would land on the text row, so it moves up.
	runs := []frame.TextRun{{Text: "Label", X: 1, Y: 1, W: 5, H: 1}}
	got := drawn([]frame.UIBox{{Kind: frame.UIFrame, X: 0, Y: 1, W: 8, H: 2}}, runs, 8, 4)
	want := "+------+\n|Label |\n+------+\n        \n"
	if got != want {
		t.Fatalf("frame around text =\n%swant\n%s", got, want)
	}
}

func TestWidgets(t *testing.T) {
	boxes := []frame.UIBox{
		{Kind: frame.UIField, X: 0, Y: 0, W: 8, H: 1},
		{Kind: frame.UIButton, X: 9, Y: 0, W: 8, H: 1},
		{Kind: frame.UICheckbox, X: 0, Y: 1, W: 3, H: 1, On: true},
		{Kind: frame.UIRadio, X: 4, Y: 1, W: 3, H: 1},
		{Kind: frame.UISelect, X: 8, Y: 1, W: 9, H: 1},
	}
	runs := []frame.TextRun{
		{Text: "abc", X: 1, Y: 0, W: 3, H: 1},
		{Text: "Go", X: 12, Y: 0, W: 2, H: 1},
		{Text: "one", X: 9, Y: 1, W: 3, H: 1},
	}
	got := drawn(boxes, runs, 17, 2)
	want := "[abc___] (  Go  )\n[x] ( ) [one   v]\n"
	if got != want {
		t.Fatalf("widgets =\n%swant\n%s", got, want)
	}
}

func TestMediaPlaceholderAndSolidBlock(t *testing.T) {
	got := drawn([]frame.UIBox{{Kind: frame.UIMedia, X: 0, Y: 0, W: 20, H: 5, Text: "IMG a cat"}}, nil, 20, 5)
	if !strings.Contains(got, "[ IMG a cat ]") {
		t.Fatalf("media placeholder =\n%s", got)
	}
	if !strings.HasPrefix(got, "+------------------+") {
		t.Fatalf("media placeholder lacks its frame:\n%s", got)
	}
	// Under the threshold it is a solid block.
	small := drawn([]frame.UIBox{{Kind: frame.UIMedia, X: 0, Y: 0, W: 3, H: 3, Text: "IMG"}}, nil, 3, 3)
	if small != "###\n###\n###\n" {
		t.Fatalf("small media = %q", small)
	}
}

func TestFrameCollapsesOntoWidget(t *testing.T) {
	boxes := []frame.UIBox{
		{Kind: frame.UIFrame, X: 0, Y: 0, W: 8, H: 1},
		{Kind: frame.UIField, X: 0, Y: 0, W: 8, H: 1},
	}
	got := drawn(boxes, nil, 8, 1)
	if got != "[______]\n" {
		t.Fatalf("frame over a widget = %q", got)
	}
}

func TestHeadingRule(t *testing.T) {
	runs := []frame.TextRun{{Text: "Title", X: 0, Y: 0, W: 5, H: 1}}
	got := drawn([]frame.UIBox{{Kind: frame.UIRule, X: 0, Y: 1, W: 5, H: 1, Level: 1}}, runs, 5, 2)
	if got != "Title\n=====\n" {
		t.Fatalf("heading rule = %q", got)
	}
	got = drawn([]frame.UIBox{{Kind: frame.UIRule, X: 0, Y: 1, W: 5, H: 1, Level: 2}}, runs, 5, 2)
	if got != "Title\n-----\n" {
		t.Fatalf("level 2 rule = %q", got)
	}
}
