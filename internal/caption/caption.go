// Package caption turns a picture into text for a caption: a social
// network's description field, set in the phone's proportional system font,
// where ordinary ASCII art falls apart because an "i" is narrower than a
// "W", and where runs of spaces and spaces at the start of a line are
// dropped.
//
// Two ways around it. Braille patterns are all the same width in every
// system font, the blank one included, and give 2x4 dots per character.
// Plain ASCII works only with characters that happen to share one width in
// the reader's font: measured in Chrome, 18 of them in Apple's system font
// and 26 in Roboto, Android's, ordered here from light to dark. Neither set
// contains the space, so the lightest of them stands for white. Neither
// contains "#" or "@" either: in a caption they start a hashtag or a
// mention, drawn in colour, and more than thirty hashtags get the caption
// refused.
package caption

import (
	"image"
	"strings"

	"github.com/eschero/ascii-browser/internal/pixel"
)

// A Style is a set of characters of equal width in one font, light to
// dark, with the geometry of that font: the advance of one character and
// the caption's line height, both in em.
type Style struct {
	Name    string
	Ramp    string  // light to dark; unused for braille
	Advance float64 // em
	Line    float64 // em
	Braille bool
}

// Styles are the caption styles, by name.
var Styles = map[string]Style{
	// Instagram sets captions at 14 px on 18 px lines.
	"braille": {Name: "braille", Advance: 0.684, Line: 18.0 / 14, Braille: true},
	"ios":     {Name: "ios", Ramp: "~^+><=YZ4K0R69$8B", Advance: 0.616, Line: 18.0 / 14},
	"android": {Name: "android", Ramp: "_1?L7unoh4e3a2bqpd05968g$", Advance: 0.556, Line: 18.0 / 14},
}

// Limit is the longest caption Instagram accepts, in characters.
const Limit = 2200

// PhoneWidth is how many characters of a style fit on one caption line on
// a phone: 343 points of text column at 14 points. A longer line wraps and
// tears the picture apart, so eight percent is kept in hand for hinting
// and for the odd glyph a hair wider than the rest.
func (st Style) PhoneWidth() int { return int(343 * 0.92 / (14 * st.Advance)) }

// Render draws img in the style, cols characters wide, shrinking the width
// until the whole text, line breaks included, fits within limit
// characters. invert puts the dense characters on the bright parts.
func Render(img image.Image, st Style, cols, limit int, invert bool) string {
	for ; cols >= 8; cols-- {
		out := render(img, st, cols, invert)
		if len([]rune(out)) <= limit {
			return out
		}
	}
	return render(img, st, 8, invert)
}

func render(img image.Image, st Style, cols int, invert bool) string {
	b := img.Bounds()
	if b.Dx() < 1 || b.Dy() < 1 {
		return ""
	}
	// Rows from the picture's proportions and the shape of one character
	// cell in the caption font.
	rows := int(float64(b.Dy())/float64(b.Dx())*float64(cols)*st.Advance/st.Line + 0.5)
	if rows < 1 {
		rows = 1
	}
	dw, dh := cols, rows // dots across and down
	if st.Braille {
		dw, dh = cols*2, rows*4
	}
	g := pixel.Downsample(img, dw, dh)
	lv := make([]float32, dw*dh)
	for i, p := range g.Pix[:dw*dh] {
		l := float32(p) / 255
		if !invert {
			l = 1 - l // ink: dark pixels are dense
		}
		lv[i] = l
	}
	stretch(lv)
	levels := 2
	if !st.Braille {
		levels = len(st.Ramp)
	}
	q := dither(lv, dw, dh, levels)
	var sb strings.Builder
	for r := 0; r < rows; r++ {
		if r > 0 {
			sb.WriteByte('\n')
		}
		for c := 0; c < cols; c++ {
			if st.Braille {
				sb.WriteRune(brailleCell(q, dw, c*2, r*4))
			} else {
				sb.WriteByte(st.Ramp[q[r*dw+c]])
			}
		}
	}
	return sb.String()
}

// stretch spreads the levels from the 2nd to the 98th percentile over the
// whole range: few pictures go from black to white.
func stretch(v []float32) {
	var hist [256]int
	for _, x := range v {
		hist[int(x*255+0.5)]++
	}
	n, acc := len(v), 0
	lo, hi := 0, 255
	for i := 0; i < 256; i++ {
		if acc += hist[i]; acc >= n/50 {
			lo = i
			break
		}
	}
	acc = 0
	for i := 255; i >= 0; i-- {
		if acc += hist[i]; acc >= n/50 {
			hi = i
			break
		}
	}
	if hi-lo < 16 {
		return
	}
	span := float32(hi-lo) / 255
	base := float32(lo) / 255
	for i, x := range v {
		x = (x - base) / span
		if x < 0 {
			x = 0
		} else if x > 1 {
			x = 1
		}
		v[i] = x
	}
}

// dither quantizes to levels steps with Floyd-Steinberg: a caption is a
// still picture, so error diffusion's finer tones are worth it.
func dither(v []float32, w, h, levels int) []int {
	out := make([]int, len(v))
	top := float32(levels - 1)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			k := int(v[i]*top + 0.5)
			if k < 0 {
				k = 0
			} else if k > levels-1 {
				k = levels - 1
			}
			out[i] = k
			e := v[i] - float32(k)/top
			if x+1 < w {
				v[i+1] += e * 7 / 16
			}
			if y+1 < h {
				if x > 0 {
					v[i+w-1] += e * 3 / 16
				}
				v[i+w] += e * 5 / 16
				if x+1 < w {
					v[i+w+1] += e * 1 / 16
				}
			}
		}
	}
	return out
}

// brailleCell packs the 2x4 dots at (x, y) into one braille pattern. The
// blank pattern, U+2800, is kept as is: unlike a space it is never dropped.
func brailleCell(q []int, w, x, y int) rune {
	// Dot numbering of the Unicode braille block, column by column.
	bits := [4][2]rune{{0x01, 0x08}, {0x02, 0x10}, {0x04, 0x20}, {0x40, 0x80}}
	h := len(q) / w
	var r rune = 0x2800
	for dy := 0; dy < 4; dy++ {
		for dx := 0; dx < 2; dx++ {
			if x+dx < w && y+dy < h && q[(y+dy)*w+x+dx] > 0 {
				r |= bits[dy][dx]
			}
		}
	}
	return r
}
