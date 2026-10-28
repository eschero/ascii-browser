package render

import (
	"image"
	"image/color"

	"github.com/eschero/ascii-browser/internal/grid"
)

// CellLuma computes the mean Rec. 709 luma of every cell, in 0..1 where 0
// is black. dst is reused when it has enough capacity. Pixels outside the
// image count as white, the usual page background, so a screenshot shorter
// than the grid does not read as a black band.
func CellLuma(img image.Image, g grid.Grid, dst []float32) []float32 {
	n := g.Cells()
	if cap(dst) < n {
		dst = make([]float32, n)
	}
	dst = dst[:n]
	b := img.Bounds()

	// sample returns 2126*R + 7152*G + 722*B with 8-bit channels: 0..2,550,000.
	var sample func(x, y int) uint32
	switch im := img.(type) {
	case *image.RGBA:
		sample = func(x, y int) uint32 {
			p := im.Pix[im.PixOffset(x, y):]
			return 2126*uint32(p[0]) + 7152*uint32(p[1]) + 722*uint32(p[2])
		}
	case *image.NRGBA:
		sample = func(x, y int) uint32 {
			p := im.Pix[im.PixOffset(x, y):]
			return 2126*uint32(p[0]) + 7152*uint32(p[1]) + 722*uint32(p[2])
		}
	case *image.YCbCr:
		// JPEG luma is BT.601; convert to RGB for Rec. 709 weights.
		sample = func(x, y int) uint32 {
			ci := im.COffset(x, y)
			r, gg, bb := color.YCbCrToRGB(im.Y[im.YOffset(x, y)], im.Cb[ci], im.Cr[ci])
			return 2126*uint32(r) + 7152*uint32(gg) + 722*uint32(bb)
		}
	case *image.Gray:
		return grayCellLuma(im, g, dst)
	default:
		sample = func(x, y int) uint32 {
			r, gg, bb, _ := img.At(x, y).RGBA()
			return (2126*r + 7152*gg + 722*bb) >> 8
		}
	}

	const white = 10000 * 255
	norm := 1 / (float32(g.CellW*g.CellH) * float32(white))
	for row := 0; row < g.Rows; row++ {
		y0 := b.Min.Y + row*g.CellH
		for col := 0; col < g.Cols; col++ {
			x0 := b.Min.X + col*g.CellW
			var sum uint64
			for y := y0; y < y0+g.CellH; y++ {
				for x := x0; x < x0+g.CellW; x++ {
					if x >= b.Max.X || y >= b.Max.Y {
						sum += white
						continue
					}
					sum += uint64(sample(x, y))
				}
			}
			dst[row*g.Cols+col] = float32(sum) * norm
		}
	}
	return dst
}

// grayCellLuma is the fast path for a stitched luma image: plain row sums.
func grayCellLuma(im *image.Gray, g grid.Grid, dst []float32) []float32 {
	b := im.Bounds()
	norm := 1 / (float32(g.CellW*g.CellH) * 255)
	for row := 0; row < g.Rows; row++ {
		y0 := b.Min.Y + row*g.CellH
		for col := 0; col < g.Cols; col++ {
			x0 := b.Min.X + col*g.CellW
			var sum uint32
			for y := y0; y < y0+g.CellH; y++ {
				if y >= b.Max.Y {
					sum += 255 * uint32(g.CellW)
					continue
				}
				off := im.PixOffset(x0, y)
				for x := x0; x < x0+g.CellW; x++ {
					if x >= b.Max.X {
						sum += 255
						continue
					}
					sum += uint32(im.Pix[off])
					off++
				}
			}
			dst[row*g.Cols+col] = float32(sum) * norm
		}
	}
	return dst
}
