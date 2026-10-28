package browser

import (
	"context"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/chromedp/cdproto/domsnapshot"
	"github.com/chromedp/chromedp"

	"github.com/eschero/ascii-browser/internal/frame"
	"github.com/eschero/ascii-browser/internal/grid"
)

// snapStyles are the computed styles asked for with every snapshot. The
// order is fixed: the st* constants index into a layout node's style list.
var snapStyles = []string{
	"visibility", "opacity", "overflow-x", "overflow-y", "position",
	"background-color", "padding-top", "padding-left", "list-style-type",
	"transition-property", "transition-duration", "animation-name", "animation-duration",
	"border-top-width", "border-right-width", "border-bottom-width", "border-left-width",
	"border-top-color", "border-right-color", "border-bottom-color", "border-left-color",
}

const (
	stVisibility = iota
	stOpacity
	stOverflowX
	stOverflowY
	stPosition
	stBackground
	stPaddingTop
	stPaddingLeft
	stListStyle
	stTransition
	stTransitionDur
	stAnimation
	stAnimationDur
	stBorderTop
	stBorderRight
	stBorderBottom
	stBorderLeft
	stBorderTopColor
	stBorderRightColor
	stBorderBottomColor
	stBorderLeftColor
)

// mediaTags are the elements the interface renderer shows as a
// placeholder, or renders from a clipped screenshot with --ui-media.
var mediaTags = map[string]bool{
	"IMG": true, "VIDEO": true, "CANVAS": true, "SVG": true, "IFRAME": true,
	"EMBED": true, "OBJECT": true, "PICTURE": true,
}

// Replaced elements paint their own content, so they occlude text beneath
// them even without a background color.
var replacedTags = map[string]bool{
	"IMG": true, "VIDEO": true, "CANVAS": true, "SVG": true, "IFRAME": true,
	"EMBED": true, "OBJECT": true, "PICTURE": true, "INPUT": true, "TEXTAREA": true, "SELECT": true,
}

var buttonTags = map[string]bool{"BUTTON": true}

// SnapshotUI extracts the page text and the interface layer in one
// capture: container frames, form widgets and media placeholders.
func (b *Browser) SnapshotUI() ([]frame.TextRun, []frame.UIBox, error) {
	var st CaptureStats
	return b.snapshotUI(&st)
}

func (b *Browser) snapshotUI(st *CaptureStats) ([]frame.TextRun, []frame.UIBox, error) {
	if b.uiMedia {
		// Lazy images load on scroll and videos need a nudge; without
		// this the crops come back empty or black.
		t0 := time.Now()
		if _, err := b.PrepareMedia(3 * time.Second); err != nil {
			return nil, nil, err
		}
		st.Prep = time.Since(t0)
	}
	t1 := time.Now()
	s, err := b.capture()
	if err != nil {
		return nil, nil, err
	}
	s.ui, s.media, s.videoOff = true, b.uiMedia, b.VideoMode() == "off"
	runs := s.build()
	st.Snapshot = time.Since(t1)
	if b.uiMedia && len(s.shots) > 0 {
		t2 := time.Now()
		img, serr := b.Screenshot()
		if serr != nil {
			return nil, nil, serr
		}
		cropMedia(s.boxes, s.shots, img)
		b.freezePosters(s.boxes)
		st.Crops, st.NCrops = time.Since(t2), len(s.shots)
	}
	return runs, s.boxes, nil
}

func (b *Browser) capture() (*snapshot, error) {
	var docs []*domsnapshot.DocumentSnapshot
	var strs []string
	err := chromedp.Run(b.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		// DOM rects are not used: only the layout bounds are, and asking
		// for offset, scroll and client rects costs about a third of the
		// capture time on a long page.
		docs, strs, err = domsnapshot.CaptureSnapshot(snapStyles).
			WithIncludePaintOrder(true).WithIncludeDOMRects(false).Do(ctx)
		return err
	}))
	if err != nil {
		return nil, err
	}
	return &snapshot{docs: docs, strs: strs, g: b.Grid(), zoom: b.zoom, opacity: b.opacity}, nil
}

// SnapshotRuns extracts the page text with DOMSnapshot.captureSnapshot
// instead of measuring it in the page. The snapshot already flattens
// shadow trees and slots, gives one box per rendered line with its offset
// into the layout text, and a global paint order that decides occlusion,
// so no per-character measuring and no hit testing are needed.
func (b *Browser) SnapshotRuns() ([]frame.TextRun, error) {
	s, err := b.capture()
	if err != nil {
		return nil, err
	}
	return s.build(), nil
}

