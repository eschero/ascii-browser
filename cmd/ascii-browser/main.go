// ascii-browser renders real web pages as pure ASCII in the terminal.
// It drives a headless Chrome through the DevTools Protocol, forces every
// page onto a character grid, and maps the screenshot onto a character
// ramp by luminance. Architecture after Browsh (github.com/browsh-org/browsh).
package main

import (
	"context"
	"flag"
	"fmt"
	"html"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/eschero/ascii-browser/internal/ascii"
	"github.com/eschero/ascii-browser/internal/browser"
	"github.com/eschero/ascii-browser/internal/caption"
	"github.com/eschero/ascii-browser/internal/frame"
	"github.com/eschero/ascii-browser/internal/grid"
	"github.com/eschero/ascii-browser/internal/render"
)

// The wake-up scroll: how long to rest on each screen, and the caps that
// keep a page which grows as it is scrolled from holding things up.
const (
	wakePause    = 120 * time.Millisecond
	wakeMaxSteps = 40
	wakeMaxTime  = 6 * time.Second
)

// minPageText is how much text a page is expected to end up with: below
// it the wait keeps going, and a dump that stays under it is reported
// rather than written out empty in silence.
const minPageText = 200

// maxShotPixels lives in the browser package; the --full cap is in cells.

type config struct {
	url        string
	render     render.Options
	dumpPath   string
	once, full bool
	noText     bool
	links      bool
	zoom       float64
	cols, rows int // 0,0: auto (terminal size, or 80x24 with --once)
	maxCells   int
	shotFormat string
	logPath    string
	buffer     int
	image      string
	renderer   string
	uiMedia    bool
	uiBoxes    bool
	video      string
	videoFPS   float64
	note       string // startup note for the log, e.g. a ramp change
	chrome     string
	profile    string
	strictOpac bool
	domText    bool
	wake       string
	cookies    string
	darkPages  string
	audio      string
	quietWait  time.Duration
	show       bool
	timeout    time.Duration
	timing     bool
}

func main() {
	os.Exit(run())
}

