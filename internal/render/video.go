package render

import (
	"image"

	"github.com/eschero/ascii-browser/internal/frame"
	"github.com/eschero/ascii-browser/internal/grid"
)

// bayer8 is the ordered dither matrix, normalized to [0,1). Ordered
// dithering is a pure function of the position, so a still part of a
// moving picture keeps the same characters from one frame to the next;
// error diffusion would pick different ones and shimmer.
var bayer8 = func() [64]float32 {
	var m [64]float32
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			v := 0
			for i := 0; i < 3; i++ {
				bx := (x >> (2 - i)) & 1
				by := (y >> (2 - i)) & 1
				v = v<<2 | (by^bx)<<1 | bx
			}
			m[y*8+x] = float32(v) / 64
		}
	}
	return m
}()

// RenderVideo turns one video crop into cells with ordered dithering. It
// is the fallback for a video whose pixels the page cannot read: the
// picture comes from a screenshot instead, but it is dithered the same
// way, so the two paths look alike.
func (r *Renderer) RenderVideo(img image.Image, cols, rows int) *frame.Frame {
	g := grid.New(cols, rows)
	f := frame.New(cols, rows)
	if cols < 1 || rows < 1 {
		return f
	}
	r.luma = CellLuma(img, g, r.luma)
	ramp := r.opts.Ramp
	if len(ramp) < 2 {
		ramp = DefaultRamp
	}
	top := float32(len(ramp) - 1)
	for row := 0; row < rows; row++ {
		line := f.Row(row)
		for col := 0; col < cols; col++ {
			l := r.luma[row*cols+col]
			if r.opts.Paper > 0 {
				if (!r.opts.Invert && l > float32(r.opts.Paper)) ||
					(r.opts.Invert && l < 1-float32(r.opts.Paper)) {
					line[col] = ramp[0]
					continue
				}
			}
			v := l
			if !r.opts.Invert {
				v = 1 - l
			}
			k := int(v*top + bayer8[(row&7)*8+(col&7)])
			if k < 0 {
				k = 0
			} else if k >= len(ramp) {
				k = len(ramp) - 1
			}
			line[col] = ramp[k]
		}
	}
	return f
}
