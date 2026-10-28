package render

import "math"

// Quantize maps per-cell values in 0..1 onto ramp indices 0..levels-1.
// With dither it applies Floyd-Steinberg error diffusion on the cell grid
// (not on pixels), modifying v in place. Cells flagged in fixed (nil for
// none) always get index 0 and take no part in the diffusion: they neither
// emit error nor keep the error that reaches them. dst is reused when
// large enough.
func Quantize(v []float32, cols, rows, levels int, dither bool, fixed []bool, dst []uint8) []uint8 {
	n := cols * rows
	if cap(dst) < n {
		dst = make([]uint8, n)
	}
	dst = dst[:n]
	maxL := float32(levels - 1)
	quant := func(x float32) float32 {
		q := float32(math.Round(float64(x * maxL)))
		if q < 0 {
			return 0
		}
		if q > maxL {
			return maxL
		}
		return q
	}
	isFixed := func(i int) bool { return fixed != nil && fixed[i] }
	if !dither {
		for i := 0; i < n; i++ {
			if isFixed(i) {
				dst[i] = 0
				continue
			}
			dst[i] = uint8(quant(v[i]))
		}
		return dst
	}
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			i := y*cols + x
			if isFixed(i) {
				dst[i] = 0
				continue
			}
			old := v[i]
			q := quant(old)
			dst[i] = uint8(q)
			e := old - q/maxL
			if x+1 < cols {
				v[i+1] += e * 7 / 16
			}
			if y+1 < rows {
				if x > 0 {
					v[i+cols-1] += e * 3 / 16
				}
				v[i+cols] += e * 5 / 16
				if x+1 < cols {
					v[i+cols+1] += e * 1 / 16
				}
			}
		}
	}
	return dst
}