func run() int {
	var (
		rampFlag = flag.String("ramp", "", "character ramp, light to dark, printable ASCII only (overrides --ramp-preset)")
		preset   = flag.String("ramp-preset", "standard", "ramp preset: standard (10 levels), short (6, more contrast), long (70)")
		structur = flag.Bool("structure", false, "draw strongly oriented cells as / \\ | - _ instead of a tonal character")
		edgeThr  = flag.Float64("edge-threshold", 0.12, "with --structure: oriented gradient strength above which a cell is an edge (0..1)")
		imageURL = flag.String("image", "", "render an image URL or file path wrapped in a white full-width page, like --once --full")
		renderer = flag.String("renderer", "auto", "how the page is read: shot (screenshot, text measured in the page), snap (screenshot, text from DOMSnapshot), ui (interface drawn from DOMSnapshot, no screenshot), auto (shot up to 4 screens of frame height, snap above)")
		noUIMed  = flag.Bool("no-ui-media", false, "with --renderer ui: draw media as a placeholder instead of photographing each one")
		uiMedMin = flag.String("ui-media-min", "4x2", "with --renderer ui: media smaller than COLSxROWS cells becomes a solid block")
		video    = flag.String("video", "live", "how a video is drawn: off (placeholder), poster (one frame, never refreshed), live (every decoded frame, rendered inside the page)")
		videoFPS = flag.Float64("video-fps", 0, "with --video live: cap on frames per second; 0 follows the video")
		invert   = flag.Bool("invert", false, "dense characters for bright pixels (phosphor) instead of dark ones (ink)")
		noDither = flag.Bool("no-dither", false, "disable Floyd-Steinberg dithering on the cell grid")
		paper    = flag.Float64("paper", 0.93, "luminance above which a cell is page background: blank, no error diffusion (0 disables)")
		dumpPath = flag.String("dump", "", "file written by Ctrl+D, and by --once (stdout when empty)")
		once     = flag.Bool("once", false, "render one frame, write the dump, exit; no terminal UI")
		full     = flag.Bool("full", false, "with --once: viewport as tall as the whole page, up to 2000 rows")
		size     = flag.String("size", "", "grid size as COLSxROWS, the page resolution; 0 follows the terminal (80x24 with --once, page height with --full)")
		maxCells = flag.Int("max-cells", 4_000_000, "with --full: cap on COLS*ROWS")
		shotFmt  = flag.String("shot-format", "auto", "screenshot encoding: png, jpeg, or auto (png up to 4M pixels, jpeg above)")
		filler   = flag.String("filler", "?", "character for runes with no ASCII transliteration (0x20..0x7E)")
		noText   = flag.Bool("no-text", false, "graphics only: do not overlay the page text")
		noHalo   = flag.Bool("no-halo", false, "no blank cell before and after text runs over dark or textured areas")
		zoom     = flag.Float64("zoom", 1, "page zoom: graphics scale by this factor, text stays one character per cell (0.5..4)")
		links    = flag.Bool("links", false, "with --once: list links and fields in Tab order on stderr")
		uiBoxes  = flag.Bool("ui-boxes", false, "with --once --renderer ui: list the interface boxes on stderr")
		chrome   = flag.String("chrome", "", "path to the Chrome/Chromium binary (default: autodetect)")
		strictOp = flag.Bool("strict-opacity", false, "treat an element at opacity 0 as hidden even while it is animating that opacity")
		domText  = flag.Bool("dom-text", false, "read text from the DOM even where the page paints nothing: ignore opacity when deciding what is visible (display, visibility and clipping still apply); for sites whose reveal animations never fire in a headless browser")
		audio    = flag.String("audio", "", "play sound: on|off (default on in the interactive browser, off with --once)")
		darkPgs  = flag.String("dark-pages", "invert", "a page with a dark background: invert (draw the background blank and the bright parts dense) or keep")
		capStyle = flag.String("caption", "", "with --image: write the picture as a caption for a phone, in a proportional font: braille (2x4 dots per character, the same width everywhere), ios or android (plain ASCII from the characters of equal width in that system font)")
		capWidth = flag.Int("caption-width", 0, "with --caption: characters per line (default: what fits on one line of a phone)")
		cookies  = flag.String("cookies", "reject", "answer cookie and consent banners: reject (the refusal, or the acceptance when there is no refusal), accept, or off to leave them")
		wake     = flag.String("wake", "", "scroll through the whole page once after loading, so lazy images and scroll-revealed blocks appear: on|off (default on with --once and --full, off in the interactive browser)")
		quiet    = flag.Duration("dom-quiet", 300*time.Millisecond, "wait for the page to stop rebuilding itself for this long before reading it; 0 to skip the wait")
		profile  = flag.String("profile", defaultProfileDir(), "directory Chrome keeps its profile in, so cookies and logins survive; empty for a throwaway profile")
		show     = flag.Bool("show", false, "run Chrome with a visible window (debug)")
		timeout  = flag.Duration("timeout", 10*time.Second, "navigation timeout")
		timing   = flag.Bool("timing", false, "print phase timings to stderr")
		logPath  = flag.String("log", "", "append timings and notes to this file (interactive mode: frames and inputs too)")
		buffer   = flag.Int("buffer", 3, "interactive mode: rendered frame height in screens; scrolling inside it needs no capture")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: ascii-browser [flags] URL
       ascii-browser [flags] --image URL-or-path

keys: arrows/PgUp/PgDn/Space/Home/End scroll, Tab/Shift+Tab cycle links and fields,
      Enter follow, Ctrl+L url bar, Ctrl+[ back (also Esc), Ctrl+] forward,
      Ctrl+R reload, Ctrl+D dump, Ctrl+Q quit; in a field: type, Esc leaves it.
mouse: left click, wheel scrolls.

flags:
`)
		flag.PrintDefaults()
	}
	flag.Parse()
	if (flag.NArg() != 1 && *imageURL == "") || (flag.NArg() != 0 && *imageURL != "") {
		flag.Usage()
		return 2
	}

	cfg := config{
		url:        normalizeURL(flag.Arg(0)),
		dumpPath:   *dumpPath,
		once:       *once || *imageURL != "",
		full:       *full || *imageURL != "",
		image:      *imageURL,
		renderer:   *renderer,
		noText:     *noText,
		links:      *links,
		zoom:       *zoom,
		chrome:     *chrome,
		profile:    *profile,
		strictOpac: *strictOp,
		domText:    *domText,
		wake:       *wake,
		cookies:    *cookies,
		darkPages:  *darkPgs,
		audio:      *audio,
		quietWait:  *quiet,
		show:       *show,
		maxCells:   *maxCells,
		shotFormat: *shotFmt,
		logPath:    *logPath,
		buffer:     *buffer,
		timeout:    *timeout,
		timing:     *timing,
	}
	var err error
	ramp, ok := render.Presets[*preset]
	if !ok {
		return fail("--ramp-preset: unknown preset %q (standard, short, long)", *preset)
	}
	if *rampFlag != "" {
		if ramp, err = render.ParseRamp(*rampFlag); err != nil {
			return fail("--ramp: %v", err)
		}
	}
	if *edgeThr < 0 || *edgeThr > 1 {
		return fail("--edge-threshold must be in 0..1")
	}
	if *structur {
		// Edge glyphs must not double as tones.
		if clean := ramp.Without(render.StructureGlyphs); len(clean) != len(ramp) {
			if len(clean) < 2 {
				return fail("--structure leaves the ramp %q with fewer than 2 characters", ramp)
			}
			cfg.note = fmt.Sprintf("--structure: ramp %q reduced to %q, structural glyphs removed", ramp, clean)
			ramp = clean
		}
	}
	if len(*filler) != 1 || !ascii.IsPrintable((*filler)[0]) {
		return fail("--filler must be one character in 0x20..0x7E")
	}
	if *paper < 0 || *paper > 1 {
		return fail("--paper must be in 0..1")
	}
	if cfg.zoom < 0.5 || cfg.zoom > 4 {
		return fail("--zoom must be in 0.5..4")
	}
	if cfg.full && !cfg.once {
		return fail("--full is only valid with --once")
	}
	if *size != "" {
		if cfg.cols, cfg.rows, err = parseSize(*size); err != nil {
			return fail("--size: %v", err)
		}
	}
	switch cfg.renderer {
	case "shot", "snap", "ui", "auto":
	default:
		return fail("--renderer must be shot, snap, ui or auto")
	}
	cfg.uiMedia = !*noUIMed
	if cfg.render.MediaMinW, cfg.render.MediaMinH, err = parseSize(*uiMedMin); err != nil {
		return fail("--ui-media-min: %v", err)
	}
	cfg.uiBoxes = *uiBoxes
	if cfg.strictOpac && cfg.domText {
		return fail("--strict-opacity and --dom-text contradict each other")
	}
	switch cfg.wake {
	case "", "on", "off":
	default:
		return fail("--wake must be on or off")
	}
	switch cfg.audio {
	case "", "on", "off":
	default:
		return fail("--audio must be on or off")
	}
	switch cfg.darkPages {
	case "invert", "keep":
	default:
		return fail("--dark-pages must be invert or keep")
	}
	if *capStyle != "" {
		return runCaption(*capStyle, *capWidth, cfg)
	}
	switch cfg.cookies {
	case "reject", "accept", "off":
	default:
		return fail("--cookies must be reject, accept or off")
	}
	cfg.video, cfg.videoFPS = *video, *videoFPS
	switch cfg.video {
	case "off", "poster", "live":
	default:
		return fail("--video must be off, poster or live")
	}
	if cfg.videoFPS < 0 {
		return fail("--video-fps cannot be negative")
	}
	if cfg.image != "" && cfg.renderer != "shot" {
		cfg.note = "--image always uses the shot renderer"
		cfg.renderer = "shot"
	}
	if cfg.buffer < 1 || cfg.buffer > 20 {
		return fail("--buffer must be in 1..20")
	}
	if cfg.maxCells < 1 {
		return fail("--max-cells must be positive")
	}
	switch cfg.shotFormat {
	case "auto", "png", "jpeg":
	default:
		return fail("--shot-format must be auto, png or jpeg")
	}
	cfg.render = render.Options{Ramp: ramp, Dither: !*noDither, Invert: *invert, Paper: *paper, Filler: (*filler)[0], Halo: !*noHalo,
		Structure: *structur, EdgeThreshold: *edgeThr}

	if cfg.profile != "" {
		if err := os.MkdirAll(cfg.profile, 0o700); err != nil {
			return fail("--profile: %v", err)
		}
	}
	if cfg.image != "" {
		wrapper, cleanup, err := imagePage(cfg.image)
		if err != nil {
			return fail("--image: %v", err)
		}
		defer cleanup()
		cfg.url = wrapper
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if cfg.once {
		return runOnce(ctx, cfg)
	}
	return runTUI(ctx, cfg)
}

func fail(format string, args ...interface{}) int {
	fmt.Fprintf(os.Stderr, "ascii-browser: "+format+"\n", args...)
	return 2
}

// imagePage writes a temporary HTML page that shows one image at full
// width on a white ground, without margins, and returns its file:// URL.
// A file page may load both remote and local images; a data: page could
// not load local ones. The image's error handler marks the title so the
// failure can be reported instead of dumping a blank page.
func imagePage(src string) (url string, cleanup func(), err error) {
	if !strings.Contains(src, "://") && !strings.HasPrefix(src, "data:") {
		abs, err := filepath.Abs(src)
		if err != nil {
			return "", nil, err
		}
		if _, err := os.Stat(abs); err != nil {
			return "", nil, err
		}
		src = "file://" + abs
	}
	f, err := os.CreateTemp("", "ascii-image-*.html")
	if err != nil {
		return "", nil, err
	}
	page := `<!doctype html><html><head><meta charset="utf-8"><title>image</title><style>html,body{margin:0;padding:0;background:#fff}img{display:block;width:100%;height:auto}</style></head><body><img src="` +
		html.EscapeString(src) + `" alt="" onerror="document.title='image-error'"></body></html>`
	if _, err := f.WriteString(page); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, err
	}
	f.Close()
	return "file://" + f.Name(), func() { os.Remove(f.Name()) }, nil
}

func normalizeURL(u string) string {
	if strings.Contains(u, "://") || strings.HasPrefix(u, "about:") || strings.HasPrefix(u, "data:") {
		return u
	}
	return "https://" + u
}

// parseSize reads COLSxROWS; 0 means "decided elsewhere": the terminal in
// interactive mode, 80x24 with --once, the page height with --full.
func parseSize(s string) (cols, rows int, err error) {
	if _, err = fmt.Sscanf(s, "%dx%d", &cols, &rows); err != nil || cols < 0 || rows < 0 {
		return 0, 0, fmt.Errorf("want COLSxROWS, got %q", s)
	}
	return cols, rows, nil
}

// fullRows is the row count for a page of hCSS CSS pixels at the given
// zoom, capped so that cols*rows stays within maxCells.
func fullRows(hCSS int, zoom float64, cellH, cols, maxCells int) int {
	rows := int(math.Ceil(float64(hCSS) * zoom / float64(cellH)))
	if cap := maxCells / cols; rows > cap {
		rows = cap
	}
	if rows < 1 {
		rows = 1
	}
	return rows
}

// defaultProfileDir is where the browser keeps its profile unless told
// otherwise: one directory per user, so a consent accepted today is still
// accepted tomorrow.
func defaultProfileDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "ascii-browser", "profile")
}

