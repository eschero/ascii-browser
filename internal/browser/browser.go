// Package browser drives a headless Chrome through the DevTools Protocol
// with chromedp: it sizes the viewport to the grid, injects the BlankMono
// grid stylesheet in every document, takes screenshots and runs the text
// walker.
package browser

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/eschero/ascii-browser/internal/blankfont"
	"github.com/eschero/ascii-browser/internal/frame"
	"github.com/eschero/ascii-browser/internal/grid"
	"image"
	"image/color"
	_ "image/jpeg" // screenshot decoders
	_ "image/png"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Options configure a Browser.
type Options struct {
	ChromePath string // empty: chromedp autodetects the binary
	// ProfileDir is the directory Chrome keeps its profile in, so
	// cookies, consents and logins outlive the session. Empty means a
	// throwaway profile.
	ProfileDir string
	// Audio lets pages play sound through the system output, and lets a
	// video start with its sound without a click. Off, every video plays
	// muted and Chrome makes no sound at all.
	Audio bool
	// Opacity says how an element at opacity 0 is read. "pending", the
	// default, treats one that is animating its opacity as visible, since
	// it is about to appear. "strict" hides it regardless. "ignore" reads
	// every such element as visible: the text is taken from the DOM even
	// where the page paints nothing, for sites whose reveal animations
	// never fire in a headless browser. Display, visibility and the clip
	// of the ancestors are respected in every mode.
	Opacity    string
	Grid       grid.Grid     // initial viewport
	Headless   bool          // false opens a visible window, for debugging
	NavTimeout time.Duration // wait for the load event; 10s when zero
	// Zoom scales the page: the CSS viewport is the grid's pixel size divided
	// by Zoom and the page is rendered at deviceScaleFactor Zoom, so
	// graphics grow while every character still fills exactly one cell.
	// 1 when zero.
	Zoom float64
	// Renderer picks how the page is read: "shot" takes a screenshot and
	// measures the text in the page, "snap" takes a screenshot and reads
	// the text from DOMSnapshot, "ui" draws the interface from DOMSnapshot
	// with no screenshot at all, and "auto" (the default) picks the text
	// source by frame height. Empty means "auto".
	Renderer string
	// UIMedia photographs each media element and renders it inside its
	// frame instead of drawing a placeholder. Only the "ui" renderer
	// uses it.
	UIMedia bool
	// Video is how a <video> is drawn: "off" leaves a placeholder,
	// "poster" renders it once and never again, "live" refreshes it at
	// VideoFPS without recapturing the page. Empty means "poster".
	Video string
	// VideoFPS caps how often a live video is refreshed; 0 means 2.
	VideoFPS float64
	// ScreenRows is one screenful of the frame, in rows; "auto" switches
	// to the snapshot once the frame is four of them or taller. 0 means
	// the whole frame is one screen.
	ScreenRows int
	// ShotFormat is the screenshot encoding: "png", "jpeg", or "auto"
	// (default): png up to jpegAbovePixels device pixels, jpeg beyond, where
	// png encoding and decoding start to dominate the frame time. After
	// averaging 128 pixels per cell the two are indistinguishable.
	ShotFormat string
}

// Screenshot limits, in device pixels. A capture larger than
// maxShotPixels is taken as vertical tiles and stitched.
var (
	maxShotPixels   = 24 << 20
	jpegAbovePixels = 4 << 20
)

// Browser is one Chrome tab.
type Browser struct {
	ctx             context.Context
	allocCancel     context.CancelFunc
	tabCancel       context.CancelFunc
	navTimeout      time.Duration
	zoom            float64
	shotFormat      string
	renderer        string
	screenRows      int
	uiMedia         bool
	video           string
	videoFPS        float64
	videoMu         sync.Mutex
	videoCh         chan VideoFrame
	videoListening  bool
	videoStatus     string
	profileNote     string
	opacity         string
	audio           bool
	consentReloaded bool
	posterMu        sync.Mutex
	posters         map[string]image.Image // frozen video crops, by cell box

	mu       sync.Mutex
	grid     grid.Grid
	frameID  cdp.FrameID
	settled  chan struct{} // one buffered signal: load event or main frame stopped loading
	navSeq   int64         // bumped whenever the main frame starts loading a new document
	readySeq int64         // bumped whenever the main frame's document is parsed or loaded

	frames  chan *ScreencastFrame // latest screencast frame, see StartScreencast
	frameMu *sync.Mutex
	casting bool
}

// New starts Chrome and prepares the tab. ctx bounds the whole lifetime.
// A profile directory is used by one Chrome at a time, so when a second
// session finds it taken the browser falls back to a throwaway profile
// rather than refusing to start.
func New(ctx context.Context, opts Options) (*Browser, error) {
	b, err := start(ctx, opts)
	if err == nil || opts.ProfileDir == "" {
		return b, err
	}
	shared := opts.ProfileDir
	opts.ProfileDir = ""
	b, err2 := start(ctx, opts)
	if err2 != nil {
		return nil, err
	}
	b.profileNote = "profile " + shared + " is in use by another session: this one keeps nothing"
	return b, nil
}

