package main

import (
	"testing"

	"github.com/eschero/ascii-browser/internal/frame"
)

func TestCollectFocusables(t *testing.T) {
	runs := []frame.TextRun{
		{Text: "plain", X: 0, Y: 0, W: 5},
		{Text: "second", X: 10, Y: 2, W: 6, Link: true, Href: "http://b", LinkID: 2},
		{Text: "first", X: 20, Y: 0, W: 5, Link: true, Href: "http://a", LinkID: 1},
		{Text: "line", X: 0, Y: 1, W: 4, Link: true, Href: "http://a", LinkID: 1}, // same anchor, wrapped
		{Text: "Search", X: 5, Y: 1, W: 20, Field: true},
	}
	items := collectFocusables(runs)
	if len(items) != 3 {
		t.Fatalf("got %d items: %+v", len(items), items)
	}
	if items[0].href != "http://a" || len(items[0].runs) != 2 {
		t.Errorf("first item = %+v", items[0])
	}
	if !items[1].field || items[1].text != "Search" {
		t.Errorf("second item = %+v", items[1])
	}
	if items[2].href != "http://b" {
		t.Errorf("third item = %+v", items[2])
	}
	if findKey(items, items[2].key) != 2 || findKey(items, "nope") != -1 {
		t.Errorf("findKey")
	}
}
