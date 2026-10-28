package main

import (
	"context"
	"fmt"
	"image"
	"math"
	"os"
	"time"

	"github.com/chromedp/chromedp/kb"
	"github.com/gdamore/tcell/v2"

	"github.com/eschero/ascii-browser/internal/browser"
	"github.com/eschero/ascii-browser/internal/frame"
	"github.com/eschero/ascii-browser/internal/grid"
	"github.com/eschero/ascii-browser/internal/render"
	"github.com/eschero/ascii-browser/internal/ui"
)

// Terminal layout: row 0 is the URL bar, the last row is the status line,
// the page fills the rows in between.
const (
	barRow     = 0
	pageTop    = 1
	chromeRows = 2
	scrollStep = 3 // rows per arrow key or wheel notch
	// coalesceWindow: scroll inputs closer than this are summed into one move.
	coalesceWindow = 30 * time.Millisecond
	// Screencast pacing. The next frame is processed no sooner than
	// minFrameGap after the previous one for a one-off repaint (a banner, a
	// late image), doubling up to maxFrameGap while frames keep coming back
	// to back (an animated GIF), so a page that never rests costs at most
	// one capture per second. Any input or a quiet moment resets the gap.
	minFrameGap = 80 * time.Millisecond
	maxFrameGap = time.Second
	// maxIdleGap: a page that keeps repainting while nothing on screen
	// changes, a spinner, a ticker, a marquee, is read ever less often, up
	// to this; quietCells is how few changed cells count as nothing.
	maxIdleGap = 8 * time.Second
	quietCells = 8
	// captureMargin: a screencast frame swapped up to this long after a
	// capture finished is taken as the capture's own composite, not as a
	// new repaint. Covers clock skew between Chrome and this process.
	captureMargin = 50 * time.Millisecond
)

// tui is the interactive session.
//
// Scrolling model: the Chromium viewport, hence the rendered frame, is
// cfg.buffer screens tall (the frame buffer). The terminal shows one
// screen of it, at page row viewTop. Scrolling moves that window over the
// frame in memory, instantly and grid-aligned; only when less than half a
// screen of buffer remains in the scroll direction is the page scrolled
// and captured again, with the buffer placed mostly ahead of the view.
// Captures run in the background, never more than one at a time; an input
// that changes the page while one is in flight discards its result and
// starts a fresh one on the final state.
type tui struct {
	cfg config
	scr *ui.Screen
	b   *browser.Browser
	r   *render.Renderer
	log *os.File

	cols, rows int       // terminal size
	g          grid.Grid // frame grid: cfg.buffer screens of viewRows

	viewTop      int // page row at the top of the terminal window
	bufTop       int // page row at the top of the frame on screen
	targetBufTop int // page row requested for the next capture
	wx           int // horizontal window offset into the frame

	frame *frame.Frame
	runs  []frame.TextRun
	state browser.PageState
	items []focusable // links and fields inside the window, reading order
	cur   int         // index into items, -1 for none
	key   string      // key of the current item, to find it again

	// what is on the terminal now, to redraw only what changed
	shown            *frame.Frame
	shownHL          []bool
	shownWx, shownWy int

	bar       ui.URLBar
	barFocus  bool
	msg       string // transient status message, shown until the next action
	mouseDown bool

	// background capture
	captureCh     chan captureResult
	consentCh     chan []string // a consent banner answered in the background
	capturing     bool
	captureWanted bool      // an input changed the page while a capture was in flight: discard it
	frameDuring   time.Time // newest screencast frame seen while a capture was in flight
	syncPending   bool      // the view moved inside the buffer and the page has yet to follow
	docText       int       // characters of text the document had after the last navigation, -1 unknown
	fullscreen    bool      // the page has an element fullscreen: the viewport is one screen
	emptyRetries  int       // recaptures spent on a capture that came back without its text
	lastDir       int       // direction of the last scroll, to place the buffer ahead of it
	followedView  int       // where the view was when the page was last placed under it
	pageRow       int       // where the page's own scroll is, in rows, as far as we know
	captureEnd    time.Time // screencast frames older than this are on screen

	// coalesced scroll input
	pendingScroll int
	pendingAbs    int // -1 none, else absolute row target (Home/End)

	// screencast pacing
	lastFrameDone time.Time
	frameGap      time.Duration
	pending       *browser.ScreencastFrame
	held          bool

	// video, decoupled from the rest of the page: the picture is turned
	// into characters inside the page and only those cells are redrawn.
	videos          []frame.UIBox // video boxes from the last capture
	ownerPaint      []int64       // per cell, the paint order of whatever drew it
	prevShot        image.Image   // the last screencast image, to see what moved
	videoN          int           // frames drawn
	videoDraw       time.Duration // time spent drawing them
	videoSince      time.Time
	tainted         []frame.UIBox // videos the page cannot read: Go photographs these
	taintedN        int           // frames drawn along that path
	loggedVideo     map[[2]int]bool
	lastVideoStatus string
}

// syncDelay is how long the scrolling has to pause before the page is
// moved to follow the view.
const syncDelay = 150 * time.Millisecond

type captureResult struct {
	page  browser.Page
	err   error
	start time.Time
}