func start(ctx context.Context, opts Options) (*Browser, error) {
	if opts.NavTimeout == 0 {
		opts.NavTimeout = 10 * time.Second
	}
	if opts.Zoom <= 0 {
		opts.Zoom = 1
	}
	w, h := opts.Grid.PixelSize()
	allocOpts := []chromedp.ExecAllocatorOption{
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.WindowSize(w, h),
		chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("disable-smooth-scrolling", true),
		// The GPU decodes video in hardware and composites it: without it a
		// playing video costs most of a core in software. ASCII_NO_GPU turns
		// it off for comparison.
		chromedp.Flag("disable-gpu", os.Getenv("ASCII_NO_GPU") != ""),
		chromedp.Flag("force-device-scale-factor", "1"),
		chromedp.Flag("font-render-hinting", "none"),
		chromedp.Flag("force-color-profile", "srgb"),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-background-timer-throttling", true),
		chromedp.Flag("disable-backgrounding-occluded-windows", true),
		chromedp.Flag("disable-renderer-backgrounding", true),
		chromedp.Flag("disable-breakpad", true),
		chromedp.Flag("disable-client-side-phishing-detection", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-hang-monitor", true),
		chromedp.Flag("disable-popup-blocking", true),
		chromedp.Flag("disable-prompt-on-repost", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("disable-features", "Translate,MediaRouter,OptimizationHints,BackForwardCache,IsolateOrigins,site-per-process"),
		chromedp.Flag("metrics-recording-only", true),
		chromedp.Flag("password-store", "basic"),
		chromedp.Flag("use-mock-keychain", true),
		// Less recognisable as an automated browser: sites that check
		// navigator.webdriver answer with a verification page and nothing
		// else.
		chromedp.Flag("enable-automation", false),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		// Every frame in one renderer, so a consent manager living in a
		// cross-origin iframe appears in the frame tree and can be answered.
		chromedp.Flag("disable-site-isolation-trials", true),
	}
	if opts.Headless {
		allocOpts = append(allocOpts, chromedp.Headless)
	}
	if opts.Audio {
		// chromedp's defaults mute Chrome; false removes the switch.
		allocOpts = append(allocOpts, chromedp.Flag("mute-audio", false),
			chromedp.Flag("autoplay-policy", "no-user-gesture-required"))
	} else {
		allocOpts = append(allocOpts, chromedp.Flag("mute-audio", true))
	}
	if opts.ChromePath != "" {
		allocOpts = append(allocOpts, chromedp.ExecPath(opts.ChromePath))
	}
	if opts.ProfileDir != "" {
		allocOpts = append(allocOpts, chromedp.UserDataDir(opts.ProfileDir))
	}
	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx, allocOpts...)
	tabCtx, tabCancel := chromedp.NewContext(allocCtx)
	b := &Browser{
		ctx:         tabCtx,
		allocCancel: allocCancel,
		tabCancel:   tabCancel,
		navTimeout:  opts.NavTimeout,
		zoom:        opts.Zoom,
		shotFormat:  opts.ShotFormat,
		opacity:     opacityMode(opts.Opacity),
		audio:       opts.Audio,
		renderer:    opts.Renderer,
		screenRows:  opts.ScreenRows,
		uiMedia:     opts.UIMedia,
		video:       opts.Video,
		videoFPS:    opts.VideoFPS,
		posters:     map[string]image.Image{},
		grid:        opts.Grid,
		settled:     make(chan struct{}, 1),
	}
	chromedp.ListenTarget(tabCtx, b.onEvent)

	css := blankfont.CSS(opts.Grid.CellW, opts.Grid.CellH, opts.Zoom)
	err := chromedp.Run(tabCtx,
		page.SetBypassCSP(true),
		b.metricsAction(opts.Grid),
		plainUserAgent(),
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(gridStyleScript(css)).Do(ctx)
			return err
		}),
	)
	if err != nil {
		b.Close()
		return nil, fmt.Errorf("start chrome: %w", err)
	}
	return b, nil
}

