package render

import (
	"image"
	"image/color"
	"math"

	"github.com/eschero/ascii-browser/internal/grid"
)

// Edge describes the dominant oriented gradient inside one cell.
type Edge struct {
	// Strength is sqrt of the anisotropic gradient energy, 0..1: a sharp
	// black-to-white step crossing the cell gives about 0.5, isotropic
	// texture and smooth shading give near 0.
	Strength float32
	// Angle is the edge direction in glyph space, degrees in [0,180): 0 is
	// horizontal, 90 vertical, 45 rises to the right like '/'. Glyph space
	// scales the cell to a square, because a cell is twice as tall as wide
	// and '/' spans it corner to corner.
	Angle float32
	// Low is true when the gradient energy sits in the lower part of the
	// cell, where a horizontal edge is drawn as '_' rather than '-'.
	Low bool
}

// Glyph is the structural character for an edge: / \ | - _ chosen from
// eight 22.5-degree sectors of the direction. Sectors nearest to 45 and
// its neighbours take '/', nearest to 135 and its neighbours '\', so
// slanted contours read as diagonals; only edges within 11.25 degrees of
// horizontal or vertical get '-', '_' or '|'.
func (e Edge) Glyph() byte {
	sector := int(math.Floor(float64(e.Angle)/22.5+0.5)) % 8
	switch sector {
	case 0:
		if e.Low {
			return '_'
		}
		return '-'
	case 1, 2, 3:
		return '/'
	case 4:
		return '|'
	default:
		return '\\'
	}
}

// lumaPlane converts an image to one byte of Rec. 709 luma per pixel.
func lumaPlane(img image.Image, dst []uint8) ([]uint8, int, int) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if cap(dst) < w*h {
		dst = make([]uint8, w*h)
	}
	dst = dst[:w*h]
	luma := func(r, g, bl uint32) uint8 { return uint8((2126*r + 7152*g + 722*bl + 5000) / 10000) }
	for y := 0; y < h; y++ {
		row := dst[y*w : (y+1)*w]
		switch im := img.(type) {
		case *image.Gray:
			copy(row, im.Pix[im.PixOffset(b.Min.X, b.Min.Y+y):im.PixOffset(b.Min.X, b.Min.Y+y)+w])
		case *image.RGBA:
			p := im.Pix[im.PixOffset(b.Min.X, b.Min.Y+y):]
			for x := 0; x < w; x++ {
				row[x] = luma(uint32(p[4*x]), uint32(p[4*x+1]), uint32(p[4*x+2]))
			}
		case *image.NRGBA:
			p := im.Pix[im.PixOffset(b.Min.X, b.Min.Y+y):]
			for x := 0; x < w; x++ {
				row[x] = luma(uint32(p[4*x]), uint32(p[4*x+1]), uint32(p[4*x+2]))
			}
		case *image.YCbCr:
			for x := 0; x < w; x++ {
				ci := im.COffset(b.Min.X+x, b.Min.Y+y)
				r, g, bl := color.YCbCrToRGB(im.Y[im.YOffset(b.Min.X+x, b.Min.Y+y)], im.Cb[ci], im.Cr[ci])
				row[x] = luma(uint32(r), uint32(g), uint32(bl))
			}
		default:
			for x := 0; x < w; x++ {
				r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
				row[x] = luma(r>>8, g>>8, bl>>8)
			}
		}
	}
	return dst, w, h
}

