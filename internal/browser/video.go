package browser

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

//go:embed video.js
var videoJS string

// videoBinding is the name the page calls to hand over a rendered frame.
const videoBinding = "__asciiVideoFrame"

// VideoFrame is one video frame already turned into characters by the
// page: the rows of text and where they go, in cells.
type VideoFrame struct {
	X     int      `json:"x"`
	Y     int      `json:"y"`
	Cols  int      `json:"cols"`
	Rows  int      `json:"rows"`
	N     int      `json:"n"`
	Lines []string `json:"lines"`
	// Tainted marks a video whose pixels the page may not read, because
	// it comes from another origin without CORS. There are no lines: Go
	// photographs the box itself instead.
	Tainted bool `json:"tainted"`
}

// VideoOptions is what the page needs to render a video the same way the
// rest of the frame is rendered.
type VideoOptions struct {
	Ramp   string
	Invert bool
	Paper  float64
	FPS    float64 // 0: as fast as the video decodes
	Once   bool    // stop after the first frame (the poster mode)
}

// VideoFrames is where rendered video frames arrive. The channel holds
// one frame per video box: a frame that is still waiting when a newer one
// comes in is dropped, because only the latest is worth drawing.
func (b *Browser) VideoFrames() <-chan VideoFrame {
	b.videoMu.Lock()
	defer b.videoMu.Unlock()
	if b.videoCh == nil {
		b.videoCh = make(chan VideoFrame, 8)
	}
	return b.videoCh
}

// StartVideo installs the in-page video renderer and points it at the
// current grid. It is safe to call again: the page reconfigures itself
// and picks up videos that appeared since.
func (b *Browser) StartVideo(opts VideoOptions) error {
	if b.VideoMode() == "off" {
		return nil
	}
	b.videoMu.Lock()
	first := !b.videoListening
	b.videoListening = true
	if b.videoCh == nil {
		b.videoCh = make(chan VideoFrame, 8)
	}
	b.videoMu.Unlock()

	if first {
		chromedp.ListenTarget(b.ctx, b.onVideoBinding)
	}
	g := b.Grid()
	cfg := fmt.Sprintf(`{cw:%s,ch:%s,cols:%d,rows:%d,ramp:%s,invert:%t,paper:%s,fps:%s,once:%t,audio:%t}`,
		strconv.FormatFloat(float64(g.CellW)/b.zoom, 'f', -1, 64),
		strconv.FormatFloat(float64(g.CellH)/b.zoom, 'f', -1, 64),
		g.Cols, g.Rows, jsString(opts.Ramp), opts.Invert,
		strconv.FormatFloat(opts.Paper, 'f', -1, 64),
		strconv.FormatFloat(opts.FPS, 'f', -1, 64), opts.Once, b.audio)
	var out string
	err := chromedp.Run(b.ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			return runtime.AddBinding(videoBinding).Do(ctx)
		}),
		chromedp.Evaluate("("+videoJS+")("+cfg+")", &out),
	)
	b.videoMu.Lock()
	b.videoStatus = out
	b.videoMu.Unlock()
	return err
}

// VideoStatus reports what the last injection did, for the log.
func (b *Browser) VideoStatus() string {
	b.videoMu.Lock()
	defer b.videoMu.Unlock()
	return b.videoStatus
}

func (b *Browser) onVideoBinding(ev interface{}) {
	e, ok := ev.(*runtime.EventBindingCalled)
	if !ok || e.Name != videoBinding {
		return
	}
	var f VideoFrame
	if err := json.Unmarshal([]byte(e.Payload), &f); err != nil {
		return
	}
	b.videoMu.Lock()
	ch := b.videoCh
	b.videoMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- f:
	default:
		// The drawing side is behind: throw the oldest away and keep the
		// newest, since a video frame is only worth drawing while fresh.
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- f:
		default:
		}
	}
}

// jsString quotes a Go string for embedding in a script.
func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
