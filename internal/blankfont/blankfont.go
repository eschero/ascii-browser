// Package blankfont generates, in memory, the "BlankMono" TrueType font
// that the browser forces on every page: two empty glyphs, a fixed advance
// of half an em, and a cmap format 13 that maps every Unicode code point
// to the empty glyph. With this font a screenshot contains the page's
// graphics but none of its text, and every character occupies exactly one
// grid cell. The idea comes from Browsh's BlankMono; the file itself is
// built here from scratch (704 bytes) so no binary asset ships in the repo.
package blankfont

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"sort"
	"strconv"
	"sync"
)

const (
	unitsPerEm = 1000
	advance    = unitsPerEm / 2 // 0.5 em: at font-size 2*CellW the advance is exactly CellW
	numGlyphs  = 2
)

var (
	once sync.Once
	ttf  []byte
)

// TTF returns the font file. The result is shared; do not modify it.
func TTF() []byte {
	once.Do(func() { ttf = build() })
	return ttf
}

// CSS returns the stylesheet injected in every document: the @font-face
// for BlankMono plus the rule that forces every element onto the grid.
// Sizes are in CSS pixels: with the page rendered at deviceScaleFactor
// zoom, a cell is cellW/zoom x cellH/zoom CSS pixels, so font-size is
// 2*cellW/zoom (the 0.5em advance then equals one cell) and line-height is
// cellH/zoom. vertical-align is pinned to baseline (table cells excepted,
// where it means something else) because super/sub raise an inline box and
// grow the line box past one row, breaking the line pitch.
func CSS(cellW, cellH int, zoom float64) string {
	if zoom <= 0 {
		zoom = 1
	}
	px := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	return fmt.Sprintf(`@font-face{font-family:"BlankMono";src:url(data:font/ttf;base64,%s)}
html *,html *::before,html *::after{font-family:"BlankMono"!important;font-size:%spx!important;line-height:%spx!important;letter-spacing:0!important;word-spacing:0!important;text-shadow:none!important;font-variant:normal!important}
html *:not(td):not(th){vertical-align:baseline!important}`,
		base64.StdEncoding.EncodeToString(TTF()), px(2*float64(cellW)/zoom), px(float64(cellH)/zoom))
}

type buf struct{ b []byte }

func (w *buf) u8(v uint8)   { w.b = append(w.b, v) }
func (w *buf) u16(v uint16) { w.b = binary.BigEndian.AppendUint16(w.b, v) }
func (w *buf) i16(v int16)  { w.b = binary.BigEndian.AppendUint16(w.b, uint16(v)) }
func (w *buf) u32(v uint32) { w.b = binary.BigEndian.AppendUint32(w.b, v) }
func (w *buf) i64(v int64)  { w.b = binary.BigEndian.AppendUint64(w.b, uint64(v)) }
func (w *buf) raw(p []byte) { w.b = append(w.b, p...) }

