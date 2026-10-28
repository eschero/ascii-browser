package browser

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// cssPoint is the center of a cell in CSS pixels.
func (b *Browser) cssPoint(col, row int) (x, y float64) {
	x, y = b.Grid().Center(col, row)
	return x / b.zoom, y / b.zoom
}

// cssCells converts a number of rows to CSS pixels.
func (b *Browser) cssCells(rows int) float64 {
	return float64(rows*b.Grid().CellH) / b.zoom
}

// focusAtJS focuses whatever focusable element sits under a point and
// reports its tag. A synthesized click does not always move the focus the
// way a real one does, and a player that never gets focus ignores the
// keyboard afterwards.
const focusAtJS = `(x, y) => {
  const el = document.elementFromPoint(x, y);
  if (!el) return '';
  const f = el.closest('video,audio,a[href],button,input,select,textarea,[tabindex],[role=button]') || el;
  const take = () => { try { if (f.focus) f.focus({ preventScroll: true }); } catch (e) {} };
  take();
  // A media element is not focusable unless it says so, and a player that
  // never takes the focus ignores the keyboard afterwards.
  if (document.activeElement !== f && (f.tagName === 'VIDEO' || f.tagName === 'AUDIO')) {
    if (!f.hasAttribute('tabindex')) f.tabIndex = 0;
    take();
  }
  return (document.activeElement && document.activeElement.tagName) || '';
}`

// Click sends a left click at the center of a cell. The events carry a
// mouse pointer type and the button bitmask, so the pointerdown,
// pointerup and click a modern player listens for are all generated;
// without the bitmask the pointer events say no button is down and the
// player ignores them. Call WaitSettled afterwards: the click may start a
// navigation.
func (b *Browser) Click(col, row int) error {
	x, y := b.cssPoint(col, row)
	b.drain()
	press := input.DispatchMouseEvent(input.MousePressed, x, y).
		WithButton(input.Left).WithButtons(1).WithClickCount(1).
		WithPointerType(input.Mouse)
	release := input.DispatchMouseEvent(input.MouseReleased, x, y).
		WithButton(input.Left).WithButtons(0).WithClickCount(1).
		WithPointerType(input.Mouse)
	var tag string
	return chromedp.Run(b.ctx,
		input.DispatchMouseEvent(input.MouseMoved, x, y).WithPointerType(input.Mouse),
		press,
		release,
		chromedp.Evaluate(fmt.Sprintf("(%s)(%v,%v)", focusAtJS, x, y), &tag),
	)
}

// Wheel scrolls whatever is under a cell by deltaRows rows, positive
// down, like a mouse wheel.
func (b *Browser) Wheel(col, row, deltaRows int) error {
	x, y := b.cssPoint(col, row)
	return chromedp.Run(b.ctx,
		input.DispatchMouseEvent(input.MouseWheel, x, y).WithDeltaX(0).WithDeltaY(b.cssCells(deltaRows)))
}

// ScrollBy scrolls the window by deltaRows rows, positive down.
func (b *Browser) ScrollBy(deltaRows int) error {
	var y float64
	expr := fmt.Sprintf(`(window.scrollBy({top:%s,left:0,behavior:'instant'}), scrollY)`,
		strconv.FormatFloat(b.cssCells(deltaRows), 'f', -1, 64))
	return chromedp.Run(b.ctx, chromedp.Evaluate(expr, &y))
}

// ScrollToRow scrolls the window so that the given grid row is at the top
// of the viewport.
func (b *Browser) ScrollToRow(row int) error {
	var y float64
	expr := fmt.Sprintf(`(window.scrollTo({top:%s,left:0,behavior:'instant'}), scrollY)`,
		strconv.FormatFloat(b.cssCells(row), 'f', -1, 64))
	return chromedp.Run(b.ctx, chromedp.Evaluate(expr, &y))
}

// ScrollTo scrolls to the top or the bottom of the document.
func (b *Browser) ScrollTo(bottom bool) error {
	var y float64
	expr := `(window.scrollTo({top:0,left:0,behavior:'instant'}), scrollY)`
	if bottom {
		expr = `(window.scrollTo({top:document.documentElement.scrollHeight,left:0,behavior:'instant'}), scrollY)`
	}
	return chromedp.Run(b.ctx, chromedp.Evaluate(expr, &y))
}

// InsertText types text into the focused element, as an IME would.
func (b *Browser) InsertText(s string) error {
	return chromedp.Run(b.ctx, input.InsertText(s))
}

// Key sends one key press (keyDown, keyUp) for a key from the kb
// package, such as kb.Enter or kb.Backspace. Call WaitSettled afterwards
// when the key may submit a form.
func (b *Browser) Key(key string) error {
	b.drain()
	return chromedp.Run(b.ctx, chromedp.KeyEvent(key))
}

// Blur removes focus from the active element, leaving input mode.
func (b *Browser) Blur() error {
	var n float64
	return chromedp.Run(b.ctx, chromedp.Evaluate(`(document.activeElement && document.activeElement.blur(), 0)`, &n))
}

// Back goes one step back in the tab history.
func (b *Browser) Back() error { return b.history(-1) }

// Forward goes one step forward in the tab history.
func (b *Browser) Forward() error { return b.history(1) }

// history moves by delta entries. A history navigation may be served from
// the back-forward cache without any load event, so instead of waiting for
// one it polls the URL until it changes (or 2s pass), then lets the page
// settle briefly.
func (b *Browser) history(delta int) error {
	before, _ := b.State()
	b.drain()
	err := chromedp.Run(b.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		cur, entries, err := page.GetNavigationHistory().Do(ctx)
		if err != nil {
			return err
		}
		target := int(cur) + delta
		if target < 0 || target >= len(entries) {
			return errors.New("no such history entry")
		}
		return page.NavigateToHistoryEntry(entries[target].ID).Do(ctx)
	}))
	if err != nil {
		return err
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st, err := b.State(); err == nil && st.URL != before.URL {
			break
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-b.ctx.Done():
			return b.ctx.Err()
		}
	}
	b.WaitSettled(300 * time.Millisecond)
	return nil
}
