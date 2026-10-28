package browser

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"sync"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/eschero/ascii-browser/internal/frame"
	"github.com/eschero/ascii-browser/internal/pixel"
)

// mediaWorkers is how many crops are cut and scaled at once.
const mediaWorkers = 4

// prepareMediaJS makes the page ready to be photographed piece by piece:
// lazy images are told to load now and awaited, and videos are nudged
// into producing a frame. Chrome paints a video's poster while no frame
// has been decoded, so a video that will not play still photographs as
// its poster. Returns the number of images and videos it waited for.
const prepareMediaJS = `(async () => {
  const deep = (root, sel, out) => {
    for (const el of root.querySelectorAll(sel)) out.push(el);
    for (const el of root.querySelectorAll('*')) if (el.shadowRoot) deep(el.shadowRoot, sel, out);
    return out;
  };
  const imgs = deep(document, 'img', []);
  for (const im of imgs) {
    if (im.loading === 'lazy') im.loading = 'eager';
    if (im.decoding === 'async') im.decoding = 'sync';
  }
  const pending = imgs.filter(im => !im.complete && im.getAttribute('src'));
  const videos = deep(document, 'video', []);
  for (const v of videos) {
    // Start a video once and never again: pressing play on every capture
    // would undo the reader pausing it.
    if (v.__asciiTried) continue;
    v.__asciiTried = true;
    // With sound on, the page decides: a video it set to play plays with
    // its own sound, one it left paused stays paused. With sound off,
    // every video is started muted, so there is a frame to render.
    if (window.__asciiAudio) {
      if (v.autoplay || v.muted) { try { const p = v.play(); if (p && p.catch) p.catch(() => {}); } catch (e) {} }
      continue;
    }
    try { v.muted = true; const p = v.play(); if (p && p.catch) p.catch(() => {}); } catch (e) {}
  }
  const settled = Promise.all(pending.map(im => new Promise(done => {
    im.addEventListener('load', done, { once: true });
    im.addEventListener('error', done, { once: true });
  })));
  await Promise.race([settled, new Promise(r => setTimeout(r, %d))]);
  // Give a played video one frame's worth of time to show something.
  if (videos.length) await new Promise(r => setTimeout(r, 120));
  return { images: imgs.length, waited: pending.length, videos: videos.length,
           playing: videos.filter(v => v.currentTime > 0).length };
})()`

// MediaPrep reports what the media preparation pass found.
type MediaPrep struct {
	Images  int `json:"images"`
	Waited  int `json:"waited"`
	Videos  int `json:"videos"`
	Playing int `json:"playing"`
}

// PrepareMedia loads lazy images and starts videos, waiting at most
// timeout for the images to arrive.
func (b *Browser) PrepareMedia(timeout time.Duration) (MediaPrep, error) {
	var out MediaPrep
	script := fmt.Sprintf(prepareMediaJS, int(timeout/time.Millisecond))
	if b.audio {
		script = "window.__asciiAudio = true;\n" + script
	}
	err := chromedp.Run(b.ctx, chromedp.Evaluate(script, &out, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
		return p.WithAwaitPromise(true)
	}))
	return out, err
}

// mediaShot is one media element waiting to be photographed. The rect is
// the element's cell box in device pixels, so a crop covers exactly the
// pixels those cells average over and lines up with what the tonal
// renderer draws in the same place.
type mediaShot struct {
	box  int // index into the UI boxes
	x, y int // cell box origin in device pixels
	w, h int // cell box size in device pixels
}