type snapshot struct {
	docs []*domsnapshot.DocumentSnapshot
	strs []string
	g    grid.Grid
	zoom float64

	cells  map[int]snapCell // cell index -> winning character
	occl   []int64          // cell index -> highest paint order of an opaque box
	fields []fieldMark      // empty form fields, so Tab can still reach them
	nextID int

	ui       bool          // collect the interface layer too
	media    bool          // photograph media elements one by one
	videoOff bool          // --video off: videos stay placeholders
	opacity  string        // how opacity 0 is read: pending, strict or ignore
	boxes    []frame.UIBox // frames, widgets and media placeholders
	rules    []pendingRule // heading underlines, sized once the text is in
	shots    []mediaShot   // media elements to photograph on their own
	scrollX  float64       // of the top document, to turn viewport into page coordinates
	scrollY  float64
}

// pendingRule is a heading underline waiting for the width of its text.
type pendingRule struct {
	col, row, maxCol, level int
	paint                   int64
}

type snapCell struct {
	r     rune
	paint int64
	link  *snapLink
}

type snapLink struct {
	id     int
	href   string
	button bool
}

type snapRect struct{ l, t, r, b float64 }

var noClip = snapRect{-1e9, -1e9, 1e9, 1e9}

func (a snapRect) intersect(b snapRect) snapRect {
	return snapRect{math.Max(a.l, b.l), math.Max(a.t, b.t), math.Min(a.r, b.r), math.Min(a.b, b.b)}
}
func (a snapRect) contains(x, y float64) bool { return x >= a.l && x < a.r && y >= a.t && y < a.b }

// doc holds the per-document lookup tables built once.
type snapDocView struct {
	d           *domsnapshot.DocumentSnapshot
	index       int
	ox, oy      float64 // viewport offset of this document's origin, in CSS px
	layoutOf    map[int64]int
	clipCache   map[int]snapRect
	clipOfCache map[int]snapRect
	opacity     map[int64]bool
	kids        map[int64]int
	linkOf      map[int64]*snapLink
	base        *url.URL
	utf16       map[int][]uint16
}

func (s *snapshot) str(i domsnapshot.StringIndex) string {
	if i < 0 || int(i) >= len(s.strs) {
		return ""
	}
	return s.strs[i]
}

func (s *snapshot) style(d *domsnapshot.DocumentSnapshot, layout int, which int) string {
	st := d.Layout.Styles
	if layout < 0 || layout >= len(st) || which >= len(st[layout]) {
		return ""
	}
	return s.str(domsnapshot.StringIndex(st[layout][which]))
}

func (s *snapshot) rect(r domsnapshot.Rectangle, ox, oy float64) snapRect {
	if len(r) < 4 {
		return snapRect{}
	}
	return snapRect{r[0] + ox, r[1] + oy, r[0] + r[2] + ox, r[1] + r[3] + oy}
}

// px reads a computed length in CSS pixels; 0 when absent.
func (s *snapshot) px(v *snapDocView, li int, which int) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSuffix(s.style(v.d, li, which), "px"), 64)
	return f
}

func (s *snapshot) cellW() float64 { return float64(s.g.CellW) / s.zoom }
func (s *snapshot) cellH() float64 { return float64(s.g.CellH) / s.zoom }

// build walks every document and returns the composed runs.
func (s *snapshot) build() []frame.TextRun {
	s.cells = make(map[int]snapCell)
	s.occl = make([]int64, s.g.Cells())
	if len(s.docs) == 0 {
		return nil
	}
	s.scrollX, s.scrollY = s.docs[0].ScrollOffsetX, s.docs[0].ScrollOffsetY
	s.walkDoc(0, 0, 0, make(map[int]bool))
	s.closeRules()
	return s.runs()
}

func (s *snapshot) newView(index int, ox, oy float64) *snapDocView {
	d := s.docs[index]
	v := &snapDocView{
		d: d, index: index,
		ox: ox - d.ScrollOffsetX, oy: oy - d.ScrollOffsetY,
		layoutOf:    make(map[int64]int, len(d.Layout.NodeIndex)),
		clipCache:   make(map[int]snapRect),
		clipOfCache: make(map[int]snapRect),
		opacity:     make(map[int64]bool),
		kids:        make(map[int64]int),
		linkOf:      make(map[int64]*snapLink),
		utf16:       make(map[int][]uint16),
	}
	for li, ni := range d.Layout.NodeIndex {
		v.layoutOf[ni] = li
	}
	if u, err := url.Parse(s.str(d.BaseURL)); err == nil {
		v.base = u
	}
	return v
}

