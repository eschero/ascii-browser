package ascii

import "testing"

func TestTables(t *testing.T) {
	if len(latin1) != 0x60 {
		t.Fatalf("latin1 table has %d entries, want 96", len(latin1))
	}
	if len(latinA) != 0x80 {
		t.Fatalf("latinA table has %d entries, want 128", len(latinA))
	}
	if len(punct) != 0x2B {
		t.Fatalf("punct table has %d entries, want 43", len(punct))
	}
}

func TestRune(t *testing.T) {
	cases := map[rune]byte{
		'a': 'a', '~': '~', 'à': 'a', 'È': 'E', 'ñ': 'n', 'ß': 's', 'Ł': 'L', 'ž': 'z',
		' ': ' ', '—': '-', '’': '\'', '“': '"', '…': '.',
		'•': '*', '€': 'E', '→': '>', '½': '?', '日': '?', '😀': '?',
	}
	for r, want := range cases {
		if got := Rune(r, '?'); got != want {
			t.Errorf("Rune(%U) = %q, want %q", r, got, want)
		}
	}
	if got := Rune('日', '#'); got != '#' {
		t.Errorf("custom filler: got %q", got)
	}
	if got := Rune('日', 0x01); got != '?' {
		t.Errorf("invalid filler must fall back to '?', got %q", got)
	}
	for r := rune(0); r < 0x3000; r++ {
		if !IsPrintable(Rune(r, '?')) {
			t.Fatalf("Rune(%U) is not printable ASCII", r)
		}
	}
}
