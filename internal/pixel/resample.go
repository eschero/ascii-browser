// Package pixel holds the small image helpers shared by the browser and
// the renderer.
package pixel

import (
	"image"
	"image/color"
)

// Downsample scales an image to exactly w x h pixels of Rec. 709 luma,
// averaging over the source rectangle each destination pixel covers. A
// media crop rarely comes back at the size its cells want, and averaging
// keeps the detail a nearest-neighbour pick would drop.
func Downsample(src image.Image, w, h int) *image.Gray {
	dst := image.NewGray(image.Rect(0, 0, w, h))
	if w < 1 || h < 1 {
		return dst
	}
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw < 1 || sh < 1 {
		return dst
	}
	luma := lumaFunc(src)
	for y := 0; y < h; y++ {
		y0 := b.Min.Y + y*sh/h
		y1 := b.Min.Y + (y+1)*sh/h
		if y1 <= y0 {
			y1 = y0 + 1
		}
		row := dst.Pix[y*dst.Stride : y*dst.Stride+w]
		for x := 0; x < w; x++ {
			x0 := b.Min.X + x*sw/w
			x1 := b.Min.X + (x+1)*sw/w
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var sum, n uint32
			for sy := y0; sy < y1 && sy < b.Max.Y; sy++ {
				for sx := x0; sx < x1 && sx < b.Max.X; sx++ {
					sum += luma(sx, sy)
					n++
				}
			}
			if n > 0 {
				row[x] = uint8(sum / n)
			}
		}
	}
	return dst
}

// lumaFunc returns the fastest per-pixel luma reader for an image.
func lumaFunc(src image.Image) func(x, y int) uint32 {
	mix := func(r, g, b uint32) uint32 { return (2126*r + 7152*g + 722*b + 5000) / 10000 }
	switch im := src.(type) {
	case *image.Gray:
		return func(x, y int) uint32 { return uint32(im.Pix[im.PixOffset(x, y)]) }
	case *image.RGBA:
		return func(x, y int) uint32 {
			p := im.Pix[im.PixOffset(x, y):]
			return mix(uint32(p[0]), uint32(p[1]), uint32(p[2]))
		}
	case *image.NRGBA:
		return func(x, y int) uint32 {
			p := im.Pix[im.PixOffset(x, y):]
			return mix(uint32(p[0]), uint32(p[1]), uint32(p[2]))
		}
	case *image.YCbCr:
		return func(x, y int) uint32 {
			ci := im.COffset(x, y)
			r, g, b := color.YCbCrToRGB(im.Y[im.YOffset(x, y)], im.Cb[ci], im.Cr[ci])
			return mix(uint32(r), uint32(g), uint32(b))
		}
	}
	return func(x, y int) uint32 {
		r, g, b, _ := src.At(x, y).RGBA()
		return mix(r>>8, g>>8, b>>8)
	}
}
