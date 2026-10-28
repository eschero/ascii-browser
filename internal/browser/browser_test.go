package browser

import (
	"context"
	"image"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/chromedp/chromedp/kb"

	"github.com/eschero/ascii-browser/internal/frame"
	"github.com/eschero/ascii-browser/internal/grid"
	"github.com/eschero/ascii-browser/internal/render"
)

func chromeAvailable() bool {
	for _, p := range []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/Applications/Chromium.app/Contents/MacOS/Chromium"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	for _, n := range []string{"google-chrome", "chromium", "chromium-browser", "chrome"} {
		if _, err := exec.LookPath(n); err == nil {
			return true
		}
	}
	return false
}

const testPage = `<!doctype html><html><head><meta charset="utf-8"><style>
body,p{margin:0} body{background:#fff}
</style></head><body>
<p>Hello <a href="/x">link</a> caff&egrave;</p>
<div style="width:64px;height:32px;background:#000"></div>
<div style="position:relative;height:16px">
  <span id="under">Covered</span>
  <div style="position:absolute;left:0;top:0;width:200px;height:16px;background:#888"></div>
</div>
<div style="position:relative;height:16px">
  <span>Catcher</span>
  <div style="position:absolute;left:0;top:0;width:200px;height:16px"></div>
</div>
<input value="typed" style="width:80px;padding:0;border:0;margin:0;height:16px">
<x-card><span>Slotted</span></x-card>
<div style="position:relative;height:16px"><button style="margin:0;padding:0;border:0;background:#eee;height:16px;font:inherit">Press</button>
  <div style="position:absolute;left:0;top:0;width:200px;height:16px"><div style="opacity:0;background:#000;width:200px;height:16px"></div></div></div>
<script>
customElements.define('x-card', class extends HTMLElement {
  constructor() {
    super();
    const r = this.attachShadow({mode: 'open'});
    r.innerHTML = '<style>:host{display:block;height:16px}</style><a href="/shadow">Shadow</a> <slot></slot>';
  }
});
</script>
<div style="height:3000px"></div>
</body></html>`

const pageX = `<!doctype html><html><head><meta charset="utf-8"></head><body style="margin:0"><p>Page X</p></body></html>`

func TestIntegration(t *testing.T) {
	// Rows are computed from the test page layout: p 16px, black div 32px,
	// two 16px blocks, input 16px, all in CSS pixels; a row is 16/zoom of them.
	integration(t, 1, 40, 12, map[string]int{"catcher": 4, "input": 5, "shadow": 6, "button": 7}, 1)
}

func TestIntegrationZoom2(t *testing.T) {
	// At zoom 2 the same CSS layout covers twice the rows: text lines are
	// 8 CSS px tall (one row), the black div 32 CSS px (rows 1-4), the
	// "Covered" block rows 5-6, "Catcher" rows 7-8, the input rows 9-10.
	integration(t, 2, 40, 16, map[string]int{"catcher": 7, "input": 10, "shadow": 11, "button": 14}, 4)
}