// Close shuts Chrome down.
func (b *Browser) Close() error {
	err := chromedp.Cancel(b.ctx)
	b.tabCancel()
	b.allocCancel()
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (b *Browser) onEvent(ev interface{}) {
	switch e := ev.(type) {
	case *page.EventFrameStartedLoading:
		b.mu.Lock()
		if b.frameID == "" || e.FrameID == b.frameID {
			b.navSeq++
		}
		b.mu.Unlock()
	case *page.EventDomContentEventFired:
		// The document is parsed: enough to move on, since the DOM quiet
		// wait follows. The load event on a page full of ads and trackers
		// can take ten seconds and more.
		b.mu.Lock()
		b.readySeq = b.navSeq
		b.mu.Unlock()
		b.signal()
	case *page.EventLoadEventFired:
		b.signal()
	case *page.EventFrameStoppedLoading:
		b.mu.Lock()
		main := b.frameID == "" || e.FrameID == b.frameID
		b.mu.Unlock()
		if main {
			b.signal()
		}
	}
}

func (b *Browser) signal() {
	select {
	case b.settled <- struct{}{}:
	default:
	}
}

func (b *Browser) drain() {
	select {
	case <-b.settled:
	default:
	}
}

// WaitSettled blocks until the page fires a load or frameStoppedLoading
// event, or max elapses. Call it after an action that may trigger a load.
func (b *Browser) WaitSettled(max time.Duration) {
	t := time.NewTimer(max)
	defer t.Stop()
	select {
	case <-b.settled:
	case <-t.C:
	case <-b.ctx.Done():
	}
}

// Grid returns the current viewport grid.
func (b *Browser) Grid() grid.Grid {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.grid
}

// VideoMode reports how videos are drawn.
func (b *Browser) VideoMode() string {
	if b.video == "" {
		return "poster"
	}
	return b.video
}

// VideoInterval is the shortest gap between refreshes of a live video.
func (b *Browser) VideoInterval() time.Duration {
	fps := b.videoFPS
	if fps <= 0 {
		fps = 2
	}
	return time.Duration(float64(time.Second) / fps)
}

// forgetPosters drops the frozen video crops; a new page has new videos.
func (b *Browser) forgetPosters() {
	b.posterMu.Lock()
	b.posters = map[string]image.Image{}
	b.posterMu.Unlock()
}

// Navigate loads a URL and waits for its load event (or the timeout).
func (b *Browser) Navigate(url string) error {
	t0 := time.Now()
	err := b.navigateOnce(url)
	// One more try for an error that came fast; one that took the whole
	// timeout would only take it again.
	if err != nil && transientNetError(err.Error()) && time.Since(t0) < b.navTimeout {
		time.Sleep(500 * time.Millisecond)
		err = b.navigateOnce(url)
	}
	return err
}

// transientNetError reports network errors worth one more try: a name that
// did not resolve under load, a connection dropped half way.
func transientNetError(s string) bool {
	for _, e := range []string{"ERR_NAME_NOT_RESOLVED", "ERR_TIMED_OUT", "ERR_CONNECTION_RESET",
		"ERR_CONNECTION_CLOSED", "ERR_HTTP2_PROTOCOL_ERROR", "ERR_NETWORK_CHANGED", "ERR_EMPTY_RESPONSE"} {
		if strings.Contains(s, e) {
			return true
		}
	}
	return false
}

func (b *Browser) navigateOnce(url string) error {
	b.drain()
	b.forgetPosters()
	// The navigation command itself can hang, on a page that answers with
	// a challenge that never finishes loading: bound it by the navigation
	// timeout, stop the load, and carry on with whatever arrived.
	// Twice the settle timeout: a slow server still gets its answer in, a
	// challenge page that never finishes does not hold the browser forever.
	nctx, cancel := context.WithTimeout(b.ctx, 2*b.navTimeout)
	defer cancel()
	err := chromedp.Run(nctx, chromedp.ActionFunc(func(ctx context.Context) error {
		fid, _, errText, _, err := page.Navigate(url).Do(ctx)
		if err != nil {
			return err
		}
		if errText != "" {
			return fmt.Errorf("navigate %s: %s", url, errText)
		}
		b.mu.Lock()
		b.frameID = fid
		b.mu.Unlock()
		return nil
	}))
	if err != nil && errors.Is(nctx.Err(), context.DeadlineExceeded) && b.ctx.Err() == nil {
		chromedp.Run(b.ctx, page.StopLoading())
		// Carry on with what arrived, but say so when nothing did.
		var got string
		chromedp.Run(b.ctx, chromedp.Evaluate(`location.href + " " + (document.body ? document.body.innerText.length : 0)`, &got))
		if strings.HasPrefix(got, "about:blank") || strings.HasSuffix(got, " 0") {
			return fmt.Errorf("navigate %s: no answer in %v", url, 2*b.navTimeout)
		}
		return nil
	}
	if err != nil {
		return err
	}
	b.WaitSettled(b.navTimeout)
	return nil
}

// Reload reloads the page and waits for it to settle.
func (b *Browser) Reload() error {
	b.drain()
	if err := chromedp.Run(b.ctx, page.Reload()); err != nil {
		return err
	}
	b.WaitSettled(b.navTimeout)
	return nil
}

// metricsAction sets the CSS viewport to the grid divided by the zoom and
// the device scale factor to the zoom, so the screenshot is exactly the
// grid's pixel size.
func (b *Browser) metricsAction(g grid.Grid) chromedp.Action {
	w, h := g.PixelSize()
	cssW := int64(math.Round(float64(w) / b.zoom))
	cssH := int64(math.Round(float64(h) / b.zoom))
	return emulation.SetDeviceMetricsOverride(cssW, cssH, b.zoom, false)
}

// Resize changes the viewport to match a new grid. A running screencast
// is restarted at the new size.
func (b *Browser) Resize(g grid.Grid) error {
	b.mu.Lock()
	b.grid = g
	casting := b.casting
	b.mu.Unlock()
	if err := chromedp.Run(b.ctx, b.metricsAction(g)); err != nil {
		return err
	}
	if casting {
		return b.restartCast(b.ctx)
	}
	return nil
}

// Zoom returns the page zoom factor.
func (b *Browser) Zoom() float64 { return b.zoom }

// ScrollHeight returns the full document height in CSS pixels.
func (b *Browser) ScrollHeight() (int, error) {
	var h float64
	err := chromedp.Run(b.ctx, chromedp.Evaluate(
		`Math.max(document.documentElement ? document.documentElement.scrollHeight : 0, document.body ? document.body.scrollHeight : 0)`, &h))
	return int(math.Ceil(h)), err
}

// Screenshot captures the viewport, exactly the grid's pixel size. Up to
// maxShotPixels it is one capture, returned as decoded. Beyond that the
// viewport is captured in vertical tiles, each a whole number of grid rows,
// through the clip parameter (CSS pixels), and stitched into one grayscale
// image holding the Rec. 709 luma of every pixel: one byte per pixel keeps
// a 6400 x 60000 page under 400 MB.
func (b *Browser) Screenshot() (image.Image, error) {
	g := b.Grid()
	devW, devH := g.PixelSize()
	if devW*devH <= maxShotPixels {
		return b.shot(nil, devW*devH)
	}
	tile := maxShotPixels / devW / g.CellH * g.CellH
	if tile < g.CellH {
		tile = g.CellH
	}
	out := image.NewGray(image.Rect(0, 0, devW, devH))
	for y := 0; y < devH; y += tile {
		h := tile
		if y+h > devH {
			h = devH - y
		}
		clip := &page.Viewport{X: 0, Y: float64(y) / b.zoom, Width: float64(devW) / b.zoom, Height: float64(h) / b.zoom, Scale: 1}
		img, err := b.shot(clip, devW*h)
		if err != nil {
			return nil, fmt.Errorf("tile at row %d: %w", y/g.CellH, err)
		}
		drawLuma(out, img, y)
	}
	return out, nil
}

// shot takes one capture, of the whole viewport or of a clip, and decodes it.
func (b *Browser) shot(clip *page.Viewport, pixels int) (image.Image, error) {
	format := page.CaptureScreenshotFormatPng
	switch {
	case b.shotFormat == "jpeg", b.shotFormat != "png" && pixels > jpegAbovePixels:
		format = page.CaptureScreenshotFormatJpeg
	}
	var data []byte
	err := chromedp.Run(b.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		p := page.CaptureScreenshot().WithFormat(format).WithCaptureBeyondViewport(false)
		if format == page.CaptureScreenshotFormatJpeg {
			p = p.WithQuality(90)
		}
		if clip != nil {
			p = p.WithClip(clip)
		}
		var err error
		data, err = p.Do(ctx)
		return err
	}))
	if err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

