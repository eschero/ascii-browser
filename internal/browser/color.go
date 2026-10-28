package browser

import (
	"strconv"
	"strings"
)

// parseCSSColor reads the serialized computed colors Chrome returns:
// "rgb(r, g, b)", "rgba(r, g, b, a)", "color(srgb r g b / a)" and
// "transparent". It returns the Rec. 709 luma in 0..1 and the alpha.
// An unparsable value counts as opaque mid gray, which is the safe
// assumption for occlusion.
func parseCSSColor(s string) (luma, alpha float64) {
	s = strings.TrimSpace(s)
	switch {
	case s == "" || s == "none":
		return 0, 0
	case s == "transparent":
		return 0, 0
	}
	open := strings.IndexByte(s, '(')
	if open < 0 || !strings.HasSuffix(s, ")") {
		return 0.5, 1
	}
	fn := strings.ToLower(s[:open])
	body := s[open+1 : len(s)-1]
	body = strings.ReplaceAll(body, "/", " ")
	body = strings.ReplaceAll(body, ",", " ")
	fields := strings.Fields(body)
	num := func(i int, scale float64) (float64, bool) {
		if i >= len(fields) {
			return 0, false
		}
		f := fields[i]
		if strings.HasSuffix(f, "%") {
			v, err := strconv.ParseFloat(strings.TrimSuffix(f, "%"), 64)
			return v / 100, err == nil
		}
		v, err := strconv.ParseFloat(f, 64)
		return v / scale, err == nil
	}
	var r, g, b float64
	var ok bool
	first := 0
	if fn == "color" {
		first = 1 // the color space name
	}
	scale := 255.0
	if fn == "color" {
		scale = 1
	}
	if r, ok = num(first, scale); !ok {
		return 0.5, 1
	}
	if g, ok = num(first+1, scale); !ok {
		return 0.5, 1
	}
	if b, ok = num(first+2, scale); !ok {
		return 0.5, 1
	}
	alpha = 1
	if a, ok := num(first+3, 1); ok {
		alpha = a
	}
	clamp := func(v float64) float64 {
		if v < 0 {
			return 0
		}
		if v > 1 {
			return 1
		}
		return v
	}
	r, g, b, alpha = clamp(r), clamp(g), clamp(b), clamp(alpha)
	return 0.2126*r + 0.7152*g + 0.0722*b, alpha
}
