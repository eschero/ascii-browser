package render

import (
	"image"
	"image/color"
	"testing"

	"github.com/eschero/ascii-browser/internal/grid"
)

// lineImage draws a 2px dark line through a white image along the given
// pixel-space direction through the image center.
func lineImage(w, h int, dx, dy float64) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{255, 255, 255, 255})
		}
	}
	cx, cy := float64(w)/2, float64(h)/2
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			px, py := float64(x)+0.5-cx, float64(y)+0.5-cy
			d := px*dy - py*dx
			if d*d < 1.0*(dx*dx+dy*dy) {
				img.Set(x, y, color.RGBA{0, 0, 0, 255})
			}
		}
	}
	return img
}

func TestCellEdgesGlyphs(t *testing.T) {
	g := grid.New(5, 3)
	w, h := g.PixelSize()
	cases := []struct {
		dx, dy float64
		want   byte
	}{
		{1, 0, '-'},  // horizontal through the cell middle
		{0, 1, '|'},  // vertical
		{1, -2, '/'}, // rises to the right at the glyph's own slope (cell is 1:2)
		{1, 2, '\\'}, // falls to the right
	}
	for _, c := range cases {
		edges, _ := CellEdges(lineImage(w, h, c.dx, c.dy), g, nil, nil)
		center := edges[1*g.Cols+2]
		if center.Strength < 0.2 {
			t.Errorf("line (%v,%v): center strength %.3f too weak", c.dx, c.dy, center.Strength)
		}
		if got := center.Glyph(); got != c.want {
			t.Errorf("line (%v,%v): glyph %q (angle %.1f), want %q", c.dx, c.dy, got, center.Angle, c.want)
		}
		// Diagonals through the center pass close to the corners; only the
		// axis-aligned lines leave the top-left cell untouched.
		if corner := edges[0]; (c.dx == 0 || c.dy == 0) && corner.Strength > 0.05 {
			t.Errorf("line (%v,%v): blank corner strength %.3f", c.dx, c.dy, corner.Strength)
		}
	}
	// A horizontal line in the lower part of a cell reads as '_'.
	img := lineImage(w, h, 1, 0)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{255, 255, 255, 255})
		}
	}
	for x := 0; x < w; x++ {
		img.Set(x, 16+14, color.RGBA{0, 0, 0, 255}) // row 1, near its bottom
	}
	edges, _ := CellEdges(img, g, nil, nil)
	if got := edges[1*g.Cols+2].Glyph(); got != '_' {
		t.Errorf("low horizontal line glyph %q, want '_'", got)
	}
}

func TestRenderStructure(t *testing.T) {
	g := grid.New(5, 3)
	w, h := g.PixelSize()
	img := lineImage(w, h, 0, 1) // vertical dark line down the middle column
	plain := New(Options{Dither: true, Paper: 0.93}).Render(img, g, nil)
	structured := New(Options{Dither: true, Paper: 0.93, Structure: true, EdgeThreshold: 0.18}).Render(img, g, nil)
	if structured.At(2, 1) != '|' {
		t.Fatalf("structured center = %q, want '|' (plain was %q)\n%s", structured.At(2, 1), plain.At(2, 1), structured.String())
	}
	if structured.At(0, 0) != ' ' {
		t.Fatalf("structured corner = %q, want blank", structured.At(0, 0))
	}
	withText := New(Options{Structure: true, EdgeThreshold: 0.18}).Render(img, g, frameRuns{{"ab", 2, 1}}.runs())
	if withText.At(2, 1) != 'a' {
		t.Fatalf("text over edge = %q, want 'a'", withText.At(2, 1))
	}
}

func TestPresets(t *testing.T) {
	for name, r := range Presets {
		if _, err := ParseRamp(string(r)); err != nil {
			t.Errorf("preset %s: %v", name, err)
		}
	}
	if len(Presets["long"]) != 70 {
		t.Errorf("long preset has %d characters, want 70", len(Presets["long"]))
	}
	if Presets["long"][0] != ' ' || Presets["long"][69] != '$' {
		t.Errorf("long preset must run light to dark: %q", Presets["long"])
	}
}