// drawLuma writes the Rec. 709 luma of src into dst starting at row y0.
func drawLuma(dst *image.Gray, src image.Image, y0 int) {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > dst.Rect.Dx() {
		w = dst.Rect.Dx()
	}
	if y0+h > dst.Rect.Dy() {
		h = dst.Rect.Dy() - y0
	}
	luma := func(r, g, bl uint32) uint8 { return uint8((2126*r + 7152*g + 722*bl + 5000) / 10000) }
	for y := 0; y < h; y++ {
		row := dst.Pix[(y0+y)*dst.Stride : (y0+y)*dst.Stride+w]
		switch im := src.(type) {
		case *image.RGBA:
			p := im.Pix[im.PixOffset(b.Min.X, b.Min.Y+y):]
			for x := 0; x < w; x++ {
				row[x] = luma(uint32(p[4*x]), uint32(p[4*x+1]), uint32(p[4*x+2]))
			}
		case *image.NRGBA:
			p := im.Pix[im.PixOffset(b.Min.X, b.Min.Y+y):]
			for x := 0; x < w; x++ {
				row[x] = luma(uint32(p[4*x]), uint32(p[4*x+1]), uint32(p[4*x+2]))
			}
		case *image.YCbCr:
			for x := 0; x < w; x++ {
				yy := im.Y[im.YOffset(b.Min.X+x, b.Min.Y+y)]
				ci := im.COffset(b.Min.X+x, b.Min.Y+y)
				r, g, bl := color.YCbCrToRGB(yy, im.Cb[ci], im.Cr[ci])
				row[x] = luma(uint32(r), uint32(g), uint32(bl))
			}
		case *image.Gray:
			copy(row, im.Pix[im.PixOffset(b.Min.X, b.Min.Y+y):im.PixOffset(b.Min.X, b.Min.Y+y)+w])
		default:
			for x := 0; x < w; x++ {
				r, g, bl, _ := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
				row[x] = luma(r>>8, g>>8, bl>>8)
			}
		}
	}
}