func runTUI(ctx context.Context, cfg config) int {
	scr, err := ui.Open()
	if err != nil {
		return fail("terminal: %v", err)
	}
	defer scr.Close()

	t := &tui{cfg: cfg, scr: scr, r: render.New(cfg.render), cur: -1, pendingAbs: -1, captureCh: make(chan captureResult, 1), consentCh: make(chan []string, 1)}
	if cfg.logPath != "" {
		if t.log, err = os.OpenFile(cfg.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err != nil {
			scr.Close()
			return fail("--log: %v", err)
		}
		defer t.log.Close()
	}
	if cfg.note != "" {
		t.logf("%s", cfg.note)
	}
	t.cols, t.rows = scr.Size()
	t.g = t.frameGrid()
	scr.DrawBar(barRow, "starting chrome...", t.cols, -1)
	scr.Show()

	t.b, err = newBrowser(ctx, cfg, t.g, t.viewRows())
	if err != nil {
		scr.Close()
		return fail("%v", err)
	}
	defer t.b.Close()

	if err := t.b.Navigate(cfg.url); err != nil {
		t.msg = err.Error()
	}
	t.waitBuilt()
	t.requestCapture()
	frames, err := t.b.StartScreencast()
	if err != nil {
		t.msg = "screencast: " + err.Error()
		t.draw()
	}

	var videoFrames <-chan browser.VideoFrame
	if cfg.video != "off" {
		videoFrames = t.b.VideoFrames()
		t.startVideo()
	}
	var gapTimer, scrollTimer, taintTimer, syncTimer <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return 0
		case res := <-t.captureCh:
			t.capturing = false
			if t.captureWanted {
				t.logf("capture discarded: input arrived during it (%v)", time.Since(res.start).Round(time.Millisecond))
				t.requestCapture()
				continue
			}
			t.applyCapture(res)
			// With no screenshot to date from, a repaint counts only if it
			// came after the whole capture finished.
			ref := res.start.Add(res.page.Stats.Screenshot + captureMargin)
			if res.page.Stats.Screenshot == 0 {
				ref = t.captureEnd
			}
			if res.err == nil && t.frameDuring.After(ref) {
				// The page repainted well after the screenshot: once more.
				t.logf("page repainted %v after the capture: capturing again", t.frameDuring.Sub(ref).Round(time.Millisecond))
				t.requestCapture()
			}
		case <-scrollTimer:
			scrollTimer = nil
			t.flushScroll()
			if t.syncPending && syncTimer == nil {
				syncTimer = time.After(syncDelay)
			}
		case <-syncTimer:
			syncTimer = nil
			t.syncScroll()
			if t.syncPending {
				syncTimer = time.After(syncDelay) // a capture was in flight: try again after it
			}
		case got := <-t.consentCh:
			// The banner was answered while the page was already on
			// screen: read the page again without it.
			t.logf("consent: clicked %q", got)
			t.emptyRetries = 0
			t.requestCapture()
		case vf := <-videoFrames:
			if vf.Tainted {
				t.addTaintedVideo(vf)
				if taintTimer == nil {
					taintTimer = time.After(t.taintedInterval())
				}
				continue
			}
			t.drawVideoFrame(vf)
		case <-taintTimer:
			taintTimer = nil
			t.refreshTaintedVideos()
			if len(t.tainted) > 0 {
				taintTimer = time.After(t.taintedInterval())
			}
		case f := <-frames:
			if t.pending != nil {
				f.Supersede(t.pending)
			}
			t.pending = f
			if wait := t.frameGap - time.Since(t.lastFrameDone); wait > 0 {
				t.held = true
				if gapTimer == nil {
					gapTimer = time.After(wait)
				}
			} else {
				t.processPending()
			}
		case <-gapTimer:
			gapTimer = nil
			if t.pending != nil {
				t.processPending()
			}
		case ev, ok := <-scr.Events():
			if !ok {
				return 0
			}
			quit, scrolled := t.handle(ev)
			if quit {
				return 0
			}
			if scrolled && scrollTimer == nil {
				scrollTimer = time.After(coalesceWindow)
			}
		}
	}
}

func (t *tui) logf(format string, args ...interface{}) {
	if t.log != nil {
		fmt.Fprintf(t.log, time.Now().Format("15:04:05.000 ")+format+"\n", args...)
	}
}

func pageRows(rows int) int {
	if rows <= chromeRows {
		return 1
	}
	return rows - chromeRows
}

// viewRows is one screen of the page: --size rows if given, else the
// terminal's page area.
func (t *tui) viewRows() int {
	if t.cfg.rows > 0 {
		return t.cfg.rows
	}
	return pageRows(t.rows)
}

// frameGrid derives the frame size: --size fixes a dimension, 0 follows
// the terminal; the height is cfg.buffer screens.
func (t *tui) frameGrid() grid.Grid {
	cols := t.cfg.cols
	if cols == 0 {
		cols = t.cols
	}
	// Fullscreen fills Chrome's viewport, so the viewport has to be the
	// terminal: with the buffer several screens tall the player would be
	// centred far below the view.
	if t.fullscreen {
		return grid.New(cols, t.viewRows())
	}
	return grid.New(cols, t.viewRows()*t.cfg.buffer)
}

// followFullscreen resizes the viewport when the page enters or leaves
// fullscreen, and reports whether it did.
func (t *tui) followFullscreen(on bool) bool {
	if on == t.fullscreen {
		return false
	}
	t.fullscreen = on
	g := t.frameGrid()
	if g == t.g {
		return false
	}
	t.g = g
	if err := t.b.Resize(t.g); err != nil {
		t.msg = "resize: " + err.Error()
	}
	if on {
		t.viewTop, t.bufTop, t.targetBufTop, t.pageRow = 0, 0, 0, 0
	}
	t.logf("fullscreen %v: viewport %dx%d", on, t.g.Cols, t.g.Rows)
	t.requestCapture()
	return true
}

// window is the part of the frame shown in the terminal.
func (t *tui) window() (w, h int) {
	w, h = t.cols, pageRows(t.rows)
	if w > t.g.Cols {
		w = t.g.Cols
	}
	if h > t.g.Rows {
		h = t.g.Rows
	}
	return w, h
}

// docRows is the document height in grid rows, from the last state.
func (t *tui) docRows() int {
	return int(math.Ceil(t.state.ScrollHeight * t.b.Zoom() / float64(t.g.CellH)))
}

// clampWindow keeps a w x h window inside a frame of fw x fh cells.
func clampWindow(x, y, w, h, fw, fh int) (int, int) {
	if x > fw-w {
		x = fw - w
	}
	if y > fh-h {
		y = fh - h
	}
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return x, y
}

// ---- background capture

// requestCapture starts a capture unless one is in flight, in which case
// the flight's result will be discarded and a new capture started.
func (t *tui) requestCapture() { t.requestCaptureAfter(0) }

// requestCaptureAfter captures after a delay, holding the capture slot
// meanwhile so nothing else starts one.
func (t *tui) requestCaptureAfter(d time.Duration) {
	if t.capturing {
		t.captureWanted = true
		return
	}
	t.capturing, t.captureWanted, t.frameDuring = true, false, time.Time{}
	go func() {
		if d > 0 {
			time.Sleep(d)
		}
		start := time.Now()
		page, err := t.b.Capture(!t.cfg.noText)
		t.captureCh <- captureResult{page, err, start}
	}()
}