// walkDoc collects occluders and text from one document, then recurses
// into the documents of its iframes.
func (s *snapshot) walkDoc(index int, ox, oy float64, seen map[int]bool) {
	if index < 0 || index >= len(s.docs) || seen[index] {
		return
	}
	seen[index] = true
	v := s.newView(index, ox, oy)
	d := v.d
	L := d.Layout

	// Pass 1: opaque boxes into the paint-order buffer, and the interface
	// layer when it is wanted.
	for li := range L.NodeIndex {
		if !s.visible(v, li) {
			continue
		}
		tag := s.tagOf(v, L.NodeIndex[li])
		if s.ui {
			s.uiNode(v, li, tag)
		}
		// Only a fully opaque background hides what is under it: a
		// translucent one lets the page show through, so it neither
		// occludes the text nor blanks the interface below.
		_, alpha := parseCSSColor(s.style(d, li, stBackground))
		opaque := alpha >= 1 || replacedTags[tag]
		if !opaque {
			continue
		}
		box := s.rect(L.Bounds[li], v.ox, v.oy).intersect(s.clipOf(v, li))
		paint := s.paint(L, li)
		s.markOccluder(box, paint)
		if s.ui && alpha >= 1 {
			if x, y, w, h := s.cellBox(box); s.onGrid(x, y, w, h) && w > 0 && h > 0 {
				s.boxes = append(s.boxes, frame.UIBox{Kind: frame.UIClear, X: x, Y: y, W: w, H: h, Paint: paint})
			}
		}
	}

	// Pass 2: text boxes.
	tb := d.TextBoxes
	if tb != nil {
		for i := range tb.LayoutIndex {
			s.textBox(v, i)
		}
	}

	// Pass 3: form controls, whose text is not a text box.
	for li, ni := range L.NodeIndex {
		switch s.tagOf(v, ni) {
		case "INPUT", "TEXTAREA", "SELECT":
			s.field(v, li, ni)
		}
	}

	// Pass 4: nested documents.
	if cd := d.Nodes.ContentDocumentIndex; cd != nil {
		for k, ni := range cd.Index {
			if k >= len(cd.Value) {
				break
			}
			li, ok := v.layoutOf[ni]
			if !ok || !s.visible(v, li) {
				continue
			}
			box := s.rect(L.Bounds[li], v.ox, v.oy)
			s.walkDoc(int(cd.Value[k]), box.l+s.px(v, li, stBorderLeft), box.t+s.px(v, li, stBorderTop), seen)
		}
	}
}

func (s *snapshot) paint(L *domsnapshot.LayoutTreeSnapshot, li int) int64 {
	if li < len(L.PaintOrders) {
		return L.PaintOrders[li]
	}
	return 0
}

func (s *snapshot) tagOf(v *snapDocView, ni int64) string {
	n := v.d.Nodes
	if ni < 0 || int(ni) >= len(n.NodeName) {
		return ""
	}
	return s.str(n.NodeName[ni])
}

func (s *snapshot) attr(v *snapDocView, ni int64, name string) (string, bool) {
	n := v.d.Nodes
	if ni < 0 || int(ni) >= len(n.Attributes) {
		return "", false
	}
	a := n.Attributes[ni]
	for i := 0; i+1 < len(a); i += 2 {
		if s.str(domsnapshot.StringIndex(a[i])) == name {
			return s.str(domsnapshot.StringIndex(a[i+1])), true
		}
	}
	return "", false
}

func (s *snapshot) parent(v *snapDocView, ni int64) int64 {
	p := v.d.Nodes.ParentIndex
	if ni < 0 || int(ni) >= len(p) {
		return -1
	}
	return p[ni]
}

// visible reports whether a layout node is painted at all: visibility and
// the opacity of its ancestors.
func (s *snapshot) visible(v *snapDocView, li int) bool {
	if s.style(v.d, li, stVisibility) != "visible" {
		return false
	}
	return !s.transparentBranch(v, v.d.Layout.NodeIndex[li])
}

// transparentBranch reports whether the node or an ancestor has opacity 0.
func (s *snapshot) transparentBranch(v *snapDocView, ni int64) bool {
	if ni < 0 || s.opacity == "ignore" {
		return false // "ignore": read the DOM even where the page paints nothing
	}
	if hit, ok := v.opacity[ni]; ok {
		return hit
	}
	res := false
	if li, ok := v.layoutOf[ni]; ok {
		if o, err := strconv.ParseFloat(s.style(v.d, li, stOpacity), 64); err == nil && o == 0 {
			// An element at opacity 0 that is animating its opacity is not
			// hidden, it is about to appear: a page that fades its content
			// in would otherwise be read while still blank.
			res = !s.fadingIn(v, li)
		}
	}
	if !res {
		res = s.transparentBranch(v, s.parent(v, ni))
	}
	v.opacity[ni] = res
	return res
}