// PageState is what the UI needs to know about the page besides its
// pixels and text: where it is, whether a text field has focus, and how
// far it is scrolled.
type PageState struct {
	// Fingerprint summarises the layout: scroll position, document height
	// and element count. Two captures taken while it is unchanged share
	// the same geometry.
	Fingerprint string  `json:"fp"`
	URL         string  `json:"url"`
	Title       string  `json:"title"`
	Focus       string  `json:"focus"`      // "input" while a text field or editable element has focus
	Fullscreen  bool    `json:"fullscreen"` // an element, usually a video player, is fullscreen
	BgLuma      float64 `json:"bgLuma"`     // luminance of the page background, 0 black to 1 white
	// Videos are the boxes of the videos on screen, in CSS pixels of the
	// viewport, for renderers that do not take a layout snapshot.
	Videos       []VideoRect `json:"videos"`
	CaretLeft    float64     `json:"caretLeft"` // content box of the focused field, CSS pixels
	CaretTop     float64     `json:"caretTop"`
	CaretHeight  float64     `json:"caretHeight"`
	CaretIndex   int         `json:"caretIndex"` // characters before the caret
	ScrollY      float64     `json:"scrollY"`
	ScrollHeight float64     `json:"scrollHeight"`
	ViewportH    float64     `json:"viewportH"`
}

const pageStateJS = `(() => {
  const de = document.documentElement, ae = document.activeElement;
  const tag = ae ? ae.tagName : '';
  const textTypes = !['button', 'submit', 'reset', 'checkbox', 'radio', 'file', 'image', 'range', 'color'].includes(ae ? ae.type : '');
  let focus = (tag === 'INPUT' && textTypes) || tag === 'TEXTAREA' || tag === 'SELECT' || (ae && ae.isContentEditable) ? 'input' : '';
  if (!focus && (tag === 'VIDEO' || tag === 'AUDIO')) focus = 'video';
  // A player usually puts the focus on a container of the video, not on
  // the element itself, and listens for the keys there. The body holding
  // a video somewhere does not count: that would be every page with one.
  if (!focus && ae && ae !== document.body && ae !== de && ae.querySelector && ae.querySelector('video')) focus = 'video';
  let caretLeft = 0, caretTop = 0, caretHeight = 0, caretIndex = 0;
  if (focus === 'input' && ae) {
    const r = ae.getBoundingClientRect(), cs = getComputedStyle(ae);
    caretLeft = r.left + parseFloat(cs.paddingLeft || 0) + parseFloat(cs.borderLeftWidth || 0);
    caretTop = r.top;
    caretHeight = r.height;
    try { caretIndex = ae.selectionStart == null ? (ae.value || '').length : ae.selectionStart; } catch (e) { caretIndex = 0; }
  }
  return {
    fullscreen: !!document.fullscreenElement,
    videos: [...document.querySelectorAll('video')].map(v => v.getBoundingClientRect())
      .filter(r => r.width > 16 && r.height > 16 && r.bottom > 0 && r.top < innerHeight && r.right > 0 && r.left < innerWidth)
      .map(r => ({ l: r.left, t: r.top, w: r.width, h: r.height })),
    bgLuma: (() => {
      // The page background: the first opaque one among body and html,
      // then the canvas default, white.
      for (const el of [document.body, document.documentElement]) {
        if (!el) continue;
        const m = getComputedStyle(el).backgroundColor.match(/[\d.]+/g);
        if (!m || m.length < 3) continue;
        const a = m.length > 3 ? parseFloat(m[3]) : 1;
        if (a < 0.5) continue;
        const f = c => { c /= 255; return c <= 0.04045 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4); };
        return 0.2126 * f(+m[0]) + 0.7152 * f(+m[1]) + 0.0722 * f(+m[2]);
      }
      return 1;
    })(),
    fp: [scrollX, scrollY, de ? de.scrollHeight : 0, document.getElementsByTagName('*').length, document.fullscreenElement ? 1 : 0].join(','),
    url: location.href, title: document.title, focus,
    caretLeft, caretTop, caretHeight, caretIndex,
    scrollY: scrollY, scrollHeight: de ? de.scrollHeight : 0, viewportH: innerHeight
  };
})()`

// State reads the current PageState.
func (b *Browser) State() (PageState, error) {
	var st PageState
	err := chromedp.Run(b.ctx, chromedp.Evaluate(pageStateJS, &st))
	return st, err
}