// applyCapture renders a finished capture and places the view in it.
func (t *tui) applyCapture(res captureResult) {
	if res.err != nil {
		t.msg = "capture: " + res.err.Error()
		t.draw()
		return
	}
	t.captureEnd = time.Now().Add(captureMargin)
	prevURL := t.state.URL
	prev := t.frame
	t0 := time.Now()
	if inv := t.cfg.invertFor(res.page.State); inv != t.r.Inverted() {
		t.r.SetInvert(inv)
		prev = nil // every cell changes: redraw the whole screen
		if len(t.videos) > 0 || t.cfg.video != "off" {
			defer t.startVideo() // the page draws its videos with the mapping too
		}
	}
	if t.cfg.renderer == "ui" {
		t.frame = t.r.RenderUI(res.page.UI, t.g, res.page.Runs)
	} else {
		t.frame = t.r.Render(res.page.Img, t.g, res.page.Runs)
	}
	tRender := time.Since(t0)
	t.runs, t.state = res.page.Runs, res.page.State
	if t.followFullscreen(res.page.State.Fullscreen) {
		return // the viewport changed size: the capture that follows redraws
	}
	t.videos = t.videos[:0]
	for _, b := range res.page.UI {
		if b.Video {
			t.videos = append(t.videos, b)
		}
	}
	// The tonal renderers take no layout snapshot, so the videos come from
	// the page state instead. Their text runs carry no paint order: the
	// video is placed below all of them, so text over a video, a player's
	// controls, a caption, stays on top of the moving picture.
	if len(res.page.UI) == 0 {
		z := t.b.Zoom()
		for _, v := range res.page.State.Videos {
			x0 := int(math.Round(v.L * z / float64(t.g.CellW)))
			y0 := int(math.Round(v.T * z / float64(t.g.CellH)))
			x1 := int(math.Round((v.L + v.W) * z / float64(t.g.CellW)))
			y1 := int(math.Round((v.T + v.H) * z / float64(t.g.CellH)))
			if x1-x0 < 2 || y1-y0 < 2 {
				continue
			}
			t.videos = append(t.videos, frame.UIBox{Kind: frame.UIMedia, X: x0, Y: y0, W: x1 - x0, H: y1 - y0, Video: true, Paint: -1})
		}
	}
	t.ownerPaint = ownerPaint(t.g, res.page.UI, res.page.Runs, t.ownerPaint)
	if len(t.videos) > 0 && t.cfg.video != "off" {
		t.startVideo()
	}
	// Where the page actually is: it may have scrolled on its own.
	t.bufTop = int(math.Round(res.page.State.ScrollY * t.b.Zoom() / float64(t.g.CellH)))
	t.pageRow, t.followedView = t.bufTop, t.viewTop
	if t.targetBufTop != t.bufTop && !t.capturing {
		t.targetBufTop = t.bufTop
	}
	_, winH := t.window()
	if res.page.State.URL != prevURL || t.viewTop < t.bufTop || t.viewTop+winH > t.bufTop+t.g.Rows {
		t.viewTop = t.bufTop
		t.targetBufTop = t.bufTop
	}
	if res.page.State.URL != prevURL {
		t.cur, t.key = -1, ""
	}
	t.refreshItems()
	t0 = time.Now()
	changed := t.drawUpdate(prev)
	// Text is invisible in the screenshot by design, so no repaint will
	// ever say that a capture missed it: one taken while the page was
	// replacing itself is read again, twice at most.
	if res.page.State.URL != prevURL {
		t.emptyRetries = 0
	}
	if !t.cfg.noText && (t.docText >= minPageText || t.docText < 0) && runChars(res.page.Runs) < 40 && t.emptyRetries < 2 {
		t.emptyRetries++
		t.logf("capture came back without its text: reading the page again")
		defer t.requestCaptureAfter(700 * time.Millisecond)
	}
	// A capture that changed nothing on screen came from a page that
	// repaints without saying anything new: wait longer before the next.
	// One that changed something resets the pace.
	if changed >= 0 && changed < quietCells && res.page.State.URL == prevURL {
		t.frameGap *= 2
		if t.frameGap > maxIdleGap {
			t.frameGap = maxIdleGap
		}
		if t.frameGap < minFrameGap {
			t.frameGap = minFrameGap
		}
	} else {
		t.frameGap = minFrameGap
	}
	t.logf("capture applied: screenshot %v text %v render %v draw %v, in flight %v, buffer rows %d-%d, view %d, next gap %v",
		res.page.Stats.Screenshot.Round(time.Millisecond), res.page.Stats.Walker.Round(time.Millisecond),
		tRender.Round(time.Millisecond), time.Since(t0).Round(time.Millisecond),
		time.Since(res.start).Round(time.Millisecond), t.bufTop, t.bufTop+t.g.Rows, t.viewTop, t.frameGap)
}

// ---- scrolling within and beyond the buffer

// queueScroll records a relative scroll; queueAbs an absolute row target.
// They are applied together at the end of the coalescing window.
func (t *tui) queueScroll(rows int) { t.pendingScroll += rows }
func (t *tui) queueAbs(row int)     { t.pendingAbs, t.pendingScroll = row, 0 }

func (t *tui) flushScroll() {
	d, abs := t.pendingScroll, t.pendingAbs
	t.pendingScroll, t.pendingAbs = 0, -1
	_, winH := t.window()
	maxTop := t.docRows() - winH
	if maxTop < 0 {
		maxTop = 0
	}
	target := t.viewTop + d
	if abs >= 0 {
		target = abs
	}
	if target < 0 {
		target = 0
	}
	if target > maxTop {
		target = maxTop
	}
	dir := 0
	switch {
	case target > t.viewTop:
		dir = 1
	case target < t.viewTop:
		dir = -1
	}
	if dir == 0 {
		return
	}
	t0 := time.Now()
	t.viewTop = target
	t.refreshItems()
	t.draw()
	t.logf("scroll %+d -> view row %d (draw %v)", d, t.viewTop, time.Since(t0).Round(time.Millisecond))
	t.lastDir = dir
	if !t.recenter(dir) {
		t.syncPending = true // the page follows once the scrolling pauses
	}
}

// recenter scrolls the page and captures when the view is within half a
// screen of the buffer's edge in the scroll direction, placing the new
// buffer mostly ahead of the view. It reports whether it did.
func (t *tui) recenter(dir int) bool {
	_, winH := t.window()
	H := t.viewRows()
	rows := t.g.Rows
	doc := t.docRows()
	top := t.targetBufTop
	ahead := top + rows - (t.viewTop + winH)
	behind := t.viewTop - top
	need := t.viewTop < top || t.viewTop+winH > top+rows
	if dir > 0 && ahead < H/2 && top+rows < doc {
		need = true
	}
	if dir < 0 && behind < H/2 && top > 0 {
		need = true
	}
	if !need {
		return false
	}
	slack := rows - winH // rows of buffer not shown
	var keepBehind int
	switch {
	case dir > 0:
		keepBehind = H / 2
	case dir < 0:
		keepBehind = slack - H/2
	default:
		keepBehind = slack / 2
	}
	if keepBehind > slack {
		keepBehind = slack
	}
	if keepBehind < 0 {
		keepBehind = 0
	}
	newTop := t.viewTop - keepBehind
	if max := doc - rows; newTop > max {
		newTop = max
	}
	if newTop < 0 {
		newTop = 0
	}
	if newTop == top {
		return false
	}
	t.targetBufTop, t.pageRow, t.followedView = newTop, newTop, t.viewTop
	t.syncPending = false // this scroll supersedes any follow-up still waiting
	if err := t.b.ScrollToRow(newTop); err != nil {
		t.msg = "scroll: " + err.Error()
	}
	t.logf("recenter buffer -> rows %d-%d (dir %+d, view %d)", newTop, newTop+rows, dir, t.viewTop)
	t.requestCapture()
	return true
}

// refreshItems lists links and fields inside the window.
func (t *tui) refreshItems() {
	_, winH := t.window()
	wy := t.viewTop - t.bufTop
	var visible []frame.TextRun
	index := make([]int, 0, len(t.runs))
	for i, r := range t.runs {
		if r.Y >= wy && r.Y < wy+winH {
			visible = append(visible, r)
			index = append(index, i)
		}
	}
	t.items = collectFocusables(visible)
	for i := range t.items {
		for j, k := range t.items[i].runs {
			t.items[i].runs[j] = index[k] // back to indices into t.runs
		}
	}
	switch {
	case t.key == "":
		t.cur = -1
	default:
		if t.cur = findKey(t.items, t.key); t.cur < 0 && len(t.items) > 0 {
			t.cur = 0
			t.key = t.items[0].key
		}
	}
}

