package ui

import (
	"testing"

	"github.com/gdamore/tcell/v2"
)

func key(k tcell.Key) *tcell.EventKey { return tcell.NewEventKey(k, 0, tcell.ModNone) }
func rune_(r rune) *tcell.EventKey    { return tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone) }

func TestURLBar(t *testing.T) {
	var u URLBar
	u.Set("example.com")
	u.Handle(key(tcell.KeyHome))
	for _, r := range "https://" {
		u.Handle(rune_(r))
	}
	u.Handle(key(tcell.KeyEnd))
	u.Handle(key(tcell.KeyBackspace2))
	u.Handle(rune_('g'))
	u.Handle(rune_('è')) // non-ASCII: ignored
	if got := u.String(); got != "https://example.cog" {
		t.Fatalf("text = %q", got)
	}
	if a := u.Handle(key(tcell.KeyEnter)); a != BarSubmit {
		t.Fatalf("Enter = %v, want submit", a)
	}
	if a := u.Handle(key(tcell.KeyEscape)); a != BarCancel {
		t.Fatalf("Escape = %v, want cancel", a)
	}
	u.Handle(key(tcell.KeyCtrlU))
	if u.String() != "" || u.cur != 0 {
		t.Fatalf("Ctrl+U left %q", u.String())
	}
}

func TestURLBarView(t *testing.T) {
	var u URLBar
	u.Set("0123456789")
	text, cur := u.View(4) // cursor at 10 must stay visible
	if text != "789" || cur != 3 {
		t.Fatalf("view = %q cursor %d", text, cur)
	}
	u.Handle(key(tcell.KeyHome))
	if text, cur = u.View(4); text != "0123" || cur != 0 {
		t.Fatalf("view at home = %q cursor %d", text, cur)
	}
}