// cropMedia cuts each media element out of one frame screenshot and keeps
// only the pixels its cells will average over.
//
// One capture of the whole frame beats one capture per element: a crop is
// rendered at the pixels its cells cover, which is exactly the resolution
// the frame screenshot already holds, so cutting loses nothing, and a
// page with a hundred pictures would otherwise pay a hundred round trips
// to Chrome. The cutting and the scaling run in parallel; the capture
// itself cannot, because Chrome composites one screenshot at a time and
// overlapping captures come back with each other's pixels.
func cropMedia(boxes []frame.UIBox, shots []mediaShot, page image.Image) {
	type subImager interface {
		SubImage(image.Rectangle) image.Image
	}
	sub, ok := page.(subImager)
	if !ok || len(shots) == 0 {
		return
	}
	bounds := page.Bounds()
	sem := make(chan struct{}, mediaWorkers)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, sh := range shots {
		r := image.Rect(
			bounds.Min.X+sh.x, bounds.Min.Y+sh.y,
			bounds.Min.X+sh.x+sh.w, bounds.Min.Y+sh.y+sh.h,
		).Intersect(bounds)
		if r.Dx() < 2 || r.Dy() < 2 || sh.w < 1 || sh.h < 1 {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(sh mediaShot, r image.Rectangle) {
			defer wg.Done()
			defer func() { <-sem }()
			crop := sub.SubImage(r)
			if uniform(crop) {
				return // a featureless crop falls back to the placeholder
			}
			small := pixel.Downsample(crop, sh.w, sh.h)
			mu.Lock()
			boxes[sh.box].Img = small
			mu.Unlock()
		}(sh, r)
	}
	wg.Wait()
}

// freezePosters keeps a video's first crop and hands it back on later
// captures, so a playing video does not make every capture redraw it.
// In "live" mode nothing is frozen.
func (b *Browser) freezePosters(boxes []frame.UIBox) {
	if b.VideoMode() != "poster" {
		return
	}
	b.posterMu.Lock()
	defer b.posterMu.Unlock()
	for i, bx := range boxes {
		if !bx.Video {
			continue
		}
		key := fmt.Sprintf("%d,%d,%d,%d", bx.X, bx.Y, bx.W, bx.H)
		if old, ok := b.posters[key]; ok {
			boxes[i].Img = old
			continue
		}
		if bx.Img != nil {
			b.posters[key] = bx.Img
		}
	}
}

// CropCells photographs one rectangle of cells and returns it scaled to
// the pixels those cells average over. It is how a live video is
// refreshed without recapturing the page.
func (b *Browser) CropCells(x, y, w, h int) (image.Image, error) {
	g := b.Grid()
	if w < 1 || h < 1 {
		return nil, nil
	}
	clip := &page.Viewport{
		X: float64(x*g.CellW) / b.zoom, Y: float64(y*g.CellH) / b.zoom,
		Width: float64(w*g.CellW) / b.zoom, Height: float64(h*g.CellH) / b.zoom,
		Scale: b.zoom,
	}
	var data []byte
	err := chromedp.Run(b.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var e error
		data, e = page.CaptureScreenshot().
			WithFormat(page.CaptureScreenshotFormatJpeg).WithQuality(80).
			WithCaptureBeyondViewport(false).WithClip(clip).Do(ctx)
		return e
	}))
	if err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil || uniform(img) {
		return nil, err
	}
	return pixel.Downsample(img, w*g.CellW, h*g.CellH), nil
}

// uniform reports whether an image carries no detail worth rendering,
// which is what a video Chromium refused to decode looks like.
func uniform(img image.Image) bool {
	b := img.Bounds()
	if b.Dx() < 1 || b.Dy() < 1 {
		return true
	}
	var lo, hi uint32 = 65535, 0
	stepX := max1(b.Dx() / 32)
	stepY := max1(b.Dy() / 32)
	for y := b.Min.Y; y < b.Max.Y; y += stepY {
		for x := b.Min.X; x < b.Max.X; x += stepX {
			r, g, bl, _ := img.At(x, y).RGBA()
			l := (2126*r + 7152*g + 722*bl) / 10000 >> 8
			if l < lo {
				lo = l
			}
			if l > hi {
				hi = l
			}
		}
	}
	return hi-lo < 6
}

func max1(v int) int {
	if v < 1 {
		return 1
	}
	return v
}