// moveWindow shifts the window horizontally by half its width.
func (t *tui) moveWindow(dx int) {
	w, _ := t.window()
	t.wx, _ = clampWindow(t.wx+dx*(w+1)/2, 0, w, 1, t.g.Cols, 1)
	t.refreshItems()
	t.draw()
}

// ---- screencast

// processPending handles the newest screencast frame: the page repainted
// on its own. The gap before the next frame doubles while frames keep
// arriving early, up to maxFrameGap, and resets otherwise.
func (t *tui) processPending() {
	f := t.pending
	t.pending = nil
	switch {
	case t.held && t.frameGap >= minFrameGap:
		t.frameGap *= 2
		if t.frameGap > maxFrameGap {
			t.frameGap = maxFrameGap
		}
	default:
		t.frameGap = minFrameGap
	}
	t.held = false
	t.onFrame(f)
	t.lastFrameDone = time.Now()
	f.Ack()
}

func (t *tui) onFrame(f *browser.ScreencastFrame) {
	if !f.Timestamp.IsZero() && f.Timestamp.Before(t.captureEnd) {
		t.logf("frame skipped: older than last capture by %v", t.captureEnd.Sub(f.Timestamp).Round(time.Millisecond))
		return
	}
	// A playing video repaints forever. When that is all that moved, the
	// rest of the page is still good: leave it alone, because the video
	// draws its own cells from inside the page.
	if box, only := t.videoOnly(f.Img); only {
		t.prevShot = f.Img
		t.logf("frame skipped: only the video at (%d,%d) changed", box.X, box.Y)
		return
	}
	t.prevShot = f.Img
	if t.capturing {
		// Covered by the capture in flight if it predates its screenshot;
		// decided when the capture lands.
		if f.Timestamp.After(t.frameDuring) {
			t.frameDuring = f.Timestamp
		}
		return
	}
	w, h := t.g.PixelSize()
	if f.Img == nil || f.Img.Bounds().Dx() != w || f.Img.Bounds().Dy() != h {
		if f.Img != nil {
			t.logf("frame %dx%d is a notification for %dx%d: capturing (gap %v)", f.Img.Bounds().Dx(), f.Img.Bounds().Dy(), w, h, t.frameGap)
		}
		t.requestCapture()
		return
	}
	// A full-size frame: render it directly, with a fresh walker pass.
	var runs []frame.TextRun
	var err error
	if !t.cfg.noText {
		if runs, err = t.b.TextRuns(); err != nil {
			t.msg = "text walker: " + err.Error()
		}
	}
	st, _ := t.b.State()
	if st.ScrollY != f.ScrollY {
		t.logf("frame skipped: scroll moved %v -> %v", f.ScrollY, st.ScrollY)
		return
	}
	t.applyCapture(captureResult{page: browser.Page{Img: f.Img, Runs: runs, State: st}, start: f.Timestamp})
}

// ---- drawing

// waitBuilt waits for a client-rendered page to finish drawing itself
// after a navigation: the load event only says the document arrived.
func (t *tui) waitBuilt() {
	t.docText, t.emptyRetries = -1, 0
	if t.cfg.quietWait > 0 {
		q, err := t.b.WaitDOMQuiet(t.cfg.quietWait, 5*time.Second, minPageText)
		if err == nil {
			t.docText = q.Text
			t.logf("dom quiet: %s after %d ms, %d characters of text", q.Why, q.Ms, q.Text)
		}
	}
	// The banner is answered in the background: the page shows at once,
	// with its banner for the moment it takes, instead of the first frame
	// waiting for the answer and for the page to settle after it.
	if t.cfg.cookies != "off" {
		go func(b *browser.Browser, prefer string, quiet time.Duration) {
			got, err := b.DismissConsent(prefer, 3*time.Second)
			if err != nil || len(got) == 0 {
				return
			}
			if quiet > 0 && b.ConsentReloaded() {
				b.WaitDOMQuiet(quiet, followUpWait(true), minPageText)
			}
			select {
			case t.consentCh <- got:
			default:
			}
		}(t.b, t.cfg.cookies, t.cfg.quietWait)
	}
	// Off by default here: it would cost every navigation a scroll through
	// the page, and the view follows the reader's own scrolling anyway.
	if t.cfg.wake == "on" {
		if w, err := t.b.WakeUp(wakePause, wakeMaxSteps, wakeMaxTime); err == nil {
			t.logf("wake: %d screens, page %d px tall, %d ms", w.Steps, w.Height, w.Ms)
			if t.cfg.quietWait > 0 {
				t.b.WaitDOMQuiet(t.cfg.quietWait, followUpWait(false), 0)
			}
		}
	}
}

// syncScroll moves the page's own scroll to follow the reader and
// captures. Inside the buffer the view moves without the page knowing,
// so nothing tied to scrolling, an observer revealing a block, an image
// loading lazily, a parallax layer, a bar that shrinks, could ever fire
// or land where it belongs. It runs once the scrolling pauses, when
// smoothness no longer matters, never while a capture is in flight,
// because a capture reads the page's scroll to place its buffer and a
// page moved under it lands the buffer somewhere else, with the view on
// its edge and the next step forcing yet another capture. The page is
// moved to where a recenter would put it, with the view in the middle of
// the viewport, and the capture that follows redraws everything as it is
// at that scroll position.
func (t *tui) syncScroll() {
	if !t.syncPending || t.capturing || t.captureWanted {
		return // stays pending; tried again when the capture has landed
	}
	t.syncPending = false
	_, winH := t.window()
	slack := t.g.Rows - winH
	// Place the buffer the way a recenter would for the last direction of
	// travel, mostly ahead of the view, so the two never undo each other.
	keepBehind := slack / 2
	switch {
	case t.lastDir > 0:
		keepBehind = t.viewRows() / 2
	case t.lastDir < 0:
		keepBehind = slack - t.viewRows()/2
	}
	if keepBehind > slack {
		keepBehind = slack
	}
	if keepBehind < 0 {
		keepBehind = 0
	}
	target := t.viewTop - keepBehind
	if max := t.docRows() - t.g.Rows; target > max {
		target = max
	}
	if target < 0 {
		target = 0
	}
	// Follow only once the view has travelled half a screen since the page
	// was last placed under it, or left the page's viewport altogether.
	// Following at every pause made a slow trackpad scroll a capture every
	// few rows, each one recentring the buffer and redrawing the screen.
	drift := t.viewTop - t.followedView
	if drift < 0 {
		drift = -drift
	}
	inside := t.viewTop >= t.pageRow && t.viewTop+winH <= t.pageRow+t.g.Rows
	if drift < t.viewRows()/2 && inside {
		return
	}
	if target == t.pageRow {
		return // the page is already there and the buffer shows it as it is
	}
	if err := t.b.ScrollToRow(target); err != nil {
		return
	}
	t.targetBufTop, t.pageRow, t.followedView = target, target, t.viewTop
	t.logf("page scrolled to follow the view: page row %d for view row %d, capturing", target, t.viewTop)
	t.requestCapture()
}