// CellEdges runs a 3x3 Sobel over the image and sums, per cell, the
// structure tensor of the gradients. dst and scratch are reused when
// large enough; scratch receives the luma plane.
func CellEdges(img image.Image, g grid.Grid, dst []Edge, scratch []uint8) ([]Edge, []uint8) {
	n := g.Cells()
	if cap(dst) < n {
		dst = make([]Edge, n)
	}
	dst = dst[:n]
	plane, w, h := lumaPlane(img, scratch)
	at := func(x, y int) int32 {
		if x < 0 {
			x = 0
		} else if x >= w {
			x = w - 1
		}
		if y < 0 {
			y = 0
		} else if y >= h {
			y = h - 1
		}
		return int32(plane[y*w+x])
	}
	const norm = 1.0 / (4 * 255) // a full step under the Sobel kernel is 4*255
	for row := 0; row < g.Rows; row++ {
		y0 := row * g.CellH
		for col := 0; col < g.Cols; col++ {
			x0 := col * g.CellW
			var jxx, jyy, jxy, e, ey float64
			for y := y0; y < y0+g.CellH && y < h; y++ {
				for x := x0; x < x0+g.CellW && x < w; x++ {
					gx := float64(-at(x-1, y-1)+at(x+1, y-1)-2*at(x-1, y)+2*at(x+1, y)-at(x-1, y+1)+at(x+1, y+1)) * norm
					gy := float64(-at(x-1, y-1)-2*at(x, y-1)-at(x+1, y-1)+at(x-1, y+1)+2*at(x, y+1)+at(x+1, y+1)) * norm
					jxx += gx * gx
					jyy += gy * gy
					jxy += gx * gy
					en := gx*gx + gy*gy
					e += en
					ey += en * float64(y-y0)
				}
			}
			cells := float64(g.CellW * g.CellH)
			jxx, jyy, jxy = jxx/cells, jyy/cells, jxy/cells
			// Eigenvalues of the tensor: the anisotropic part is l1-l2.
			aniso := 2 * math.Hypot((jxx-jyy)/2, jxy)
			ed := Edge{Strength: float32(math.Sqrt(aniso))}
			if e > 0 {
				// Dominant gradient direction, then the edge is perpendicular.
				theta := 0.5 * math.Atan2(2*jxy, jxx-jyy)
				// Gradient vector in pixel space -> edge vector in glyph space
				// (x scaled by 1/CellW, y by 1/CellH, y up).
				gxv, gyv := math.Cos(theta), math.Sin(theta)
				ex, eyv := -gyv/float64(g.CellW), gxv/float64(g.CellH) // perpendicular to the gradient
				ang := math.Atan2(-eyv, ex) * 180 / math.Pi            // screen y grows downward
				for ang < 0 {
					ang += 180
				}
				for ang >= 180 {
					ang -= 180
				}
				ed.Angle = float32(ang)
				ed.Low = ey/e > float64(g.CellH)*0.6
			}
			dst[row*g.Cols+col] = ed
		}
	}
	return dst, plane
}

// EdgeMask decides which cells are drawn as edges. A cell is an edge when
// its strength exceeds threshold, or exceeds 60% of it while touching (8
// neighbours) a cell above threshold, so contours stay continuous where
// they fade. Edge cells with no edge neighbour at all are dropped: a
// contour always spans several cells, an isolated one is texture.
func EdgeMask(edges []Edge, cols, rows int, threshold float64, dst []bool) []bool {
	n := cols * rows
	if cap(dst) < n {
		dst = make([]bool, n)
	}
	dst = dst[:n]
	strong := make([]bool, n)
	for i, e := range edges {
		strong[i] = float64(e.Strength) > threshold
	}
	low := float32(threshold * 0.6)
	neighbours := func(mask []bool, x, y int) int {
		c := 0
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				if dx == 0 && dy == 0 {
					continue
				}
				nx, ny := x+dx, y+dy
				if nx >= 0 && ny >= 0 && nx < cols && ny < rows && mask[ny*cols+nx] {
					c++
				}
			}
		}
		return c
	}
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			i := y*cols + x
			dst[i] = strong[i] || (edges[i].Strength > low && neighbours(strong, x, y) > 0)
		}
	}
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			i := y*cols + x
			if dst[i] && neighbours(dst, x, y) == 0 {
				strong[i] = false // reuse as "keep" marker below
			} else {
				strong[i] = dst[i]
			}
		}
	}
	copy(dst, strong)
	return dst
}