// fadingIn reports whether a node's opacity is being animated, by a
// transition on opacity or by any animation at all.
func (s *snapshot) fadingIn(v *snapDocView, li int) bool {
	if s.opacity == "strict" {
		return false
	}
	if a := s.style(v.d, li, stAnimation); a != "" && a != "none" {
		if running(s.style(v.d, li, stAnimationDur)) {
			return true
		}
	}
	// A transition only counts when it has a duration: every element
	// reports transition-property "all" by default, so the property alone
	// would make anything at opacity 0 look like it is arriving.
	if !running(s.style(v.d, li, stTransitionDur)) {
		return false
	}
	t := s.style(v.d, li, stTransition)
	return t == "all" || strings.Contains(t, "opacity")
}

// running reports whether a comma separated list of times has one above zero.
func running(list string) bool {
	for _, part := range strings.Split(list, ",") {
		if f, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(part), "s"), 64); err == nil && f > 0 {
			return true
		}
	}
	return false
}

// clipOf is the clip that applies to a node's own box: a fixed node
// escapes every ancestor clip, an absolute one the clips up to its
// containing block, anything else inherits its parent's content clip.
func (s *snapshot) clipOf(v *snapDocView, li int) snapRect {
	if c, ok := v.clipOfCache[li]; ok {
		return c
	}
	v.clipOfCache[li] = noClip // break cycles
	ni := v.d.Layout.NodeIndex[li]
	var c snapRect
	switch s.style(v.d, li, stPosition) {
	case "fixed":
		c = noClip
	case "absolute":
		c = noClip
		for p := s.parent(v, ni); p >= 0; p = s.parent(v, p) {
			pl, ok := v.layoutOf[p]
			if !ok {
				continue
			}
			if pos := s.style(v.d, pl, stPosition); pos != "static" && pos != "" {
				c = s.clipRect(v, pl)
				break
			}
		}
	default:
		c = noClip
		for p := s.parent(v, ni); p >= 0; p = s.parent(v, p) {
			if pl, ok := v.layoutOf[p]; ok {
				c = s.clipRect(v, pl)
				break
			}
		}
	}
	v.clipOfCache[li] = c
	return c
}

// clipRect is the clip that applies to a node's content: its own overflow
// box within the clip on itself.
func (s *snapshot) clipRect(v *snapDocView, li int) snapRect {
	if c, ok := v.clipCache[li]; ok {
		return c
	}
	v.clipCache[li] = noClip // break cycles
	c := s.clipOf(v, li)
	ox, oy := s.style(v.d, li, stOverflowX), s.style(v.d, li, stOverflowY)
	if (ox != "visible" && ox != "") || (oy != "visible" && oy != "") {
		c = c.intersect(s.rect(v.d.Layout.Bounds[li], v.ox, v.oy))
	}
	v.clipCache[li] = c
	return c
}

// linkFor finds the enclosing anchor or button of a node, with an id that
// tells them apart within one capture.
func (s *snapshot) linkFor(v *snapDocView, ni int64) *snapLink {
	if l, ok := v.linkOf[ni]; ok {
		return l
	}
	var res *snapLink
	for p := ni; p >= 0; p = s.parent(v, p) {
		tag := s.tagOf(v, p)
		href, hasHref := s.attr(v, p, "href")
		role, _ := s.attr(v, p, "role")
		typ, _ := s.attr(v, p, "type")
		isButton := buttonTags[tag] || role == "button" ||
			(tag == "INPUT" && (typ == "submit" || typ == "button" || typ == "reset"))
		if tag == "A" && hasHref {
			abs := href
			if v.base != nil {
				if u, err := url.Parse(href); err == nil {
					abs = v.base.ResolveReference(u).String()
				}
			}
			res = &snapLink{id: s.nextLinkID(), href: abs}
			break
		}
		if isButton {
			res = &snapLink{id: s.nextLinkID(), button: true}
			break
		}
	}
	// Share one id per element: cache the result on every node walked.
	for p := ni; p >= 0; p = s.parent(v, p) {
		if _, ok := v.linkOf[p]; ok {
			break
		}
		v.linkOf[p] = res
		if res == nil {
			continue
		}
	}
	return res
}

func (s *snapshot) nextLinkID() int {
	s.nextID++
	return s.nextID
}

func (s *snapshot) markOccluder(box snapRect, paint int64) {
	cw, ch := s.cellW(), s.cellH()
	c0 := int(math.Floor(box.l / cw))
	c1 := int(math.Ceil(box.r / cw))
	r0 := int(math.Floor(box.t / ch))
	r1 := int(math.Ceil(box.b / ch))
	if c0 < 0 {
		c0 = 0
	}
	if r0 < 0 {
		r0 = 0
	}
	if c1 > s.g.Cols {
		c1 = s.g.Cols
	}
	if r1 > s.g.Rows {
		r1 = s.g.Rows
	}
	for row := r0; row < r1; row++ {
		cy := (float64(row) + 0.5) * ch
		for col := c0; col < c1; col++ {
			cx := (float64(col) + 0.5) * cw
			if !box.contains(cx, cy) {
				continue
			}
			i := row*s.g.Cols + col
			if paint > s.occl[i] {
				s.occl[i] = paint
			}
		}
	}
}