// startVideo points the in-page video renderer at the current grid and
// rendering options. It is cheap to repeat: the page reconfigures itself
// and picks up videos that appeared since.
func (t *tui) startVideo() {
	for _, v := range t.videos {
		if t.isTainted(v) {
			continue // already on the screenshot path, and logged as such
		}
		if !t.loggedVideo[[2]int{v.X, v.Y}] {
			if t.loggedVideo == nil {
				t.loggedVideo = map[[2]int]bool{}
			}
			t.loggedVideo[[2]int{v.X, v.Y}] = true
			t.logf("video at (%d,%d) %dx%d: in-page renderer", v.X, v.Y, v.W, v.H)
		}
	}
	if err := t.b.StartVideo(browser.VideoOptions{
		Ramp:   string(t.cfg.render.Ramp),
		Invert: t.r.Inverted(),
		Paper:  t.cfg.render.Paper,
		FPS:    t.cfg.videoFPS,
		Once:   t.cfg.video == "poster",
	}); err != nil {
		t.logf("video renderer: %v", err)
	} else if st := t.b.VideoStatus(); st != "" && st != t.lastVideoStatus {
		t.lastVideoStatus = st
		t.logf("video renderer: %s", st)
	}
}

// drawVideoFrame puts one frame of characters, already rendered by the
// page, into the cells of its video box and repaints only what changed.
// It never touches the rest of the frame and never asks for a capture.
func (t *tui) drawVideoFrame(vf browser.VideoFrame) {
	if t.frame == nil {
		return
	}
	// The page can read this one after all: stop photographing it.
	for i, o := range t.tainted {
		if o.X == vf.X && o.Y == vf.Y {
			t.tainted = append(t.tainted[:i], t.tainted[i+1:]...)
			t.logf("video at (%d,%d): back on the in-page renderer", vf.X, vf.Y)
			break
		}
	}
	t0 := time.Now()
	w, h := t.window()
	paint, known := t.videoCovers(vf.X, vf.Y, vf.Cols, vf.Rows)
	n := 0
	for j, line := range vf.Lines {
		row := vf.Y + j
		if row < 0 || row >= t.g.Rows {
			continue
		}
		col := vf.X
		for _, r := range line {
			if col >= t.g.Cols {
				break
			}
			if col < 0 {
				col++
				continue
			}
			if known && t.ownerPaint != nil {
				if i := row*t.g.Cols + col; i >= 0 && i < len(t.ownerPaint) && t.ownerPaint[i] > paint {
					col++
					continue // something painted after the video owns this cell
				}
			}
			c := byte(r)
			if r < 0x20 || r > 0x7E {
				c = '?'
			}
			if t.frame.At(col, row) != c {
				t.frame.Set(col, row, c)
				sx, sy := col-t.wx, row-(t.viewTop-t.bufTop)
				if sx >= 0 && sy >= 0 && sx < w && sy < h {
					t.scr.SetCell(sx, sy+pageTop, c, false)
					n++
				}
			}
			col++
		}
	}
	if n > 0 {
		t.placeCursor()
		t.scr.Show()
	}
	if t.videoSince.IsZero() {
		t.videoSince = time.Now()
	}
	t.videoN++
	t.videoDraw += time.Since(t0)
	if t.videoN%30 == 0 {
		el := time.Since(t.videoSince).Seconds()
		t.logf("video: %d frames, %.1f fps, %.3f ms per frame in Go, %d cells last",
			t.videoN, float64(t.videoN)/el, float64(t.videoDraw.Microseconds())/float64(t.videoN)/1000, n)
	}
}

// isTainted reports whether a video box is on the fallback path.
func (t *tui) isTainted(b frame.UIBox) bool {
	for _, o := range t.tainted {
		if o.X == b.X && o.Y == b.Y {
			return true
		}
	}
	return false
}

// taintedInterval caps the fallback path: photographing a box through
// Chrome costs a round trip, so it runs well below the video's own rate.
func (t *tui) taintedInterval() time.Duration {
	fps := 8.0
	if t.cfg.videoFPS > 0 && t.cfg.videoFPS < fps {
		fps = t.cfg.videoFPS
	}
	return time.Duration(float64(time.Second) / fps)
}

// addTaintedVideo records a video the page gave up on, so Go takes over.
func (t *tui) addTaintedVideo(vf browser.VideoFrame) {
	b := frame.UIBox{Kind: frame.UIMedia, X: vf.X, Y: vf.Y, W: vf.Cols, H: vf.Rows, Video: true}
	for _, o := range t.tainted {
		if o.X == b.X && o.Y == b.Y && o.W == b.W && o.H == b.H {
			return
		}
	}
	t.tainted = append(t.tainted, b)
	t.logf("video at (%d,%d) %dx%d: page cannot read its pixels, falling back to the screenshot path at %.0f fps",
		b.X, b.Y, b.W, b.H, float64(time.Second)/float64(t.taintedInterval()))
}

// refreshTaintedVideos photographs each fallback video's box and redraws
// only its cells, dithered the same ordered way as the in-page path.
func (t *tui) refreshTaintedVideos() {
	if t.frame == nil {
		return
	}
	for _, b := range t.tainted {
		t0 := time.Now()
		img, err := t.b.CropCells(b.X, b.Y, b.W, b.H)
		if err != nil || img == nil {
			continue
		}
		block := t.r.RenderVideo(img, b.W, b.H)
		paint, known := t.videoCovers(b.X, b.Y, b.W, b.H)
		w, h := t.window()
		n := 0
		for y := 0; y < b.H; y++ {
			for x := 0; x < b.W; x++ {
				if i := (b.Y + y) * t.g.Cols; known && t.ownerPaint != nil && i+b.X+x < len(t.ownerPaint) && t.ownerPaint[i+b.X+x] > paint {
					continue
				}
				col, row := b.X+x, b.Y+y
				if col < 0 || row < 0 || col >= t.g.Cols || row >= t.g.Rows {
					continue
				}
				c := block.At(x, y)
				if t.frame.At(col, row) == c {
					continue
				}
				t.frame.Set(col, row, c)
				sx, sy := col-t.wx, row-(t.viewTop-t.bufTop)
				if sx >= 0 && sy >= 0 && sx < w && sy < h {
					t.scr.SetCell(sx, sy+pageTop, c, false)
					n++
				}
			}
		}
		if n > 0 {
			t.placeCursor()
			t.scr.Show()
		}
		t.taintedN++
		if t.taintedN%8 == 0 {
			t.logf("video fallback at (%d,%d): frame %d, %v, %d cells", b.X, b.Y, t.taintedN, time.Since(t0).Round(time.Millisecond), n)
		}
	}
}