func newBrowser(ctx context.Context, cfg config, g grid.Grid, screenRows int) (*browser.Browser, error) {
	return browser.New(ctx, browser.Options{
		ChromePath: cfg.chrome,
		ProfileDir: cfg.profile,
		Opacity:    cfg.opacityMode(),
		Audio:      cfg.audio == "on" || (cfg.audio == "" && !cfg.once && cfg.image == ""),
		Grid:       g,
		Headless:   !cfg.show,
		NavTimeout: cfg.timeout,
		Zoom:       cfg.zoom,
		ShotFormat: cfg.shotFormat,
		Renderer:   cfg.renderer,
		ScreenRows: screenRows,
		UIMedia:    cfg.uiMedia && cfg.renderer == "ui",
		Video:      cfg.video,
		VideoFPS:   cfg.videoFPS,
	})
}

// runOnce renders a single frame and writes the dump.
func runOnce(ctx context.Context, cfg config) int {
	t0 := time.Now()
	if cfg.note != "" {
		if cfg.logPath != "" {
			if f, err := os.OpenFile(cfg.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				fmt.Fprintf(f, "%s %s\n", time.Now().Format("15:04:05.000"), cfg.note)
				f.Close()
			}
		}
		if cfg.timing {
			fmt.Fprintln(os.Stderr, cfg.note)
		}
	}
	cols, rows := cfg.cols, cfg.rows
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24 // with --full, the initial viewport before it grows
	}
	screenRows := rows
	g := grid.New(cols, rows)
	b, err := newBrowser(ctx, cfg, g, screenRows)
	if err != nil {
		return fail("%v", err)
	}
	defer b.Close()
	tStart := time.Now()

	if err := b.Navigate(cfg.url); err != nil {
		return fail("%v", err)
	}
	if cfg.image != "" {
		if title, _ := b.Title(); title == "image-error" {
			return fail("--image: %s could not be loaded", cfg.image)
		}
	}
	tNav := time.Now()

	// A load event says the document arrived, not that a client-rendered
	// page finished building itself, so wait for the DOM to go quiet too.
	docText := -1 // characters of text in the document, when measured
	if cfg.quietWait > 0 {
		if q, err := b.WaitDOMQuiet(cfg.quietWait, 5*time.Second, minPageText); err == nil {
			docText = q.Text
			if cfg.timing {
				fmt.Fprintf(os.Stderr, "dom quiet: %s after %d ms, %d characters of text\n", q.Why, q.Ms, q.Text)
			}
		}
	}
	// The consent banner comes first: it covers the page, and some pages
	// only build the rest once it is answered.
	if cfg.cookies != "off" {
		tc := time.Now()
		if got, err := b.DismissConsent(cfg.cookies, 3*time.Second); err == nil && len(got) > 0 {
			if cfg.timing {
				fmt.Fprintf(os.Stderr, "consent: clicked %q in %v\n", got, time.Since(tc).Round(time.Millisecond))
			}
			if cfg.quietWait > 0 {
				b.WaitDOMQuiet(cfg.quietWait, followUpWait(b.ConsentReloaded()), minTextAfter(b.ConsentReloaded()))
			}
		}
	}
	// Scroll through the page once so lazy images load and blocks that
	// appear on scroll appear, then let what that started settle. Only as
	// far as the frame reaches, the whole page with --full.
	if cfg.wake != "off" {
		tw := time.Now()
		reach := float64(g.Rows*g.CellH) / cfg.zoomOr1()
		if cfg.full {
			reach = 0
		}
		if w, err := b.WakeUpTo(wakePause, wakeMaxSteps, wakeMaxTime, reach); err == nil {
			if cfg.timing {
				fmt.Fprintf(os.Stderr, "wake: %d screens, page %d px tall, %v\n", w.Steps, w.Height, time.Since(tw).Round(time.Millisecond))
			}
			// The wake already waited for its animations; only a long
			// scroll can have mounted enough to be worth another look.
			if cfg.quietWait > 0 && w.Steps > 2 {
				b.WaitDOMQuiet(cfg.quietWait, followUpWait(false), 0)
			}
		} else if cfg.timing {
			fmt.Fprintf(os.Stderr, "wake: %v\n", err)
		}
	}
	// Measured after the page has built itself and been woken: both can
	// change its height.
	if cfg.full {
		if g, err = growToPage(b, g, cfg.maxCells); err != nil {
			return fail("%v", err)
		}
	}
	// Late scripts (banners, lazy images) keep moving the page for a while
	// after load; a dump should show the settled page.
	b.WaitQuiet(100*time.Millisecond, time.Second)
	tQuiet := time.Now()

	page, err := b.Capture(!cfg.noText)
	if err != nil {
		return fail("capture: %v", err)
	}
	// A capture with next to no text from a document full of it was taken
	// while the page was still replacing itself, a redirect by script, a
	// client router mounting the real view: let it settle and read again.
	for try := 0; try < 2 && !cfg.noText && cfg.image == "" && (docText >= minPageText || docText < 0) && runChars(page.Runs) < 40; try++ {
		b.WaitSettled(2 * time.Second)
		if cfg.quietWait > 0 {
			b.WaitDOMQuiet(cfg.quietWait, 3*time.Second, minPageText)
		}
		if p2, err := b.Capture(!cfg.noText); err == nil {
			page = p2
		}
	}
	runs, stats := page.Runs, page.Stats
	tText := time.Now()

	r := render.New(cfg.render)
	r.SetInvert(cfg.invertFor(page.State))
	var f *frame.Frame
	if cfg.renderer == "ui" {
		f = r.RenderUI(page.UI, g, runs)
	} else {
		f = r.Render(page.Img, g, runs)
	}
	tRender := time.Now()

	var out io.Writer = os.Stdout
	if cfg.dumpPath != "" {
		fh, err := os.Create(cfg.dumpPath)
		if err != nil {
			return fail("%v", err)
		}
		defer fh.Close()
		out = fh
	}
	if _, err := f.WriteTo(out); err != nil {
		return fail("write dump: %v", err)
	}
	if cfg.uiBoxes {
		for i, bx := range page.UI {
			fmt.Fprintf(os.Stderr, "box %3d %-8s (%3d,%3d) %3dx%-3d paint=%-5d %q\n",
				i, bx.Kind, bx.X, bx.Y, bx.W, bx.H, bx.Paint, bx.Text)
		}
	}
	// A document that ended up with almost no text is worth a word: it
	// may build itself after loading, or serve a check page to a headless
	// browser. An image page has none by design.
	if docText >= 0 && docText < minPageText && cfg.image == "" {
		fmt.Fprintf(os.Stderr, "ascii-browser: the document has only %d characters of text; if the page builds itself after loading try a longer --dom-quiet, and --show if it may be checking for a real browser\n", docText)
	}
	if cfg.links {
		for i, it := range collectFocusables(runs) {
			r := runs[it.runs[0]]
			kind := it.href
			switch {
			case it.field:
				kind = "field: " + it.text
			case it.button:
				kind = "button: " + it.text
			}
			fmt.Fprintf(os.Stderr, "%3d  (%3d,%3d)  %-30q  %s\n", i+1, r.X, r.Y, it.text, kind)
		}
	}
	if cfg.timing && stats.Source == "ui" {
		fmt.Fprintf(os.Stderr, "ui detail: media prep %v, snapshot %v, %d crops %v\n",
			stats.Prep.Round(time.Millisecond), stats.Snapshot.Round(time.Millisecond),
			stats.NCrops, stats.Crops.Round(time.Millisecond))
	}
	if cfg.timing {
		chars := 0
		for _, r := range runs {
			chars += len([]rune(r.Text))
		}
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		w, h := g.PixelSize()
		fmt.Fprintf(os.Stderr, "chrome start %v, navigate %v, quiet %v, screenshot %v, text %v via %s (%d runs, %d chars, %d attempt(s)), render %v, total %v\n",
			tStart.Sub(t0).Round(time.Millisecond), tNav.Sub(tStart).Round(time.Millisecond),
			tQuiet.Sub(tNav).Round(time.Millisecond), stats.Screenshot.Round(time.Millisecond),
			stats.Walker.Round(time.Millisecond), stats.Source, len(runs), chars, stats.Attempts,
			tRender.Sub(tText).Round(time.Millisecond), tRender.Sub(t0).Round(time.Millisecond))
		fmt.Fprintf(os.Stderr, "grid %dx%d (%d cells), zoom %g, renderer %s, screenshot %dx%d px (%T), ui boxes %d, heap %d MB, peak %d MB, sys %d MB\n",
			g.Cols, g.Rows, g.Cells(), cfg.zoom, cfg.renderer, w, h, page.Img, len(page.UI),
			ms.HeapInuse>>20, ms.HeapSys>>20, ms.Sys>>20)
	}
	return 0
}