// textBox turns one post-layout text box into cells.
func (s *snapshot) textBox(v *snapDocView, i int) {
	tb := v.d.TextBoxes
	li := int(tb.LayoutIndex[i])
	L := v.d.Layout
	if li < 0 || li >= len(L.NodeIndex) || !s.visible(v, li) {
		return
	}
	u16, ok := v.utf16[li]
	if !ok {
		u16 = utf16.Encode([]rune(s.str(L.Text[li])))
		v.utf16[li] = u16
	}
	start, length := int(tb.Start[i]), int(tb.Length[i])
	if start < 0 || length <= 0 || start+length > len(u16) {
		return
	}
	text := utf16.Decode(u16[start : start+length])
	box := s.rect(tb.Bounds[i], v.ox, v.oy)
	// Clip by the parent element's content clip; the text node's own
	// styles are copies of its parent's.
	clip := noClip
	for p := s.parent(v, L.NodeIndex[li]); p >= 0; p = s.parent(v, p) {
		if pl, ok := v.layoutOf[p]; ok {
			clip = s.clipRect(v, pl)
			break
		}
	}
	cw, ch := s.cellW(), s.cellH()
	row := int(math.Floor((box.t + (box.b-box.t)/2) / ch))
	col0 := int(math.Round(box.l / cw))
	paint := s.paint(L, li)
	if row < 0 || row >= s.g.Rows {
		return
	}
	link := s.linkFor(v, L.NodeIndex[li])
	for k, r := range text {
		col := col0 + k
		if col < 0 || col >= s.g.Cols {
			continue
		}
		// Test the clip where the character actually is, not at the centre
		// of the cell it lands in: a run rarely starts on a cell boundary,
		// and sampling the cell instead would keep or drop a different
		// last character than measuring the glyph does.
		x := box.l + (float64(k)+0.5)*cw
		y := box.t + (box.b-box.t)/2
		if !clip.contains(x, y) {
			continue
		}
		s.put(col, row, r, paint, link)
	}
}

// field renders the visible text of a form control.
func (s *snapshot) field(v *snapDocView, li int, ni int64) {
	if !s.visible(v, li) {
		return
	}
	tag := s.tagOf(v, ni)
	typ, _ := s.attr(v, ni, "type")
	switch typ {
	case "hidden", "checkbox", "radio", "file", "image", "range", "color":
		return
	case "submit", "button", "reset":
		s.buttonInput(v, li, ni)
		return
	}
	var text string
	switch tag {
	case "INPUT":
		text = s.rareString(v.d.Nodes.InputValue, ni)
	case "TEXTAREA":
		text = s.rareString(v.d.Nodes.TextValue, ni)
	case "SELECT":
		text = s.selectedOption(v, ni)
	}
	if text == "" {
		text, _ = s.attr(v, ni, "placeholder")
	}
	if typ == "password" {
		text = strings.Repeat("*", len([]rune(text)))
	}
	box := s.rect(v.d.Layout.Bounds[li], v.ox, v.oy)
	if box.r <= box.l {
		return
	}
	cw, ch := s.cellW(), s.cellH()
	// Text inside a control starts at its content box, not its border box.
	padLeft := s.px(v, li, stPaddingLeft) + s.px(v, li, stBorderLeft)
	row := int(math.Floor((box.t + (box.b-box.t)/2) / ch))
	if tag == "TEXTAREA" {
		row = int(math.Floor((box.t + s.px(v, li, stPaddingTop) + s.px(v, li, stBorderTop) + ch/2) / ch))
	}
	col0 := int(math.Round((box.l + padLeft) / cw))
	if row < 0 || row >= s.g.Rows {
		return
	}
	paint := s.paint(v.d.Layout, li)
	max := int((box.r - box.l - padLeft) / cw)
	line := strings.SplitN(text, "\n", 2)[0]
	for k, r := range []rune(line) {
		if k >= max {
			break
		}
		col := col0 + k
		if col < 0 || col >= s.g.Cols {
			continue
		}
		s.put(col, row, r, paint, nil)
	}
	// A field with no text still needs a cell so Tab can reach it; the
	// renderer draws nothing for a run whose text is empty.
	if line == "" {
		s.putField(col0, row, max)
	}
}

