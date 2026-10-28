// Package ascii maps Unicode text onto the 95 printable ASCII characters.
// The mapping is strictly one rune to one byte, so a text run never grows
// past the cells it was measured in.
package ascii

// DefaultFiller replaces runes that have no ASCII counterpart.
const DefaultFiller byte = '?'

// IsPrintable reports whether b is in 0x20..0x7E.
func IsPrintable(b byte) bool { return b >= 0x20 && b <= 0x7E }

// Latin-1 Supplement U+00A0..U+00FF. A zero byte means "use the filler".
const latin1 = " !cLoY|S\"ca<--R-o+23'uP.,1o>\x00\x00\x00?" +
	"AAAAAAACEEEEIIIIDNOOOOOxOUUUUYTs" +
	"aaaaaaaceeeeiiiidnooooo/ouuuuyty"

// Latin Extended-A U+0100..U+017F.
const latinA = "AaAaAaCcCcCcCcDdDdEeEeEeEeEeGgGgGgGgHhHhIiIiIiIiIiIiJjKkk" +
	"LlLlLlLlLlNnNnNnnNnOoOoOoOoRrRrRrSsSsSsSsTtTtTtUuUuUuUuUuUuWwYyYZzZzZzs"

// General Punctuation U+2010..U+203A.
const punct = "------" + "|" + "=" + "''''" + "\"\"\"\"" + "++" + "**" + "...." +
	"  " + "\x00\x00\x00\x00\x00" + " " + "%%" + "''''" + "\"\"" + "^" + "<" + ">"

// Rune maps r to one printable ASCII byte. Runes without a mapping become
// filler; a filler outside 0x20..0x7E falls back to DefaultFiller.
func Rune(r rune, filler byte) byte {
	if !IsPrintable(filler) {
		filler = DefaultFiller
	}
	switch {
	case r >= 0x20 && r <= 0x7E:
		return byte(r)
	case r >= 0xA0 && r <= 0xFF:
		return pick(latin1[r-0xA0], filler)
	case r >= 0x100 && r <= 0x17F:
		return latinA[r-0x100]
	case r >= 0x2000 && r <= 0x200A, r == 0x202F, r == 0x205F, r == 0x3000:
		return ' '
	case r >= 0x2010 && r <= 0x203A:
		return pick(punct[r-0x2010], filler)
	case r == 0x2212, r == 0x2043: // minus sign, hyphen bullet
		return '-'
	case r == 0x2044: // fraction slash
		return '/'
	case r == 0x2190:
		return '<'
	case r == 0x2191:
		return '^'
	case r == 0x2192:
		return '>'
	case r == 0x2193:
		return 'v'
	case r == 0x20AC: // euro
		return 'E'
	}
	return filler
}

func pick(b, filler byte) byte {
	if b == 0 {
		return filler
	}
	return b
}

// Bytes transliterates a whole string, one byte per rune.
func Bytes(s string, filler byte) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		out = append(out, Rune(r, filler))
	}
	return out
}
