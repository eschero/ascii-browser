package browser

import "testing"

func TestParseCSSColor(t *testing.T) {
	cases := []struct {
		in    string
		luma  float64
		alpha float64
	}{
		{"rgb(255, 255, 255)", 1, 1},
		{"rgb(0, 0, 0)", 0, 1},
		{"rgba(0, 0, 0, 0)", 0, 0},
		{"transparent", 0, 0},
		{"", 0, 0},
		{"rgba(136, 136, 136, 0.5)", 136.0 / 255, 0.5},
		{"color(srgb 1 1 1)", 1, 1},
		{"color(srgb 0 0 0 / 0.25)", 0, 0.25},
		{"weird-value", 0.5, 1},
	}
	for _, c := range cases {
		l, a := parseCSSColor(c.in)
		if diff(l, c.luma) > 0.01 || diff(a, c.alpha) > 0.01 {
			t.Errorf("parseCSSColor(%q) = %.3f, %.3f; want %.3f, %.3f", c.in, l, a, c.luma, c.alpha)
		}
	}
}

func diff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}