// buttonInput places the label of an <input type=submit>, centered.
func (s *snapshot) buttonInput(v *snapDocView, li int, ni int64) {
	label := s.rareString(v.d.Nodes.InputValue, ni)
	if label == "" {
		if a, ok := s.attr(v, ni, "value"); ok {
			label = a
		} else {
			label = "Submit"
		}
	}
	box := s.rect(v.d.Layout.Bounds[li], v.ox, v.oy)
	cw, ch := s.cellW(), s.cellH()
	runes := []rune(label)
	row := int(math.Floor((box.t + (box.b-box.t)/2) / ch))
	col0 := int(math.Round((box.l + (box.r-box.l-float64(len(runes))*cw)/2) / cw))
	if row < 0 || row >= s.g.Rows {
		return
	}
	link := &snapLink{id: s.nextLinkID(), button: true}
	paint := s.paint(v.d.Layout, li)
	for k, r := range runes {
		col := col0 + k
		if col >= 0 && col < s.g.Cols {
			s.put(col, row, r, paint, link)
		}
	}
}

func (s *snapshot) rareString(r *domsnapshot.RareStringData, ni int64) string {
	if r == nil {
		return ""
	}
	for k, idx := range r.Index {
		if idx == ni && k < len(r.Value) {
			return s.str(r.Value[k])
		}
	}
	return ""
}

func (s *snapshot) rareBool(r *domsnapshot.RareBooleanData, ni int64) bool {
	if r == nil {
		return false
	}
	for _, idx := range r.Index {
		if idx == ni {
			return true
		}
	}
	return false
}

// selectedOption returns the text of the selected option of a <select>.
func (s *snapshot) selectedOption(v *snapDocView, ni int64) string {
	n := v.d.Nodes
	for i := range n.NodeType {
		if s.tagOf(v, int64(i)) != "OPTION" || !s.rareBool(n.OptionSelected, int64(i)) {
			continue
		}
		if !s.isAncestor(v, ni, int64(i)) {
			continue
		}
		for j := range n.NodeType {
			if n.ParentIndex[j] == int64(i) && n.NodeType[j] == 3 {
				return strings.TrimSpace(s.str(n.NodeValue[j]))
			}
		}
	}
	return ""
}

func (s *snapshot) isAncestor(v *snapDocView, anc, ni int64) bool {
	for p := ni; p >= 0; p = s.parent(v, p) {
		if p == anc {
			return true
		}
	}
	return false
}

// put writes one character cell, the highest paint order winning.
func (s *snapshot) put(col, row int, r rune, paint int64, link *snapLink) {
	if col < 0 || row < 0 || col >= s.g.Cols || row >= s.g.Rows {
		return
	}
	i := row*s.g.Cols + col
	if old, ok := s.cells[i]; ok && old.paint > paint {
		return
	}
	s.cells[i] = snapCell{r: r, paint: paint, link: link}
}

// fieldMark records an empty form field so Tab can reach it.
type fieldMark struct{ col, row, w int }

func (s *snapshot) putField(col, row, w int) {
	s.fields = append(s.fields, fieldMark{col, row, w})
}

// runs groups the winning cells into text runs, dropping those covered by
// a box with a higher paint order.
func (s *snapshot) runs() []frame.TextRun {
	var out []frame.TextRun
	for row := 0; row < s.g.Rows; row++ {
		var cur *frame.TextRun
		var curLink *snapLink
		for col := 0; col < s.g.Cols; col++ {
			i := row*s.g.Cols + col
			c, ok := s.cells[i]
			if ok && s.occl[i] > c.paint {
				ok = false // painted over
			}
			if !ok {
				if cur != nil {
					out = append(out, *cur)
					cur = nil
				}
				continue
			}
			if cur == nil || c.link != curLink {
				if cur != nil {
					out = append(out, *cur)
				}
				cur = &frame.TextRun{X: col, Y: row, H: 1, Paint: c.paint}
				curLink = c.link
				if c.link != nil {
					cur.Link = !c.link.button
					cur.Button = c.link.button
					cur.Href = c.link.href
					cur.LinkID = c.link.id
				}
			}
			cur.Text += string(c.r)
			cur.W = col - cur.X + 1
		}
		if cur != nil {
			out = append(out, *cur)
		}
	}
	for _, f := range s.fields {
		out = append(out, frame.TextRun{X: f.col, Y: f.row, W: f.w, H: 1, Field: true})
	}
	return out
}

// ---- the interface layer