// ownerPaint records, for every cell of the frame, the paint order of
// whatever ended up drawing it. A video is an element like any other:
// everything the page paints after it, a caption over it, the menu above
// it, a panel that overlaps it, has to stay on top while the picture
// moves underneath. The map is built once per capture, so the per-frame
// path stays free.
func ownerPaint(g grid.Grid, boxes []frame.UIBox, runs []frame.TextRun, dst []int64) []int64 {
	n := g.Cells()
	if cap(dst) < n {
		dst = make([]int64, n)
	}
	dst = dst[:n]
	for i := range dst {
		dst[i] = -1
	}
	mark := func(x0, y0, x1, y1 int, paint int64) {
		for y := y0; y < y1; y++ {
			if y < 0 || y >= g.Rows {
				continue
			}
			for x := x0; x < x1; x++ {
				if x < 0 || x >= g.Cols {
					continue
				}
				if i := y*g.Cols + x; paint > dst[i] {
					dst[i] = paint
				}
			}
		}
	}
	for _, b := range boxes {
		switch b.Kind {
		case frame.UIMedia:
			continue // a picture is the thing being covered, not a cover
		case frame.UIFrame:
			// A frame draws only its outline, not its inside.
			mark(b.X, b.Y, b.Right(), b.Y+1, b.Paint)
			mark(b.X, b.Bottom()-1, b.Right(), b.Bottom(), b.Paint)
			mark(b.X, b.Y, b.X+1, b.Bottom(), b.Paint)
			mark(b.Right()-1, b.Y, b.Right(), b.Bottom(), b.Paint)
		default:
			mark(b.X, b.Y, b.Right(), b.Bottom(), b.Paint)
		}
	}
	for _, r := range runs {
		k := len([]rune(r.Text))
		if k > r.W {
			k = r.W
		}
		mark(r.X, r.Y, r.X+k, r.Y+1, r.Paint)
	}
	return dst
}