func integration(t *testing.T, zoom float64, cols, rows int, wantRow map[string]int, blackRows int) {
	if !chromeAvailable() {
		t.Skip("no Chrome/Chromium found")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/x" {
			w.Write([]byte(pageX))
			return
		}
		w.Write([]byte(testPage))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	g := grid.New(cols, rows)
	b, err := New(ctx, Options{Grid: g, Headless: true, Zoom: zoom})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := b.Navigate(srv.URL); err != nil {
		t.Fatal(err)
	}

	img, err := b.Screenshot()
	if err != nil {
		t.Fatal(err)
	}
	if w, h := img.Bounds().Dx(), img.Bounds().Dy(); w != cols*8 || h != rows*16 {
		t.Fatalf("screenshot %dx%d, want %dx%d", w, h, cols*8, rows*16)
	}
	luma := render.CellLuma(img, g, nil)
	for row := 1; row <= blackRows; row++ {
		if l := luma[row*g.Cols+0]; l > 0.05 {
			t.Errorf("black div cell (0,%d) luma = %.3f, want ~0", row, l)
		}
	}
	if l := luma[0*g.Cols+0]; l < 0.95 {
		t.Errorf("text cell luma = %.3f, want ~1 (text must be invisible in the screenshot)", l)
	}

	runs, err := b.TextRuns()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	var linkRun frame.TextRun
	for _, r := range runs {
		t.Logf("run %+v", r)
		switch r.Text {
		case "Hello ":
			found["hello"] = r.X == 0 && r.Y == 0 && r.W == 6 && !r.Link && r.LinkID == 0
		case "link":
			found["link"] = r.X == 6 && r.Y == 0 && r.Link && r.LinkID > 0 && r.Href == srv.URL+"/x"
			linkRun = r
		case " caffè": // the space after the link is rendered, so it belongs to the run
			found["accent"] = r.X == 10 && r.Y == 0 && r.W == 6
		case "Covered":
			found["covered"] = true
		case "Catcher":
			found["catcher"] = r.X == 0 && r.Y == wantRow["catcher"]
		case "typed":
			found["input"] = r.X == 0 && r.Y == wantRow["input"] && r.Field && r.W == int(10*zoom)
		case "Shadow":
			// Text inside an open shadow root, wrapped in a shadow-tree anchor.
			found["shadow"] = r.Y == wantRow["shadow"] && r.X == 0 && r.Link && r.Href == srv.URL+"/shadow"
		case "Slotted":
			// Light-DOM text rendered through a slot, after "Shadow ".
			found["slotted"] = r.Y == wantRow["shadow"] && r.X == 7 && !r.Link
		case "Press":
			// A button label under a transparent overlay whose only child is
			// invisible: the overlay paints nothing, so the text is visible.
			found["button"] = r.Y == wantRow["button"] && r.X == 0 && r.Button && r.LinkID > 0 && !r.Link
		}
	}
	for _, k := range []string{"hello", "link", "accent", "catcher", "input", "shadow", "slotted", "button"} {
		if !found[k] {
			t.Errorf("run %q missing or misplaced", k)
		}
	}
	if found["covered"] {
		t.Errorf("text under an opaque overlay must not be reported")
	}
	if zoom != 1 {
		return
	}

	// Tiled capture: force tiles two rows tall and compare with the single shot.
	saved := maxShotPixels
	maxShotPixels = cols * 8 * 32
	tiled, err := b.Screenshot()
	maxShotPixels = saved
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tiled.(*image.Gray); !ok || tiled.Bounds() != img.Bounds() {
		t.Fatalf("tiled screenshot is %T %v, want *image.Gray %v", tiled, tiled.Bounds(), img.Bounds())
	}
	tl := render.CellLuma(tiled, g, nil)
	for i := range luma {
		if d := tl[i] - luma[i]; d > 0.02 || d < -0.02 {
			t.Fatalf("cell %d: tiled luma %.3f vs single %.3f", i, tl[i], luma[i])
		}
	}

	// Interaction: scroll, type into the field, follow the link, go back.
	if err := b.ScrollBy(3); err != nil {
		t.Fatal(err)
	}
	st, err := b.State()
	if err != nil {
		t.Fatal(err)
	}
	if st.ScrollY != 48 {
		t.Errorf("after ScrollBy(3) scrollY = %v, want 48", st.ScrollY)
	}
	if err := b.Wheel(0, 0, -3); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if st, _ = b.State(); st.ScrollY != 0 {
		t.Errorf("after Wheel(-3) scrollY = %v, want 0", st.ScrollY)
	}

	if err := b.Click(0, wantRow["input"]); err != nil {
		t.Fatal(err)
	}
	b.WaitSettled(200 * time.Millisecond)
	if st, _ = b.State(); st.Focus != "input" {
		t.Fatalf("after clicking the field focus = %q, want input", st.Focus)
	}
	if err := b.Key(kb.End); err != nil {
		t.Fatal(err)
	}
	if err := b.InsertText("ab"); err != nil {
		t.Fatal(err)
	}
	if err := b.Key(kb.Backspace); err != nil {
		t.Fatal(err)
	}
	runs, _ = b.TextRuns()
	typed := false
	for _, r := range runs {
		if r.Field && r.Text == "typeda" {
			typed = true
		}
	}
	if !typed {
		t.Errorf("field text after typing: %+v", runs)
	}
	if err := b.Blur(); err != nil {
		t.Fatal(err)
	}
	if st, _ = b.State(); st.Focus != "" {
		t.Errorf("after Blur focus = %q, want none", st.Focus)
	}

	if err := b.Click(linkRun.X, linkRun.Y); err != nil {
		t.Fatal(err)
	}
	b.WaitSettled(2 * time.Second)
	if st, _ = b.State(); st.URL != srv.URL+"/x" {
		t.Fatalf("after clicking the link url = %q, want %s/x", st.URL, srv.URL)
	}
	if err := b.Back(); err != nil {
		t.Fatal(err)
	}
	if st, _ = b.State(); st.URL != srv.URL+"/" {
		t.Errorf("after Back url = %q, want %s/", st.URL, srv.URL)
	}
	if err := b.Forward(); err != nil {
		t.Fatal(err)
	}
	if st, _ = b.State(); st.URL != srv.URL+"/x" {
		t.Errorf("after Forward url = %q, want %s/x", st.URL, srv.URL)
	}
}