// cellBox converts a rect in CSS pixels to whole cells, rounding outward
// so a one pixel border still gets a line.
func (s *snapshot) cellBox(r snapRect) (x, y, w, h int) {
	cw, ch := s.cellW(), s.cellH()
	x0 := int(math.Round(r.l / cw))
	y0 := int(math.Round(r.t / ch))
	x1 := int(math.Round(r.r / cw))
	y1 := int(math.Round(r.b / ch))
	if x1 <= x0 {
		x1 = x0 + 1
	}
	if y1 <= y0 {
		y1 = y0 + 1
	}
	return x0, y0, x1 - x0, y1 - y0
}

// onGrid reports whether a cell box intersects the grid at all.
func (s *snapshot) onGrid(x, y, w, h int) bool {
	return x < s.g.Cols && y < s.g.Rows && x+w > 0 && y+h > 0
}

// hasBorder reports whether any side of a node draws a line. Chrome
// computes a zero width for border-style none, so a positive width is
// already a visible border; a fully transparent color is not.
func (s *snapshot) hasBorder(v *snapDocView, li int) bool {
	sides := [][2]int{
		{stBorderTop, stBorderTopColor}, {stBorderRight, stBorderRightColor},
		{stBorderBottom, stBorderBottomColor}, {stBorderLeft, stBorderLeftColor},
	}
	for _, sd := range sides {
		if s.px(v, li, sd[0]) < 1 {
			continue
		}
		if _, a := parseCSSColor(s.style(v.d, li, sd[1])); a > 0 {
			return true
		}
	}
	return false
}

// ownBackground reports whether a node's background differs from the one
// it sits on, which is what makes it read as a container of its own.
func (s *snapshot) ownBackground(v *snapDocView, li int) bool {
	luma, alpha := parseCSSColor(s.style(v.d, li, stBackground))
	if alpha == 0 {
		return false
	}
	ni := v.d.Layout.NodeIndex[li]
	for p := s.parent(v, ni); p >= 0; p = s.parent(v, p) {
		pl, ok := v.layoutOf[p]
		if !ok {
			continue
		}
		pl2, pa := parseCSSColor(s.style(v.d, pl, stBackground))
		if pa == 0 {
			continue
		}
		return math.Abs(luma-pl2) > 0.04
	}
	return luma < 0.96 // against the default white page
}

// visibleKids counts the element children of a node that have a box.
func (s *snapshot) visibleKids(v *snapDocView, ni int64) int {
	if n, ok := v.kids[ni]; ok {
		return n
	}
	n := 0
	for i, p := range v.d.Nodes.ParentIndex {
		if p != ni || v.d.Nodes.NodeType[i] != 1 {
			continue
		}
		if li, ok := v.layoutOf[int64(i)]; ok && s.visible(v, li) {
			n++
		}
	}
	v.kids[ni] = n
	return n
}

