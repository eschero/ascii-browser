package caption

import (
	"image"
	"image/color"
	"strings"
	"testing"
)

func TestRenderFitsAndAligns(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 400, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 400; x++ {
			img.SetGray(x, y, color.Gray{uint8(x * 255 / 399)})
		}
	}
	for name, st := range Styles {
		out := Render(img, st, 60, Limit, false)
		if n := len([]rune(out)); n > Limit {
			t.Fatalf("%s: %d characters, over the limit", name, n)
		}
		lines := strings.Split(out, "\n")
		w := len([]rune(lines[0]))
		for i, l := range lines {
			if len([]rune(l)) != w {
				t.Fatalf("%s: line %d is %d wide, want %d", name, i, len([]rune(l)), w)
			}
			if strings.ContainsAny(l, "#@") {
				t.Fatalf("%s: line %d has # or @, which a caption turns into links", name, i)
			}
			if strings.Contains(l, " ") {
				t.Fatalf("%s: line %d has a space, which a caption would drop", name, i)
			}
		}
		if !st.Braille {
			for _, r := range out {
				if r != '\n' && !strings.ContainsRune(st.Ramp, r) {
					t.Fatalf("%s: %q is not in the equal-width ramp", name, r)
				}
			}
		}
	}
}
