package browser

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"sync"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// ScreencastFrame is one frame pushed by Chrome: the page repainted.
// Img is the decoded frame; it matches the grid's pixel size unless Chrome
// scaled it down, in which case the receiver should fall back to Capture.
// Ack must be called once the frame has been consumed: Chrome keeps at
// most a couple of frames in flight and waits for acknowledgements, which
// is what throttles a page that repaints continuously to the receiver's
// pace.
type ScreencastFrame struct {
	Img       image.Image
	ScrollX   float64   // CSS pixels
	ScrollY   float64   // CSS pixels
	Timestamp time.Time // frame swap time, Chrome's wall clock
	sessionID int64
	dropped   []int64 // earlier frames replaced by this one, acknowledged with it
	b         *Browser
}

// Ack tells Chrome the frame was consumed, so the next one may be sent.
// Frames this one replaced are acknowledged at the same time: holding
// their acknowledgement until now is what keeps Chrome from encoding
// frames faster than they are consumed.
func (f *ScreencastFrame) Ack() {
	if f == nil || f.b == nil {
		return
	}
	for _, id := range f.dropped {
		_ = chromedp.Run(f.b.ctx, page.ScreencastFrameAck(id))
	}
	_ = chromedp.Run(f.b.ctx, page.ScreencastFrameAck(f.sessionID))
}

// Supersede makes f stand in for an older frame that will not be
// processed: the older frame is acknowledged when f is.
func (f *ScreencastFrame) Supersede(old *ScreencastFrame) {
	if old == nil || old == f {
		return
	}
	f.dropped = append(f.dropped, old.dropped...)
	f.dropped = append(f.dropped, old.sessionID)
}

// StartScreencast asks Chrome to push a frame whenever the page repaints,
// as JPEG at the grid's pixel size when that is within Chrome's cap, else
// as a thumbnail that serves as a change notification. Frames arrive on the returned
// channel, which holds one frame: a frame that is still waiting when the
// next arrives is dropped and acknowledged together with the newer one,
// so the receiver always sees the latest and Chrome never runs ahead. Call StopScreencast, or Resize, which restarts it at the
// new size.
func (b *Browser) StartScreencast() (<-chan *ScreencastFrame, error) {
	b.mu.Lock()
	if b.frames == nil {
		b.frames = make(chan *ScreencastFrame, 1)
		b.frameMu = &sync.Mutex{}
		chromedp.ListenTarget(b.ctx, b.onScreencastFrame)
	}
	b.casting = true
	ch := b.frames
	b.mu.Unlock()
	return ch, b.startCast()
}

// screencastMaxPixels is Chrome's cap on a screencast frame: larger
// viewports arrive scaled down. Beyond it the frames cannot be rendered
// directly, so a small thumbnail is requested instead and each frame only
// signals that the page repainted; the receiver then captures.
const screencastMaxPixels = 1_000_000

func (b *Browser) startCast() error {
	w, h := b.Grid().PixelSize()
	if w*h > screencastMaxPixels {
		w, h = 160, 160
	}
	return chromedp.Run(b.ctx, page.StartScreencast().
		WithFormat(page.ScreencastFormatJpeg).
		WithQuality(85).
		WithMaxWidth(int64(w)).
		WithMaxHeight(int64(h)).
		WithEveryNthFrame(1))
}

// StopScreencast stops the frame stream.
func (b *Browser) StopScreencast() error {
	b.mu.Lock()
	b.casting = false
	b.mu.Unlock()
	return chromedp.Run(b.ctx, page.StopScreencast())
}

func (b *Browser) onScreencastFrame(ev interface{}) {
	e, ok := ev.(*page.EventScreencastFrame)
	if !ok {
		return
	}
	f := &ScreencastFrame{sessionID: e.SessionID, b: b}
	if e.Metadata != nil {
		f.ScrollX, f.ScrollY = e.Metadata.ScrollOffsetX, e.Metadata.ScrollOffsetY
		if e.Metadata.Timestamp != nil {
			f.Timestamp = e.Metadata.Timestamp.Time()
		}
	}
	data, err := base64.StdEncoding.DecodeString(e.Data)
	if err == nil {
		f.Img, _, err = image.Decode(bytes.NewReader(data))
	}
	if err != nil {
		f.Ack() // undecodable frame: let the stream continue
		return
	}
	b.frameMu.Lock()
	defer b.frameMu.Unlock()
	select {
	case b.frames <- f:
	default:
		select {
		case old := <-b.frames:
			f.dropped = append(old.dropped, old.sessionID)
		default:
		}
		b.frames <- f
	}
}

// restartCast is called after a viewport change while casting.
func (b *Browser) restartCast(ctx context.Context) error {
	if err := chromedp.Run(ctx, page.StopScreencast()); err != nil {
		return err
	}
	return b.startCast()
}