// growToPage resizes the viewport to the full document height, rounded up
// to whole rows and capped at maxCells cells in total. A taller viewport
// can make the page reflow (lazy content loads), so the height is read
// twice. The document height is in CSS pixels; one row is CellH/zoom of
// them.
func growToPage(b *browser.Browser, g grid.Grid, maxCells int) (grid.Grid, error) {
	prev := -1
	for i := 0; i < 2; i++ {
		h, err := b.ScrollHeight()
		if err != nil {
			return g, err
		}
		rows := fullRows(h, b.Zoom(), g.CellH, g.Cols, maxCells)
		if rows == g.Rows || h == prev {
			break
		}
		prev = h
		g = grid.New(g.Cols, rows)
		if err := b.Resize(g); err != nil {
			return g, err
		}
		b.WaitSettled(300 * time.Millisecond)
	}
	return g, nil
}

// opacityMode maps the two opacity flags onto the browser's option.
func (cfg config) opacityMode() string {
	switch {
	case cfg.domText:
		return "ignore"
	case cfg.strictOpac:
		return "strict"
	}
	return "pending"
}

// zoomOr1 is the page zoom, 1 when unset.
func (cfg config) zoomOr1() float64 {
	if cfg.zoom <= 0 {
		return 1
	}
	return cfg.zoom
}