func (b *Browser) fingerprint() (string, error) {
	st, err := b.State()
	return st.Fingerprint, err
}

// WaitQuiet returns once the layout fingerprint has stayed the same for
// interval, or after max. Use it after a navigation so a dump reflects the
// page after its late scripts (banners, lazy content) have run.
func (b *Browser) WaitQuiet(interval, max time.Duration) {
	deadline := time.Now().Add(max)
	last, _ := b.fingerprint()
	for time.Now().Before(deadline) {
		select {
		case <-time.After(interval):
		case <-b.ctx.Done():
			return
		}
		fp, err := b.fingerprint()
		if err != nil {
			return
		}
		if fp == last {
			return
		}
		last = fp
	}
}

// CaptureStats reports how a Capture went.
type CaptureStats struct {
	Screenshot time.Duration // last screenshot, decode included
	Walker     time.Duration // last text pass
	Attempts   int           // 1 unless the layout moved in between
	Source     string        // where the text came from: "walk", "snap" or "ui"
	Prep       time.Duration // media preparation, only for the ui renderer
	Snapshot   time.Duration // the DOM snapshot itself
	Crops      time.Duration // the media crops
	NCrops     int
}

// Page is one capture of the page: the pixels (nil for the interface
// renderer), the text, the interface layer, and the page state.
type Page struct {
	Img   image.Image
	Runs  []frame.TextRun
	UI    []frame.UIBox
	State PageState
	Stats CaptureStats
}

// Capture reads the page, retrying when the layout changes in between so
// every layer describes the same geometry. The interface renderer takes
// no screenshot: its frames, widgets and text all come from one snapshot.
func (b *Browser) Capture(withText bool) (Page, error) {
	const attempts = 3
	var p Page
	ui := b.renderer == "ui"
	for i := 0; i < attempts; i++ {
		p.Stats.Attempts = i + 1
		before, ferr := b.fingerprint()
		if !ui {
			t0 := time.Now()
			img, err := b.Screenshot()
			if err != nil {
				return p, err
			}
			p.Img = img
			p.Stats.Screenshot = time.Since(t0)
		}
		if withText || ui {
			t1 := time.Now()
			var err error
			if ui {
				p.Stats.Source = "ui"
				p.Runs, p.UI, err = b.snapshotUI(&p.Stats)
			} else if b.useSnapshot() {
				p.Stats.Source = "snap"
				p.Runs, err = b.SnapshotRuns()
			} else {
				p.Stats.Source = "walk"
				p.Runs, err = b.WalkerRuns()
			}
			if err != nil {
				return p, err
			}
			p.Stats.Walker = time.Since(t1)
		}
		var ferr2 error
		p.State, ferr2 = b.State()
		if ferr != nil || ferr2 != nil || before == p.State.Fingerprint {
			return p, nil
		}
	}
	return p, nil // still moving after several tries: use the last set
}

// ProfileNote is non-empty when the wanted profile could not be used.
func (b *Browser) ProfileNote() string { return b.profileNote }

// SetScreenRows tells the browser how tall one screenful is, which is
// what the "auto" renderer weighs against the frame height.
func (b *Browser) SetScreenRows(n int) {
	b.mu.Lock()
	b.screenRows = n
	b.mu.Unlock()
}

// useSnapshot reports whether the text comes from the snapshot. The
// walker measures only the characters on screen, so it wins on a short
// frame; the snapshot serializes the whole document at a fixed cost, so
// it wins once the frame is several screens tall.
func (b *Browser) useSnapshot() bool {
	switch b.renderer {
	case "snap", "ui":
		return true
	case "shot":
		return false
	}
	b.mu.Lock()
	screen := b.screenRows
	rows := b.grid.Rows
	b.mu.Unlock()
	if screen <= 0 {
		screen = rows
	}
	return rows >= 4*screen
}

// TextRuns reads the page text with the configured source.
func (b *Browser) TextRuns() ([]frame.TextRun, error) {
	if b.useSnapshot() {
		return b.SnapshotRuns()
	}
	return b.WalkerRuns()
}

// WalkerRuns measures the page text in the page, character by character.
func (b *Browser) WalkerRuns() ([]frame.TextRun, error) {
	g := b.Grid()
	var runs []frame.TextRun
	expr := textWalkerExpr(float64(g.CellW)/b.zoom, float64(g.CellH)/b.zoom, g.Cols, g.Rows, b.opacity)
	err := chromedp.Run(b.ctx, chromedp.Evaluate(expr, &runs))
	return runs, err
}

// URL returns the current document URL.
func (b *Browser) URL() (string, error) {
	var u string
	err := chromedp.Run(b.ctx, chromedp.Location(&u))
	return u, err
}

// Title returns the current document title.
func (b *Browser) Title() (string, error) {
	var t string
	err := chromedp.Run(b.ctx, chromedp.Title(&t))
	return t, err
}