func pad4(b []byte) []byte {
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

func checksum(b []byte) uint32 {
	b = pad4(append([]byte(nil), b...))
	var s uint32
	for i := 0; i < len(b); i += 4 {
		s += binary.BigEndian.Uint32(b[i:])
	}
	return s
}

func build() []byte {
	tables := map[string][]byte{
		"OS/2": os2(),
		"cmap": cmap(),
		"glyf": glyf(),
		"head": head(),
		"hhea": hhea(),
		"hmtx": hmtx(),
		"loca": loca(),
		"maxp": maxp(),
		"name": name(),
		"post": post(),
	}
	tags := make([]string, 0, len(tables))
	for t := range tables {
		tags = append(tags, t)
	}
	sort.Strings(tags) // byte order: "OS/2" sorts before the lowercase tags

	n := len(tags)
	entrySelector := 0
	for (1 << (entrySelector + 1)) <= n {
		entrySelector++
	}
	searchRange := 16 << entrySelector

	var w buf
	w.u32(0x00010000)
	w.u16(uint16(n))
	w.u16(uint16(searchRange))
	w.u16(uint16(entrySelector))
	w.u16(uint16(n*16 - searchRange))

	offset := 12 + 16*n
	var data []byte
	headOffset := 0
	for _, t := range tags {
		tb := tables[t]
		if t == "head" {
			headOffset = offset + len(data)
		}
		w.raw([]byte(t))
		w.u32(checksum(tb))
		w.u32(uint32(offset + len(data)))
		w.u32(uint32(len(tb)))
		data = append(data, pad4(append([]byte(nil), tb...))...)
	}
	font := append(w.b, data...)
	adjust := 0xB1B0AFBA - checksum(font)
	binary.BigEndian.PutUint32(font[headOffset+8:], adjust)
	return font
}

func head() []byte {
	var w buf
	w.u32(0x00010000) // version
	w.u32(0x00010000) // fontRevision
	w.u32(0)          // checkSumAdjustment, patched in build
	w.u32(0x5F0F3CF5) // magicNumber
	w.u16(0x000B)     // flags
	w.u16(unitsPerEm)
	w.i64(0) // created
	w.i64(0) // modified
	for i := 0; i < 4; i++ {
		w.i16(0) // xMin yMin xMax yMax
	}
	w.u16(0) // macStyle
	w.u16(8) // lowestRecPPEM
	w.i16(2) // fontDirectionHint
	w.i16(0) // indexToLocFormat: short offsets
	w.i16(0) // glyphDataFormat
	return w.b
}

func hhea() []byte {
	var w buf
	w.u32(0x00010000)
	w.i16(800)  // ascender
	w.i16(-200) // descender
	w.i16(0)    // lineGap
	w.u16(advance)
	for i := 0; i < 3; i++ {
		w.i16(0) // minLSB minRSB xMaxExtent
	}
	w.i16(1) // caretSlopeRise
	w.i16(0) // caretSlopeRun
	w.i16(0) // caretOffset
	for i := 0; i < 4; i++ {
		w.i16(0) // reserved
	}
	w.i16(0) // metricDataFormat
	w.u16(1) // numberOfHMetrics: every glyph shares one advance
	return w.b
}

func maxp() []byte {
	var w buf
	w.u32(0x00010000)
	w.u16(numGlyphs)
	for _, v := range []uint16{1, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0} {
		w.u16(v)
	}
	return w.b
}

func os2() []byte {
	var w buf
	w.u16(3) // version
	w.i16(advance)
	w.u16(400) // usWeightClass
	w.u16(5)   // usWidthClass
	w.u16(0)   // fsType
	for _, v := range []int16{650, 600, 0, 75, 650, 600, 0, 350, 50, 250, 0} {
		w.i16(v)
	}
	w.raw([]byte{2, 0, 5, 9, 0, 0, 0, 0, 0, 0}) // panose, monospaced
	for _, v := range []uint32{0xFFFFFFFF, 0xFFFFFFFF, 0xFFFFFFFF, 0x0FFFFFFF} {
		w.u32(v)
	}
	w.raw([]byte("OVRS"))
	w.u16(0x0040) // fsSelection: regular
	w.u16(0x0020) // usFirstCharIndex
	w.u16(0xFFFF) // usLastCharIndex
	w.i16(800)
	w.i16(-200)
	w.i16(0)
	w.u16(800)
	w.u16(200)
	w.u32(0xFFFFFFFF)
	w.u32(0xFFFFFFFF)
	w.i16(500) // sxHeight
	w.i16(700) // sCapHeight
	w.u16(0)   // usDefaultChar
	w.u16(32)  // usBreakChar
	w.u16(0)   // usMaxContext
	return w.b
}

func hmtx() []byte {
	var w buf
	w.u16(advance)
	w.i16(0)
	w.i16(0) // left side bearing of glyph 1
	return w.b
}

// glyf holds one glyph, .notdef, made of a single point at the origin.
// Chrome's OpenType sanitizer rejects a zero-length glyf table, so the
// glyph has to exist; glyph 1, the one every character maps to, is empty.
func glyf() []byte {
	var w buf
	w.i16(1) // numberOfContours
	for i := 0; i < 4; i++ {
		w.i16(0) // bbox
	}
	w.u16(0)   // endPtsOfContours[0]
	w.u16(0)   // instructionLength
	w.u8(0x01) // flags: on curve
	w.i16(0)   // x
	w.i16(0)   // y
	w.u8(0)    // pad to an even length for the short loca format
	return w.b
}

func loca() []byte {
	var w buf
	w.u16(0)
	w.u16(uint16(len(glyf()) / 2))
	w.u16(uint16(len(glyf()) / 2)) // glyph 1 is empty
	return w.b
}

// cmap has one subtable, (3,10) format 13: many-to-one mapping of the
// whole code space onto glyph 1.
func cmap() []byte {
	var w buf
	w.u16(0)  // version
	w.u16(1)  // numTables
	w.u16(3)  // platformID
	w.u16(10) // encodingID
	w.u32(12) // subtable offset
	w.u16(13) // format
	w.u16(0)
	w.u32(28) // length
	w.u32(0)  // language
	w.u32(1)  // numGroups
	w.u32(0)
	w.u32(0x10FFFF)
	w.u32(1)
	return w.b
}

func name() []byte {
	records := []struct {
		id  uint16
		str string
	}{
		{1, "BlankMono"}, {2, "Regular"}, {3, "BlankMono"}, {4, "BlankMono"},
		{5, "Version 1.0"}, {6, "BlankMono-Regular"},
	}
	var w buf
	w.u16(0)
	w.u16(uint16(len(records)))
	w.u16(uint16(6 + 12*len(records)))
	var strs buf
	for _, r := range records {
		w.u16(3)
		w.u16(1)
		w.u16(0x409)
		w.u16(r.id)
		w.u16(uint16(2 * len(r.str)))
		w.u16(uint16(len(strs.b)))
		for _, c := range r.str {
			strs.u16(uint16(c))
		}
	}
	w.raw(strs.b)
	return w.b
}

func post() []byte {
	var w buf
	w.u32(0x00030000)
	w.u32(0) // italicAngle
	w.i16(0) // underlinePosition
	w.i16(0) // underlineThickness
	for i := 0; i < 5; i++ {
		w.u32(0) // isFixedPitch, min/max mem
	}
	return w.b
}
