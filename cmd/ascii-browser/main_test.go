package main

import "testing"

func TestParseSize(t *testing.T) {
	if c, r, err := parseSize("200x49"); err != nil || c != 200 || r != 49 {
		t.Fatalf("200x49 -> %d %d %v", c, r, err)
	}
	if c, r, err := parseSize("800x0"); err != nil || c != 800 || r != 0 {
		t.Fatalf("800x0 -> %d %d %v", c, r, err)
	}
	if c, r, err := parseSize("0x0"); err != nil || c != 0 || r != 0 {
		t.Fatalf("0x0 -> %d %d %v", c, r, err)
	}
	for _, bad := range []string{"", "x", "10", "-5x3", "3x-1"} {
		if _, _, err := parseSize(bad); err == nil {
			t.Errorf("parseSize(%q) accepted", bad)
		}
	}
}

func TestFullRows(t *testing.T) {
	// 15000 CSS px at zoom 4 = 60000 device px = 3750 rows of 16.
	if got := fullRows(15000, 4, 16, 800, 4_000_000); got != 3750 {
		t.Fatalf("rows = %d, want 3750", got)
	}
	// Capped by cells: 4M / 800 = 5000 rows.
	if got := fullRows(100000, 4, 16, 800, 4_000_000); got != 5000 {
		t.Fatalf("capped rows = %d, want 5000", got)
	}
	if got := fullRows(0, 1, 16, 80, 4_000_000); got != 1 {
		t.Fatalf("empty page rows = %d, want 1", got)
	}
}

func TestClampWindow(t *testing.T) {
	// Frame 800x192, terminal window 200x48.
	if x, y := clampWindow(-5, 10, 200, 48, 800, 192); x != 0 || y != 10 {
		t.Fatalf("got %d,%d", x, y)
	}
	if x, y := clampWindow(700, 300, 200, 48, 800, 192); x != 600 || y != 144 {
		t.Fatalf("got %d,%d", x, y)
	}
	// Frame smaller than the window: always 0,0.
	if x, y := clampWindow(3, 3, 200, 48, 100, 20); x != 0 || y != 0 {
		t.Fatalf("got %d,%d", x, y)
	}
}