// runChars counts the characters of text in a set of runs.
func runChars(runs []frame.TextRun) int {
	n := 0
	for _, r := range runs {
		n += len([]rune(r.Text))
	}
	return n
}

// darkBackground is the page background luminance below which a page is
// drawn inverted: YouTube's dark theme is 0.005, a light grey page 0.8.
const darkBackground = 0.2

// invertFor decides the mapping for a page: --invert flips it by hand, and
// a dark page flips it again, so its background comes out blank and its
// text readable instead of drowned in the densest characters.
func (cfg config) invertFor(st browser.PageState) bool {
	inv := cfg.render.Invert
	if cfg.darkPages == "invert" && st.URL != "" && st.BgLuma < darkBackground {
		inv = !inv
	}
	return inv
}

// followUpWait caps a DOM quiet wait after the page has been built once:
// a player updating its clock, a ticker, a carousel never let the DOM go
// quiet, and every follow-up wait would run to its ceiling. Only a page
// that reloaded, after a consent answer, is waited for in full.
func followUpWait(reloaded bool) time.Duration {
	if reloaded {
		return 3 * time.Second
	}
	return 600 * time.Millisecond
}

func minTextAfter(reloaded bool) int {
	if reloaded {
		return minPageText
	}
	return 0
}

// runCaption writes a picture as caption text: no browser, no terminal.
func runCaption(style string, width int, cfg config) int {
	st, ok := caption.Styles[style]
	if !ok {
		return fail("--caption must be braille, ios or android")
	}
	if cfg.image == "" {
		return fail("--caption needs --image URL-or-path")
	}
	img, err := caption.Load(cfg.image)
	if err != nil {
		return fail("--image: %v", err)
	}
	if width <= 0 {
		width = st.PhoneWidth()
	}
	out := caption.Render(img, st, width, caption.Limit, cfg.render.Invert) + "\n"
	if cfg.dumpPath == "" {
		os.Stdout.WriteString(out)
	} else if err := os.WriteFile(cfg.dumpPath, []byte(out), 0o644); err != nil {
		return fail("--dump: %v", err)
	}
	lines := strings.Count(out, "\n")
	fmt.Fprintf(os.Stderr, "caption %s: %d characters on %d lines of %d, limit %d\n",
		style, len([]rune(out))-1, lines, len([]rune(strings.SplitN(out, "\n", 2)[0])), caption.Limit)
	return 0
}
