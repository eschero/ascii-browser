package render

import (
	"image"
	"image/color"
	"strings"
	"testing"

	"github.com/eschero/ascii-browser/internal/frame"
	"github.com/eschero/ascii-browser/internal/grid"
)

func TestParseRamp(t *testing.T) {
	if _, err := ParseRamp(" .:@"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "@", " .█", "a\tb"} {
		if _, err := ParseRamp(bad); err == nil {
			t.Errorf("ParseRamp(%q) accepted", bad)
		}
	}
}

// halfImage is black on the left half, white on the right.
func halfImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{255, 255, 255, 255}
			if x < w/2 {
				c = color.RGBA{0, 0, 0, 255}
			}
			img.Set(x, y, c)
		}
	}
	return img
}

func TestCellLuma(t *testing.T) {
	g := grid.New(4, 2)
	l := CellLuma(halfImage(32, 32), g, nil)
	if l[0] != 0 || l[1] != 0 || l[2] != 1 || l[3] != 1 {
		t.Fatalf("luma row 0 = %v", l[:4])
	}
	// Grid taller than the image: missing pixels read as white.
	g2 := grid.New(4, 3)
	l2 := CellLuma(halfImage(32, 32), g2, nil)
	if l2[8] != 1 {
		t.Fatalf("out-of-image cell luma = %v, want 1", l2[8])
	}
}

func TestQuantizeDitherDensity(t *testing.T) {
	cols, rows := 40, 20
	v := make([]float32, cols*rows)
	for i := range v {
		v[i] = 0.5
	}
	idx := Quantize(v, cols, rows, 2, true, nil, nil)
	ones := 0
	for _, x := range idx {
		ones += int(x)
	}
	if ratio := float64(ones) / float64(len(idx)); ratio < 0.45 || ratio > 0.55 {
		t.Fatalf("dithered 50%% gray gives %.2f density", ratio)
	}
	flat := make([]float32, 4)
	for i := range flat {
		flat[i] = 0.5
	}
	if got := Quantize(flat, 2, 2, 2, false, nil, nil); got[0] != 1 || got[3] != 1 {
		t.Fatalf("undithered 0.5 at 2 levels = %v", got)
	}
}

func TestPaper(t *testing.T) {
	// A 4x1 grid of light gray (luma 0.94): dithered at 10 levels it gives
	// dots; with --paper 0.93 it is background and must stay blank.
	img := image.NewRGBA(image.Rect(0, 0, 32, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, color.RGBA{240, 240, 240, 255})
		}
	}
	g := grid.New(4, 1)
	dots := New(Options{Dither: true}).Render(img, g, nil).String()
	if !strings.Contains(dots, ".") {
		t.Fatalf("without paper, light gray should dither to dots, got %q", dots)
	}
	blank := New(Options{Dither: true, Paper: 0.93}).Render(img, g, nil).String()
	if blank != "    \n" {
		t.Fatalf("with paper, light gray should be blank, got %q", blank)
	}
	// Threshold above the luma: back to dithering.
	if got := New(Options{Dither: true, Paper: 0.99}).Render(img, g, nil).String(); !strings.Contains(got, ".") {
		t.Fatalf("paper above the luma must not blank the cell, got %q", got)
	}
	// Fixed cells must not receive error either: dark neighbour, paper cell.
	v := []float32{0.9, 0.05}
	idx := Quantize(v, 2, 1, 2, true, []bool{false, true}, nil)
	if idx[1] != 0 {
		t.Fatalf("paper cell took error from its neighbour: %v", idx)
	}
}

func TestRenderInkAndInvert(t *testing.T) {
	g := grid.New(4, 2)
	img := halfImage(32, 32)
	f := New(Options{Dither: false}).Render(img, g, nil)
	if f.At(0, 0) != '@' || f.At(3, 0) != ' ' {
		t.Fatalf("ink: %q", f.String())
	}
	f = New(Options{Dither: false, Invert: true}).Render(img, g, nil)
	if f.At(0, 0) != ' ' || f.At(3, 0) != '@' {
		t.Fatalf("invert: %q", f.String())
	}
}

func TestOverlay(t *testing.T) {
	f := frame.New(8, 2)
	for _, line := range []int{0, 1} {
		copy(f.Row(line), strings.Repeat("#", 8))
	}
	Overlay(f, []frame.TextRun{
		{Text: "ab cdèfg", X: 1, Y: 0, W: 5, H: 1}, // clipped to W=5
		{Text: "xy", X: 6, Y: 1, W: 4, H: 1},       // clipped to the grid
		{Text: "日", X: 0, Y: 1, W: 1, H: 1},        // filler
	}, '?', false)
	if got := f.String(); got != "#ab cd##\n?#####xy\n" {
		t.Fatalf("overlay = %q", got)
	}
}

func TestOverlayHalo(t *testing.T) {
	f := frame.New(12, 1)
	copy(f.Row(0), strings.Repeat("@", 12))
	// Two runs one cell apart: the halo must not erase the neighbour's text.
	Overlay(f, []frame.TextRun{
		{Text: "ab", X: 2, Y: 0, W: 2, H: 1},
		{Text: "cd", X: 5, Y: 0, W: 2, H: 1},
	}, '?', true)
	if got := f.String(); got != "@ ab cd @@@@\n" {
		t.Fatalf("halo overlay = %q", got)
	}
	f = frame.New(4, 1)
	copy(f.Row(0), "@@@@")
	Overlay(f, []frame.TextRun{{Text: "xyzw", X: 0, Y: 0, W: 4, H: 1}}, '?', true)
	if got := f.String(); got != "xyzw\n" {
		t.Fatalf("halo at the grid edge = %q", got)
	}
}

func TestRampWithout(t *testing.T) {
	got := DefaultRamp.Without(StructureGlyphs)
	if string(got) != " .:=+*#%@" {
		t.Fatalf("standard without structure glyphs = %q", got)
	}
	if string(Presets["short"].Without(StructureGlyphs)) != string(Presets["short"]) {
		t.Fatal("short preset has no structure glyphs and must be unchanged")
	}
	if n := len(Presets["long"].Without(StructureGlyphs)); n != 65 {
		t.Fatalf("long preset without structure glyphs has %d levels, want 65", n)
	}
}
