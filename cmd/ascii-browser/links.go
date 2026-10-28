package main

import (
	"sort"

	"github.com/eschero/ascii-browser/internal/frame"
)

// focusable is something Tab can land on: one anchor (all its runs) or one
// form field. key re-identifies it across captures, when runs are rebuilt.
type focusable struct {
	key    string
	href   string
	field  bool
	button bool
	text   string
	runs   []int // indices into the capture's runs
	y, x   int   // first cell, for reading order
}

// collectFocusables groups link and button runs by element and lists
// fields, in reading order (row, then column).
func collectFocusables(runs []frame.TextRun) []focusable {
	var items []focusable
	byLink := map[int]int{}
	for i, r := range runs {
		switch {
		case r.LinkID > 0:
			if j, ok := byLink[r.LinkID]; ok {
				items[j].runs = append(items[j].runs, i)
				if r.Y < items[j].y || (r.Y == items[j].y && r.X < items[j].x) {
					items[j].y, items[j].x = r.Y, r.X
				}
				continue
			}
			byLink[r.LinkID] = len(items)
			kind := "link"
			if r.Button {
				kind = "button"
			}
			items = append(items, focusable{key: kind + "\x00" + r.Href + "\x00" + r.Text, href: r.Href, button: r.Button, text: r.Text, runs: []int{i}, y: r.Y, x: r.X})
		case r.Field:
			items = append(items, focusable{key: "field\x00" + r.Text, field: true, text: r.Text, runs: []int{i}, y: r.Y, x: r.X})
		}
	}
	sort.SliceStable(items, func(a, b int) bool {
		if items[a].y != items[b].y {
			return items[a].y < items[b].y
		}
		return items[a].x < items[b].x
	})
	return items
}

// findKey returns the index of the item with the given key, or -1.
func findKey(items []focusable, key string) int {
	for i, it := range items {
		if it.key == key {
			return i
		}
	}
	return -1
}
