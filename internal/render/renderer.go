// Package render turns a page screenshot into an ASCII frame: mean luma per
// cell, optional Floyd-Steinberg dithering on the cell grid, a character
// ramp, and the text layer on top.
package render

import (
	"image"

	"github.com/eschero/ascii-browser/internal/ascii"
	"github.com/eschero/ascii-browser/internal/frame"
	"github.com/eschero/ascii-browser/internal/grid"
	"github.com/eschero/ascii-browser/internal/pixel"
)

// Options configure a Renderer.
type Options struct {
	Ramp   Ramp // light to dark; DefaultRamp when nil
	Dither bool // Floyd-Steinberg on the cell grid
	// Invert flips the meaning of the ramp. By default a dark pixel gives a
	// dense character ("ink on paper"), so an ordinary white page renders
	// mostly blank. With Invert a bright pixel gives a dense character
	// ("phosphor"), the natural look on a black terminal.
	Invert bool
	// Paper is a luminance threshold in (0,1]. Cells brighter than it (or,
	// with Invert, darker than 1-Paper) are the page background: they become
	// the lightest ramp character, normally a space, and are left out of the
	// error diffusion, so a near-white page stays blank instead of turning
	// into a field of dots. 0 disables the rule.
	Paper  float64
	Filler byte // for runes with no ASCII transliteration; '?' when zero
	Halo   bool // blank cell before and after each text run
	// Structure draws cells whose oriented gradient strength exceeds
	// EdgeThreshold as / \ | - _ by direction instead of a tonal
	// character. Edge cells, like paper cells, take no part in the error
	// diffusion. Text still wins over edges.
	Structure     bool
	EdgeThreshold float64
	// MediaMinW and MediaMinH are the smallest media box, in cells, that
	// is drawn as a placeholder or a crop rather than a solid block.
	MediaMinW, MediaMinH int
}

// Renderer holds reusable buffers; one per goroutine.
type Renderer struct {
	opts    Options
	luma    []float32
	paper   []bool
	idx     []uint8
	edges   []Edge
	isEdge  []bool
	scratch []uint8
}

// New returns a renderer, filling in defaults.
func New(opts Options) *Renderer {
	if opts.Ramp == nil {
		opts.Ramp = DefaultRamp
	}
	if opts.Filler == 0 {
		opts.Filler = ascii.DefaultFiller
	}
	return &Renderer{opts: opts}
}

// RenderUI composes the interface layer: container frames, form widgets
// and media placeholders on a blank page, with the text on top. Nothing
// is sampled from pixels, so the ramp, the dithering and the paper
// threshold do not apply; the halo is off because the page is already
// blank and it would eat the frame lines around a label.
func (r *Renderer) RenderUI(boxes []frame.UIBox, g grid.Grid, runs []frame.TextRun) *frame.Frame {
	f := frame.New(g.Cols, g.Rows)
	mask := textMask(f, runs)
	filler := r.opts.Filler
	if filler == 0 {
		filler = ascii.DefaultFiller
	}
	minW, minH := r.mediaMin()
	for _, it := range uiOrder(boxes, runs) {
		if it.run >= 0 {
			OverlayRun(f, runs[it.run], filler)
			continue
		}
		b := boxes[it.box]
		switch b.Kind {
		case frame.UIClear:
			clearBox(f, b)
		case frame.UIMedia:
			if b.Img != nil {
				r.blitMedia(f, b)
				continue
			}
			drawMedia(f, avoidText(b, mask, f.Cols, f.Rows), filler, minW, minH)
		case frame.UIFrame:
			if coincidesWithWidget(b, boxes) {
				continue // the widget already draws this box
			}
			drawFrame(f, avoidText(b, mask, f.Cols, f.Rows), filler)
		default:
			drawWidget(f, b, filler)
		}
	}
	return f
}

func (r *Renderer) mediaMin() (int, int) {
	minW, minH := r.opts.MediaMinW, r.opts.MediaMinH
	if minW <= 0 {
		minW = defaultMediaMinW
	}
	if minH <= 0 {
		minH = defaultMediaMinH
	}
	return minW, minH
}

// blitMedia renders a media element's own crop over its box, through the
// same tonal and structural pipeline as a full page, so the ramp, the
// paper threshold, the dithering and --structure all apply within the
// picture. The picture covers the cells the element itself covers, so it
// matches what the tonal renderer draws there, and the frame is laid over
// its outer ring afterwards.
func (r *Renderer) blitMedia(f *frame.Frame, b frame.UIBox) {
	minW, minH := r.mediaMin()
	if b.W < minW || b.H < minH {
		return
	}
	bg := grid.New(b.W, b.H)
	w, h := bg.PixelSize()
	block := r.Render(pixel.Downsample(b.Img, w, h), bg, nil)
	for y := 0; y < b.H; y++ {
		for x := 0; x < b.W; x++ {
			f.Set(b.X+x, b.Y+y, block.At(x, y))
		}
	}
	DrawMediaBorder(f, b)
}

// Render composes graphics and text into a frame of g.Cols x g.Rows cells.
// img is expected to be the viewport screenshot for g; runs may be nil.
// SetInvert switches between ink (dense characters for dark pixels) and
// phosphor (dense for bright ones) from one frame to the next, so a page
// with a dark theme can be drawn with its background blank.
func (r *Renderer) SetInvert(on bool) { r.opts.Invert = on }

// Inverted reports the current mapping.
func (r *Renderer) Inverted() bool { return r.opts.Invert }

func (r *Renderer) Render(img image.Image, g grid.Grid, runs []frame.TextRun) *frame.Frame {
	r.luma = CellLuma(img, g, r.luma)
	var fixed []bool
	if r.opts.Paper > 0 {
		fixed = r.paperMask(r.luma)
	}
	var isEdge []bool
	if r.opts.Structure {
		r.edges, r.scratch = CellEdges(img, g, r.edges, r.scratch)
		r.isEdge = EdgeMask(r.edges, g.Cols, g.Rows, r.opts.EdgeThreshold, r.isEdge)
		isEdge = r.isEdge
		if fixed == nil {
			fixed = r.paperMask(nil) // all false, sized to the grid
		}
		for i, e := range isEdge {
			if e {
				fixed[i] = true
			}
		}
	}
	if !r.opts.Invert {
		for i, l := range r.luma {
			r.luma[i] = 1 - l // darkness
		}
	}
	r.idx = Quantize(r.luma, g.Cols, g.Rows, len(r.opts.Ramp), r.opts.Dither, fixed, r.idx)
	f := frame.New(g.Cols, g.Rows)
	for row := 0; row < g.Rows; row++ {
		line := f.Row(row)
		base := row * g.Cols
		for col := range line {
			switch {
			case isEdge != nil && isEdge[base+col]:
				line[col] = r.edges[base+col].Glyph()
			default:
				line[col] = r.opts.Ramp[r.idx[base+col]]
			}
		}
	}
	Overlay(f, runs, r.opts.Filler, r.opts.Halo)
	return f
}

// paperMask flags background cells from the raw luma (before inversion).
// With a nil luma it returns a cleared mask sized to the last luma.
func (r *Renderer) paperMask(luma []float32) []bool {
	n := len(r.luma)
	if cap(r.paper) < n {
		r.paper = make([]bool, n)
	}
	r.paper = r.paper[:n]
	if luma == nil {
		for i := range r.paper {
			r.paper[i] = false
		}
		return r.paper
	}
	t := float32(r.opts.Paper)
	for i, l := range luma {
		if r.opts.Invert {
			r.paper[i] = l < 1-t
		} else {
			r.paper[i] = l > t
		}
	}
	return r.paper
}