// Context is the tab's chromedp context, for callers that need raw CDP.
func (b *Browser) Context() context.Context { return b.ctx }

// domQuietJS waits for the page to stop rebuilding itself. A load event
// says the document arrived, not that the application finished drawing:
// a client-rendered page fires it while still showing its placeholders.
// A MutationObserver watches the whole tree and the wait ends after the
// first stretch with no mutations, or at the timeout.
const domQuietJS = `(quiet, timeout, minText) => new Promise(done => {
  const start = performance.now();
  let timer = null, observer = null;
  const textLen = () => (document.body ? document.body.innerText : '').trim().length;
  const finish = why => {
    if (observer) observer.disconnect();
    if (timer) clearTimeout(timer);
    done({ why, ms: Math.round(performance.now() - start), text: textLen() });
  };
  // A quiet stretch with nothing to read means the application has not
  // drawn yet, only stopped touching the DOM for a moment: keep waiting.
  const settle = () => { if (textLen() >= minText) finish('quiet'); else arm(); };
  const arm = () => { if (timer) clearTimeout(timer); timer = setTimeout(settle, quiet); };
  observer = new MutationObserver(arm);
  // Structure and text only: a carousel or a marquee rewrites style and
  // class attributes forever, and would keep the page "building" until
  // the timeout every time.
  try {
    observer.observe(document.documentElement || document, {
      childList: true, subtree: true, characterData: true });
  } catch (e) { return finish('no observer'); }
  arm();
  setTimeout(() => finish('timeout'), timeout);
})`

// DOMQuiet reports how waiting for the page to settle went.
type DOMQuiet struct {
	Why  string `json:"why"`  // "quiet", "timeout" or "no observer"
	Ms   int    `json:"ms"`   // how long the wait took, in the page's clock
	Text int    `json:"text"` // characters of text the document ended up with
}

// WaitDOMQuiet waits until the page stops changing its own DOM: quiet is
// the stretch of calm that ends the wait, max the longest it may take,
// and minText how much readable text a quiet page must already have for
// the wait to count as finished.
func (b *Browser) WaitDOMQuiet(quiet, max time.Duration, minText int) (DOMQuiet, error) {
	// A page that replaces itself by script while it is being watched, a
	// redirect, a consent reload, takes the watcher with it: wait for the
	// new document and watch that one.
	var out DOMQuiet
	var err error
	for try := 0; try < 3; try++ {
		seq := b.NavSeq()
		out, err = b.waitDOMQuietOnce(quiet, max, minText)
		if err == nil {
			return out, nil
		}
		if b.ctx.Err() != nil {
			return out, err
		}
		if !b.FollowReload(seq, b.navTimeout) {
			time.Sleep(200 * time.Millisecond)
		}
	}
	return out, err
}

func (b *Browser) waitDOMQuietOnce(quiet, max time.Duration, minText int) (DOMQuiet, error) {
	var out DOMQuiet
	expr := fmt.Sprintf("(%s)(%d,%d,%d)", domQuietJS, quiet/time.Millisecond, max/time.Millisecond, minText)
	err := chromedp.Run(b.ctx, chromedp.Evaluate(expr, &out, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
		return p.WithAwaitPromise(true)
	}))
	return out, err
}

// opacityMode normalizes the Opacity option.
func opacityMode(m string) string {
	switch m {
	case "strict", "ignore":
		return m
	}
	return "pending"
}

// wakeJS scrolls the page from top to bottom one screen at a time, with a
// pause at each step, then back to where it was. With a viewport several
// screens tall every IntersectionObserver fires at once in the first
// instant, and whatever was not mounted yet misses it for good; a page
// that reveals its blocks on scroll, or loads its images lazily, stays
// half empty until someone actually scrolls. This is that someone. The
// page may grow while it is being scrolled, so both the steps and the
// time are capped.
const wakeJS = `(pause, maxSteps, maxMs, reach) => new Promise(done => {
  const start = performance.now();
  const de = document.documentElement;
  const height = () => {
    const d = document.documentElement, bd = document.body;
    return Math.max(d ? d.scrollHeight : 0, bd ? bd.scrollHeight : 0);
  };
  const step = Math.max(200, innerHeight);
  const y0 = scrollY;
  let y = 0, n = 0;
  // Back at the top, the blocks that were waiting for a scroll start
  // their fade-in: wait for every finite animation to end before the
  // page is read, or it is caught half transparent. Endless ones, a
  // marquee, a spinner, are not waited for.
  const settling = () => {
    try {
      return document.getAnimations().some(a => a.playState === 'running' &&
        a.effect && a.effect.getTiming && a.effect.getTiming().iterations !== Infinity);
    } catch (e) { return false; }
  };
  const finish = () => {
    window.scrollTo({ top: y0, left: 0, behavior: 'instant' });
    const until = performance.now() + Math.min(2500, Math.max(0, maxMs - (performance.now() - start)));
    const wait = () => {
      if (settling() && performance.now() < until) return setTimeout(wait, 50);
      done({ steps: n, height: height(), ms: Math.round(performance.now() - start) });
    };
    setTimeout(wait, pause);
  };
  const tick = () => {
    // Only as far as the frame reaches: waking forty screens to render one
    // cost five seconds a page. reach 0 means the whole document.
    const full = Math.max(0, height() - innerHeight);
    const max = reach > 0 ? Math.min(full, reach) : full;
    if (y >= max || n >= maxSteps || performance.now() - start > maxMs) {
      window.scrollTo({ top: max, left: 0, behavior: 'instant' });
      setTimeout(finish, pause);
      return;
    }
    y = Math.min(y + step, max);
    n++;
    window.scrollTo({ top: y, left: 0, behavior: 'instant' });
    setTimeout(tick, pause);
  };
  tick();
})`

