package blankfont

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// The expected hash is that of the prototype font that was loaded in Chrome
// 153 headless and measured: 8.00px per character at 16px, text absent from
// the screenshot. Any change here must be re-validated in a real Chrome.
const validatedSHA256 = "7c7fbb1855cc68c66aabe8d20926be309a627262621b49b3522e5c2ba1179826"

func TestTTFMatchesValidatedPrototype(t *testing.T) {
	b := TTF()
	if len(b) != 704 {
		t.Fatalf("font is %d bytes, want 704", len(b))
	}
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); got != validatedSHA256 {
		t.Fatalf("sha256 = %s, want %s", got, validatedSHA256)
	}
}

func TestCSS(t *testing.T) {
	css := CSS(8, 16, 1)
	for _, want := range []string{"font-size:16px!important", "line-height:16px!important", "data:font/ttf;base64,AAEAAAAKAIAAAwAg"} {
		if !strings.Contains(css, want) {
			t.Errorf("CSS lacks %q", want)
		}
	}
	css = CSS(8, 16, 2)
	for _, want := range []string{"font-size:8px!important", "line-height:8px!important"} {
		if !strings.Contains(css, want) {
			t.Errorf("CSS at zoom 2 lacks %q", want)
		}
	}
	if css = CSS(8, 16, 3); !strings.Contains(css, "font-size:5.333333333333333px") {
		t.Errorf("CSS at zoom 3 = %q", css)
	}
}