// videoCovers reports, for a video frame arriving from the page, the
// paint order to compare cells against. The page and the snapshot round a
// box to cells independently and the snapshot clips it to what is on
// screen, so the two rarely agree: the video box that overlaps the frame
// most wins, and with none the lowest video paint order is used, which
// protects more rather than less.
func (t *tui) videoCovers(x, y, cols, rows int) (int64, bool) {
	best, bestArea := int64(-1), 0
	lowest := int64(-1)
	for _, v := range t.videos {
		if lowest < 0 || v.Paint < lowest {
			lowest = v.Paint
		}
		w := minInt(x+cols, v.Right()) - maxInt(x, v.X)
		h := minInt(y+rows, v.Bottom()) - maxInt(y, v.Y)
		if w > 0 && h > 0 && w*h > bestArea {
			best, bestArea = v.Paint, w*h
		}
	}
	if bestArea > 0 {
		return best, true
	}
	return lowest, lowest >= 0
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// videoOnly reports whether everything that moved since the previous
// screencast frame lies inside a video box. The frames are compared block
// by block on averages, because the screencast is JPEG and a single
// sampled pixel would call compression noise a change.
func (t *tui) videoOnly(img image.Image) (frame.UIBox, bool) {
	var none frame.UIBox
	if img == nil || t.prevShot == nil || t.frame == nil || (len(t.videos) == 0 && len(t.tainted) == 0) {
		return none, false
	}
	a, b := t.prevShot.Bounds(), img.Bounds()
	if a.Dx() != b.Dx() || a.Dy() != b.Dy() || b.Dx() < 8 || b.Dy() < 8 {
		return none, false
	}
	const blocks = 20
	stepX, stepY := maxInt(b.Dx()/blocks, 2), maxInt(b.Dy()/blocks, 2)
	// Nothing outside a video moving is enough: if nothing moved at all
	// there is nothing to capture either.
	hit := none
	for y := 0; y+stepY <= b.Dy(); y += stepY {
		for x := 0; x+stepX <= b.Dx(); x += stepX {
			if blockDiff(t.prevShot, a.Min, img, b.Min, x, y, stepX, stepY) < 10 {
				continue
			}
			// Which cells does this block cover?
			x0, x1 := x*t.g.Cols/b.Dx(), ((x+stepX)*t.g.Cols+b.Dx()-1)/b.Dx()
			y0, y1 := y*t.g.Rows/b.Dy(), ((y+stepY)*t.g.Rows+b.Dy()-1)/b.Dy()
			// A block straddling a video's edge still changed because of
			// the video, so overlapping is enough. The cost is a band one
			// block wide around a video where a change goes unnoticed
			// until something else moves.
			inside := false
			for _, v := range append(append([]frame.UIBox(nil), t.videos...), t.tainted...) {
				if x1 > v.X && x0 < v.Right() && y1 > v.Y && y0 < v.Bottom() {
					inside, hit = true, v
					break
				}
			}
			if !inside {
				return none, false // something outside a video moved
			}
		}
	}
	return hit, true
}

// blockDiff is the mean absolute luma difference over a block, sampled on
// a small lattice so the cost stays flat whatever the frame size.
func blockDiff(a image.Image, ao image.Point, b image.Image, bo image.Point, x, y, w, h int) int {
	const n = 4
	sum := 0
	for j := 0; j < n; j++ {
		for i := 0; i < n; i++ {
			px, py := x+(i*2+1)*w/(2*n), y+(j*2+1)*h/(2*n)
			d := lumaAt(a, ao, px, py) - lumaAt(b, bo, px, py)
			if d < 0 {
				d = -d
			}
			sum += d
		}
	}
	return sum / (n * n)
}

func lumaAt(img image.Image, base image.Point, x, y int) int {
	r, g, bl, _ := img.At(base.X+x, base.Y+y).RGBA()
	return int((2126*r + 7152*g + 722*bl) / 10000 >> 8)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// highlight is the reverse-video mask for the current item, nil for none.
func (t *tui) highlight() []bool {
	if t.frame == nil || t.cur < 0 || t.cur >= len(t.items) {
		return nil
	}
	hl := make([]bool, t.frame.Cols*t.frame.Rows)
	for _, i := range t.items[t.cur].runs {
		r := t.runs[i]
		for col := r.X; col < r.X+r.W && col < t.frame.Cols; col++ {
			if col >= 0 && r.Y >= 0 && r.Y < t.frame.Rows {
				hl[r.Y*t.frame.Cols+col] = true
			}
		}
	}
	return hl
}

func sameMask(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// draw repaints bar, page window and status in full.
func (t *tui) draw() {
	t.drawBar()
	hl := t.highlight()
	wy := t.viewTop - t.bufTop
	if t.frame != nil {
		w, _ := t.window()
		t.wx, _ = clampWindow(t.wx, 0, w, 1, t.g.Cols, 1)
		t.scr.DrawFrame(t.frame, pageTop, hl, t.wx, wy, t.cols, pageRows(t.rows))
	}
	t.shown, t.shownHL, t.shownWx, t.shownWy = t.frame, hl, t.wx, wy
	t.scr.DrawBar(t.rows-1, t.statusLine(), t.cols, -1)
	t.placeCursor()
	t.scr.Show()
}

// drawUpdate repaints only the cells that differ from what is on screen.
// A change of window, highlight or frame size falls back to draw.
func (t *tui) drawUpdate(prev *frame.Frame) int {
	hl := t.highlight()
	wy := t.viewTop - t.bufTop
	if prev == nil || t.shown != prev || t.frame == nil || prev.Cols != t.frame.Cols || prev.Rows != t.frame.Rows ||
		t.wx != t.shownWx || wy != t.shownWy || !sameMask(hl, t.shownHL) {
		t.draw()
		return -1
	}
	w, h := t.window()
	n := 0
	for _, c := range t.frame.Diff(prev) {
		x, y := c.Col-t.wx, c.Row-wy
		if x < 0 || y < 0 || x >= w || y >= h {
			continue
		}
		t.scr.SetCell(x, y+pageTop, c.C, hl != nil && hl[c.Row*t.frame.Cols+c.Col])
		n++
	}
	t.shown = t.frame
	t.drawBar()
	t.scr.DrawBar(t.rows-1, t.statusLine(), t.cols, -1)
	t.placeCursor()
	t.scr.Show()
	t.logf("cells changed: %d", n)
	return n
}

func (t *tui) drawBar() {
	if t.barFocus {
		text, _ := t.bar.View(t.cols)
		t.scr.DrawBar(barRow, text, t.cols, -1)
	} else {
		t.scr.DrawBar(barRow, t.state.URL, t.cols, -1)
	}
}

// placeCursor puts the terminal's own cursor where typing will land: in
// the URL bar while it is being edited, at the caret of a focused page
// field otherwise, and nowhere when nothing takes typing.
func (t *tui) placeCursor() {
	if t.barFocus {
		_, cur := t.bar.View(t.cols)
		if cur >= 0 {
			t.scr.ShowCursorAt(cur, barRow)
			return
		}
	}
	if t.state.Focus == "input" && t.state.CaretHeight > 0 {
		cw := float64(t.g.CellW) / t.b.Zoom()
		ch := float64(t.g.CellH) / t.b.Zoom()
		col := int(math.Round(t.state.CaretLeft/cw)) + t.state.CaretIndex - t.wx
		row := int(math.Floor((t.state.CaretTop+t.state.CaretHeight/2)/ch)) - (t.viewTop - t.bufTop) + pageTop
		if col >= 0 && col < t.cols && row >= pageTop && row < t.rows-1 {
			t.scr.ShowCursorAt(col, row)
			return
		}
	}
	t.scr.HideCursor()
}

// statusLine: transient message, else input mode, else the current item;
// window position and scroll position sit at the right edge.
func (t *tui) statusLine() string {
	left := t.msg
	switch {
	case left != "":
	case t.state.Focus == "input":
		left = "input  (Esc leaves the field)"
	case t.cur >= 0 && t.cur < len(t.items):
		it := t.items[t.cur]
		switch {
		case it.field:
			left = "field: " + it.text
		case it.button:
			left = "button: " + it.text
		default:
			left = it.href
		}
	default:
		left = "Tab links  Enter follow  Ctrl+L url  Ctrl+[ back  Ctrl+] fwd  Ctrl+D dump  Ctrl+Q quit"
	}
	right := t.scrollPercent()
	if w, _ := t.window(); w < t.g.Cols {
		right = fmt.Sprintf("col %d of %d  %s", t.wx, t.g.Cols, right)
	}
	width := t.cols - len(right) - 1
	if width < 0 {
		width = 0
	}
	if len(left) > width {
		left = left[:width]
	}
	return fmt.Sprintf("%-*s %s", width, left, right)
}

func (t *tui) scrollPercent() string {
	_, winH := t.window()
	max := t.docRows() - winH
	if max <= 0 {
		return "100%"
	}
	pct := int(float64(t.viewTop)/float64(max)*100 + 0.5)
	if pct > 100 {
		pct = 100
	}
	if pct < 0 {
		pct = 0
	}
	return fmt.Sprintf("%3d%%", pct)
}

// ---- input

// handle dispatches one terminal event; quit means exit, scrolled means a
// scroll was queued for coalescing.
func (t *tui) handle(ev tcell.Event) (quit, scrolled bool) {
	switch e := ev.(type) {
	case *tcell.EventResize:
		t.cols, t.rows = e.Size()
		t.scr.Sync()
		g := t.frameGrid()
		if g == t.g {
			t.draw()
			return false, false
		}
		t.g = g
		t.b.SetScreenRows(t.viewRows())
		if err := t.b.Resize(t.g); err != nil {
			t.msg = "resize: " + err.Error()
		}
		t.requestCapture()
	case *tcell.EventMouse:
		return false, t.mouse(e)
	case *tcell.EventKey:
		return t.keyEvent(e)
	}
	return false, false
}

func (t *tui) mouse(e *tcell.EventMouse) (scrolled bool) {
	btn := e.Buttons()
	sx, sy := e.Position()
	x, row := sx+t.wx, sy-pageTop+(t.viewTop-t.bufTop) // frame coordinates
	switch {
	case btn&tcell.WheelUp != 0:
		t.queueScroll(-scrollStep)
		return true
	case btn&tcell.WheelDown != 0:
		t.queueScroll(scrollStep)
		return true
	case btn&tcell.Button1 != 0:
		if t.mouseDown {
			return false // drag: one click per press
		}
		t.mouseDown = true
		if sy == barRow {
			t.focusBar()
			return false
		}
		if sy < pageTop || row < 0 || row >= t.g.Rows || x >= t.g.Cols {
			return false
		}
		t.msg = ""
		t.barFocus = false
		t0 := time.Now()
		if err := t.b.Click(x, row); err != nil {
			t.msg = "click: " + err.Error()
		}
		t.b.WaitSettled(300 * time.Millisecond)
		t.requestCapture()
		t.logf("click at %d,%d: %v", x, row, time.Since(t0).Round(time.Millisecond))
	case btn == tcell.ButtonNone:
		t.mouseDown = false
	}
	return false
}

func (t *tui) focusBar() {
	t.barFocus = true
	t.bar.Set(t.state.URL)
	t.draw()
}

// isEscape covers Escape and Ctrl+[, which most terminals cannot tell
// apart; terminals with the kitty keyboard protocol report Ctrl+[ as a
// rune with the control modifier.
func isEscape(e *tcell.EventKey) bool {
	return e.Key() == tcell.KeyEscape || (e.Key() == tcell.KeyRune && e.Rune() == '[' && e.Modifiers()&tcell.ModCtrl != 0)
}

func isCtrlRightBracket(e *tcell.EventKey) bool {
	return e.Key() == tcell.KeyCtrlRightSq || (e.Key() == tcell.KeyRune && e.Rune() == ']' && e.Modifiers()&tcell.ModCtrl != 0)
}

func (t *tui) keyEvent(e *tcell.EventKey) (quit, scrolled bool) {
	k := e.Key()
	switch k {
	case tcell.KeyCtrlQ, tcell.KeyCtrlC:
		return true, false
	case tcell.KeyCtrlL:
		t.focusBar()
		return false, false
	}
	if t.barFocus {
		switch t.bar.Handle(e) {
		case ui.BarSubmit:
			t.barFocus = false
			t.navigate(normalizeURL(t.bar.String()))
		case ui.BarCancel:
			t.barFocus = false
			t.draw()
		default:
			t.draw()
		}
		return false, false
	}
	t.msg = ""
	switch {
	case k == tcell.KeyCtrlR:
		if err := t.b.Reload(); err != nil {
			t.msg = "reload: " + err.Error()
		}
		t.requestCapture()
		return false, false
	case k == tcell.KeyCtrlD:
		t.dump()
		return false, false
	case isCtrlRightBracket(e):
		if err := t.b.Forward(); err != nil {
			t.msg = "forward: no next page"
		}
		t.requestCapture()
		return false, false
	}
	if t.state.Focus == "input" {
		t.inputKey(e)
		return false, false
	}
	if t.state.Focus == "video" && t.videoKey(e) {
		return false, false
	}
	_, winH := t.window()
	shift := e.Modifiers()&tcell.ModShift != 0
	switch {
	case isEscape(e):
		if err := t.b.Back(); err != nil {
			t.msg = "back: no previous page"
		}
		t.waitBuilt()
		t.requestCapture()
	case shift && k == tcell.KeyLeft:
		t.moveWindow(-1)
	case shift && k == tcell.KeyRight:
		t.moveWindow(1)
	case shift && k == tcell.KeyUp:
		t.queueScroll(-(winH + 1) / 2)
		return false, true
	case shift && k == tcell.KeyDown:
		t.queueScroll((winH + 1) / 2)
		return false, true
	case k == tcell.KeyUp:
		t.queueScroll(-scrollStep)
		return false, true
	case k == tcell.KeyDown:
		t.queueScroll(scrollStep)
		return false, true
	case k == tcell.KeyPgUp:
		t.queueScroll(-(winH - 2))
		return false, true
	case k == tcell.KeyPgDn, k == tcell.KeyRune && e.Rune() == ' ':
		t.queueScroll(winH - 2)
		return false, true
	case k == tcell.KeyHome:
		t.queueAbs(0)
		return false, true
	case k == tcell.KeyEnd:
		t.queueAbs(1 << 30)
		return false, true
	case k == tcell.KeyTab:
		t.cycle(1)
	case k == tcell.KeyBacktab:
		t.cycle(-1)
	case k == tcell.KeyEnter:
		t.follow()
	}
	return false, false
}

// videoKey forwards a player's own shortcuts to the page while a video
// has the focus, and reports whether it took the key. Space is the one
// that matters: it must pause the video, not scroll the page.
func (t *tui) videoKey(e *tcell.EventKey) bool {
	var key string
	switch {
	case e.Key() == tcell.KeyRune:
		switch e.Rune() {
		case ' ':
			key = " "
		case 'k', 'f', 'm', 'j', 'l', 'c':
			key = string(e.Rune())
		}
	case e.Key() == tcell.KeyLeft:
		key = kb.ArrowLeft
	case e.Key() == tcell.KeyRight:
		key = kb.ArrowRight
	case e.Key() == tcell.KeyUp:
		key = kb.ArrowUp
	case e.Key() == tcell.KeyDown:
		key = kb.ArrowDown
	}
	if key == "" {
		return false
	}
	if err := t.b.Key(key); err != nil {
		t.msg = "video key: " + err.Error()
	}
	t.logf("video key %q sent to the page", key)
	// The picture answers on its own; only the state is worth refreshing.
	t.requestCapture()
	return true
}

// inputKey forwards keys to the focused page field.
func (t *tui) inputKey(e *tcell.EventKey) {
	var err error
	switch k := e.Key(); {
	case isEscape(e):
		err = t.b.Blur()
	case k == tcell.KeyEnter:
		err = t.b.Key(kb.Enter)
		t.b.WaitSettled(300 * time.Millisecond) // Enter may submit a form
	case k == tcell.KeyBackspace, k == tcell.KeyBackspace2:
		err = t.b.Key(kb.Backspace)
	case k == tcell.KeyDelete:
		err = t.b.Key(kb.Delete)
	case k == tcell.KeyLeft:
		err = t.b.Key(kb.ArrowLeft)
	case k == tcell.KeyRight:
		err = t.b.Key(kb.ArrowRight)
	case k == tcell.KeyUp:
		err = t.b.Key(kb.ArrowUp)
	case k == tcell.KeyDown:
		err = t.b.Key(kb.ArrowDown)
	case k == tcell.KeyHome:
		err = t.b.Key(kb.Home)
	case k == tcell.KeyEnd:
		err = t.b.Key(kb.End)
	case k == tcell.KeyTab:
		err = t.b.Key(kb.Tab)
	case k == tcell.KeyRune:
		err = t.b.InsertText(string(e.Rune()))
	default:
		return
	}
	if err != nil {
		t.msg = "input: " + err.Error()
	}
	t.requestCapture()
}

// cycle moves the current item by d in reading order, wrapping around.
func (t *tui) cycle(d int) {
	n := len(t.items)
	if n == 0 {
		t.msg = "no links on screen"
		t.draw()
		return
	}
	switch {
	case t.cur < 0 && d > 0:
		t.cur = 0
	case t.cur < 0:
		t.cur = n - 1
	default:
		t.cur = ((t.cur+d)%n + n) % n
	}
	t.key = t.items[t.cur].key
	t.draw()
}

// follow clicks the current item: a link navigates, a field takes focus.
func (t *tui) follow() {
	if t.cur < 0 || t.cur >= len(t.items) {
		return
	}
	r := t.runs[t.items[t.cur].runs[0]]
	if err := t.b.Click(r.X, r.Y); err != nil {
		t.msg = "click: " + err.Error()
	}
	t.b.WaitSettled(300 * time.Millisecond)
	t.requestCapture()
}

func (t *tui) navigate(url string) {
	t.scr.DrawBar(barRow, "loading "+url, t.cols, -1)
	t.scr.Show()
	if err := t.b.Navigate(url); err != nil {
		t.msg = err.Error()
	}
	t.waitBuilt()
	t.viewTop, t.bufTop, t.targetBufTop, t.pageRow, t.followedView = 0, 0, 0, 0, 0
	t.syncPending = false
	t.requestCapture()
}

// dump writes the current screen at the frame's full width.
func (t *tui) dump() {
	if t.frame == nil {
		return
	}
	path := t.cfg.dumpPath
	if path == "" {
		path = "ascii-" + time.Now().Format("20060102-150405") + ".txt"
	}
	wy := t.viewTop - t.bufTop
	view := frame.New(t.frame.Cols, t.viewRows())
	for y := 0; y < view.Rows; y++ {
		copy(view.Row(y), t.frame.Row(wy+y))
	}
	if err := writeDump(path, view); err != nil {
		t.msg = "dump: " + err.Error()
	} else {
		t.msg = fmt.Sprintf("dumped %dx%d to %s", view.Cols, view.Rows, path)
	}
	t.draw()
}

func writeDump(path string, f *frame.Frame) error {
	fh, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := f.WriteTo(fh); err != nil {
		fh.Close()
		return err
	}
	return fh.Close()
}