// Wake reports how the wake-up scroll went.
type Wake struct {
	Steps  int `json:"steps"`  // screens scrolled through
	Height int `json:"height"` // document height at the end, CSS pixels
	Ms     int `json:"ms"`
}

// WakeUp scrolls through the whole page once and comes back, so lazy
// images load and scroll-revealed blocks appear before the first capture.
func (b *Browser) WakeUp(pause time.Duration, maxSteps int, max time.Duration) (Wake, error) {
	return b.WakeUpTo(pause, maxSteps, max, 0)
}

// WakeUpTo wakes the page down to reach CSS pixels below the top, or the
// whole page when reach is 0.
func (b *Browser) WakeUpTo(pause time.Duration, maxSteps int, max time.Duration, reach float64) (Wake, error) {
	var out Wake
	ctx, cancel := context.WithTimeout(b.ctx, max+3*time.Second)
	defer cancel()
	expr := fmt.Sprintf("(%s)(%d,%d,%d,%s)", wakeJS, pause/time.Millisecond, maxSteps, max/time.Millisecond, strconv.FormatFloat(reach, 'f', 0, 64))
	err := chromedp.Run(ctx, chromedp.Evaluate(expr, &out, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
		return p.WithAwaitPromise(true)
	}))
	return out, err
}

// plainUserAgent presents the browser as the Chrome it is rather than as
// HeadlessChrome, in the user agent and in the client hints alike. Many
// sites answer a headless user agent with a verification page and nothing
// else; the page itself is the same either way.
func plainUserAgent() chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		_, product, _, ua, _, err := cdpbrowser.GetVersion().Do(ctx)
		if err != nil {
			return err
		}
		ua = strings.Replace(ua, "HeadlessChrome/", "Chrome/", 1)
		full := strings.TrimPrefix(strings.TrimPrefix(product, "HeadlessChrome/"), "Chrome/")
		major := full
		if i := strings.IndexByte(full, '.'); i > 0 {
			major = full[:i]
		}
		brands := []*emulation.UserAgentBrandVersion{
			{Brand: "Chromium", Version: major},
			{Brand: "Google Chrome", Version: major},
			{Brand: "Not.A/Brand", Version: "99"},
		}
		fullList := []*emulation.UserAgentBrandVersion{
			{Brand: "Chromium", Version: full},
			{Brand: "Google Chrome", Version: full},
			{Brand: "Not.A/Brand", Version: "99.0.0.0"},
		}
		platform := "macOS"
		switch {
		case strings.Contains(ua, "Windows"):
			platform = "Windows"
		case strings.Contains(ua, "Linux"):
			platform = "Linux"
		}
		return emulation.SetUserAgentOverride(ua).
			WithAcceptLanguage("it-IT,it;q=0.9,en-US;q=0.8,en;q=0.7").
			WithUserAgentMetadata(&emulation.UserAgentMetadata{
				Brands: brands, FullVersionList: fullList,
				Platform: platform, PlatformVersion: "", Architecture: "arm", Model: "", Mobile: false,
			}).Do(ctx)
	})
}

// NavSeq counts the documents the main frame has started loading.
func (b *Browser) NavSeq() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.navSeq
}

// FollowReload waits for the new document when something since seq,
// typically a click, made the page navigate or reload itself: answering a
// consent banner often does. Counters rather than a signal, so a document
// that was ready before the wait began is not waited for again. It
// returns whether a new document came.
func (b *Browser) FollowReload(seq int64, max time.Duration) bool {
	if b.NavSeq() == seq {
		return false
	}
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) && b.ctx.Err() == nil {
		b.mu.Lock()
		ready := b.readySeq >= b.navSeq
		b.mu.Unlock()
		if ready {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	return true
}

// VideoRect is a video's box in CSS pixels, relative to the viewport.
type VideoRect struct {
	L float64 `json:"l"`
	T float64 `json:"t"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}