// frameCollapses reports whether a frame should be left to an ancestor:
// their boxes coincide within one cell per side, or the ancestor holds
// nothing else, so two nested outlines would say the same thing twice.
func (s *snapshot) frameCollapses(v *snapDocView, li int, x, y, w, h int) bool {
	ni := v.d.Layout.NodeIndex[li]
	for p := s.parent(v, ni); p >= 0; p = s.parent(v, p) {
		pl, ok := v.layoutOf[p]
		if !ok || !s.visible(v, pl) {
			continue
		}
		if !s.hasBorder(v, pl) && !s.ownBackground(v, pl) {
			continue // not a frame itself: keep looking up
		}
		px, py, pw, ph := s.cellBox(s.rect(v.d.Layout.Bounds[pl], v.ox, v.oy))
		near := abs(px-x) <= 1 && abs(py-y) <= 1 && abs(px+pw-x-w) <= 1 && abs(py+ph-y-h) <= 1
		if near || s.visibleKids(v, p) <= 1 {
			return true
		}
		return false // an outer frame that genuinely holds more than this
	}
	return false
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// uiNode turns one layout node into interface boxes.
func (s *snapshot) uiNode(v *snapDocView, li int, tag string) {
	L := v.d.Layout
	ni := L.NodeIndex[li]
	box := s.rect(L.Bounds[li], v.ox, v.oy).intersect(s.clipOf(v, li))
	if box.r <= box.l || box.b <= box.t {
		return
	}
	x, y, w, h := s.cellBox(box)
	if !s.onGrid(x, y, w, h) {
		return
	}
	paint := s.paint(L, li)
	typ, _ := s.attr(v, ni, "type")
	// A control shrunk to a pixel or two is a hidden one behind a styled
	// label, not something to draw.
	tiny := box.r-box.l < 8 || box.b-box.t < 8
	// A widget is drawn on the row its own text lands on, so brackets and
	// value never end up on different lines.
	mid := int(math.Floor((box.t + (box.b-box.t)/2) / s.cellH()))
	widget := func(kind frame.UIKind, on bool) {
		s.boxes = append(s.boxes, frame.UIBox{Kind: kind, X: x, Y: mid, W: w, H: 1, On: on, Paint: paint})
	}

	switch {
	case mediaTags[tag]:
		alt, _ := s.attr(v, ni, "alt")
		if alt == "" {
			alt, _ = s.attr(v, ni, "aria-label")
		}
		if alt == "" {
			alt, _ = s.attr(v, ni, "title")
		}
		label := strings.TrimSpace(tag + " " + strings.TrimSpace(alt))
		isVideo := tag == "VIDEO"
		s.boxes = append(s.boxes, frame.UIBox{Kind: frame.UIMedia, X: x, Y: y, W: w, H: h,
			Text: label, Paint: paint, Video: isVideo})
		if isVideo && s.videoOff {
			return // --video off: the placeholder is all a video gets
		}
		// The crop fills the inside of the frame, so it is photographed
		// at the pixels those cells will average over.
		if s.media && w > 2 && h > 2 {
			s.shots = append(s.shots, mediaShot{
				box: len(s.boxes) - 1,
				x:   x * s.g.CellW, y: y * s.g.CellH,
				w: w * s.g.CellW, h: h * s.g.CellH,
			})
		}
		return
	case tag == "INPUT":
		switch typ {
		case "checkbox":
			if tiny {
				return
			}
			widget(frame.UICheckbox, s.rareBool(v.d.Nodes.InputChecked, ni))
			return
		case "radio":
			if tiny {
				return
			}
			widget(frame.UIRadio, s.rareBool(v.d.Nodes.InputChecked, ni))
			return
		case "submit", "button", "reset":
			widget(frame.UIButton, false)
			return
		case "hidden":
			return
		default:
			if tiny {
				return
			}
			widget(frame.UIField, false)
			return
		}
	case tag == "TEXTAREA":
		if tiny {
			return
		}
		s.boxes = append(s.boxes, frame.UIBox{Kind: frame.UIField, X: x, Y: y, W: w, H: h, Paint: paint})
		return
	case tag == "SELECT":
		if tiny {
			return
		}
		widget(frame.UISelect, false)
		return
	case tag == "BUTTON":
		if tiny {
			return
		}
		widget(frame.UIButton, false)
		return
	case len(tag) == 2 && tag[0] == 'H' && tag[1] >= '1' && tag[1] <= '6':
		s.rules = append(s.rules, pendingRule{col: x, row: y + h, maxCol: x + w, level: int(tag[1] - '0'), paint: paint})
		return
	case tag == "LI":
		// A list that hides its markers is being used for layout.
		if st := s.style(v.d, li, stListStyle); st != "none" && st != "" {
			s.marker(v, ni, x, y, paint)
		}
		return
	}

	// Anything else is a container: it gets a frame when it draws a
	// border or carries a background of its own.
	if tag == "HTML" || tag == "BODY" || w < 2 || h < 2 {
		return
	}
	if !s.hasBorder(v, li) && !s.ownBackground(v, li) {
		return
	}
	if s.frameCollapses(v, li, x, y, w, h) {
		return
	}
	s.boxes = append(s.boxes, frame.UIBox{Kind: frame.UIFrame, X: x, Y: y, W: w, H: h, Paint: paint})
}

// marker places the bullet or number of a list item.
func (s *snapshot) marker(v *snapDocView, ni int64, x, y int, paint int64) {
	parent := s.parent(v, ni)
	ordered := s.tagOf(v, parent) == "OL"
	text := "*"
	if ordered {
		n := 1
		for i, p := range v.d.Nodes.ParentIndex {
			if p == parent && int64(i) < ni && s.tagOf(v, int64(i)) == "LI" {
				n++
			}
		}
		text = strconv.Itoa(n) + "."
	}
	col := x - len(text) - 1
	if col < 0 {
		col = 0
	}
	s.boxes = append(s.boxes, frame.UIBox{Kind: frame.UIMarker, X: col, Y: y, W: len(text), H: 1, Text: text, Paint: paint})
}

// closeRules sizes each heading underline to the text actually laid out
// inside the heading, then emits it.
func (s *snapshot) closeRules() {
	for _, r := range s.rules {
		row := r.row - 1
		if row < 0 || row >= s.g.Rows {
			continue
		}
		last := -1
		for col := r.col; col < r.maxCol && col < s.g.Cols; col++ {
			if col < 0 {
				continue
			}
			if c, ok := s.cells[row*s.g.Cols+col]; ok && c.r != ' ' {
				last = col
			}
		}
		if last < r.col {
			continue
		}
		s.boxes = append(s.boxes, frame.UIBox{Kind: frame.UIRule, X: r.col, Y: r.row,
			W: last - r.col + 1, H: 1, Level: r.level, Paint: r.paint})
	}
}
