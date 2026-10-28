package render

import (
	"errors"
	"fmt"
	"strings"

	"github.com/eschero/ascii-browser/internal/ascii"
)

// Ramp lists the characters used for cell darkness, from lightest
// (index 0, normally a space) to darkest. Only printable ASCII is allowed.
type Ramp []byte

// DefaultRamp has 10 levels.
var DefaultRamp = Ramp(" .:-=+*#%@")

// Presets selectable with --ramp-preset, all from light to dark.
var Presets = map[string]Ramp{
	"standard": DefaultRamp,
	// short: 6 levels, more contrast.
	"short": Ramp(" .:+#@"),
	// long: the classic 70-character ramp, reversed to run light to dark.
	"long": Ramp(" .'`^\",:;Il!i><~+_-?][}{1)(|\\/tfjrxnuvczXYUJCLQ0OZmwqpdbkhao*#MW&8%B@$"),
}

// ParseRamp validates a --ramp flag value.
func ParseRamp(s string) (Ramp, error) {
	if len(s) < 2 {
		return nil, errors.New("ramp needs at least 2 characters")
	}
	for i := 0; i < len(s); i++ {
		if !ascii.IsPrintable(s[i]) {
			return nil, fmt.Errorf("ramp byte %d (0x%02X) is not printable ASCII", i, s[i])
		}
	}
	return Ramp(s), nil
}

// StructureGlyphs are the characters --structure draws edges with. A ramp
// used together with --structure must not contain them, or tones and
// edges become indistinguishable.
const StructureGlyphs = "/\\|-_"

// Without returns the ramp with the given bytes removed, keeping order.
func (r Ramp) Without(chars string) Ramp {
	out := make(Ramp, 0, len(r))
	for _, c := range r {
		if !strings.ContainsRune(chars, rune(c)) {
			out = append(out, c)
		}
	}
	return out
}
