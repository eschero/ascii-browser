package browser

import (
	"context"
	_ "embed"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

//go:embed consent.js
var consentJS string

// DismissConsent answers the cookie or privacy banner, if there is one:
// it looks in every frame, consent managers usually live in an iframe of
// their own, and clicks the refusal, or the acceptance when prefer is
// "accept" or when there is no refusal to click. Banners that come in two
// steps get a second round. It returns the labels it clicked.
func (b *Browser) DismissConsent(prefer string, max time.Duration) ([]string, error) {
	var clicked []string
	seq := b.NavSeq()
	defer func() {
		// Answering often reloads the page: read the new one, not the old.
		b.consentReloaded = false
		if len(clicked) > 0 {
			time.Sleep(150 * time.Millisecond)
			b.consentReloaded = b.FollowReload(seq, b.navTimeout)
		}
	}()
	deadline := time.Now().Add(max)
	waitUntil := time.Now().Add(2 * time.Second) // how long a late banner is waited for
	var last []string
	for round := 0; round < 3 && time.Now().Before(deadline); {
		got, cmp, err := b.consentRound(prefer)
		if err != nil {
			return clicked, err
		}
		if len(got) == 0 {
			// A consent manager still loading its message, or a page that
			// talks about cookies without a button yet: wait a little, but
			// only before anything has been answered.
			if !cmp || len(clicked) > 0 || time.Now().After(waitUntil) {
				break
			}
			time.Sleep(250 * time.Millisecond)
			continue
		}
		// The same button again is the banner fading out, not a second step.
		if len(clicked) > 0 && strings.Join(got, "\x00") == strings.Join(last, "\x00") {
			break
		}
		round++
		last = got
		clicked = append(clicked, got...)
		time.Sleep(350 * time.Millisecond) // let the banner close or show its next step
	}
	// Answering the banner often uncovers the next thing in the way, a
	// sign-in prompt or a newsletter pop-up over the page: close it.
	if len(clicked) > 0 && time.Now().Before(deadline) {
		if got, _, err := b.consentRound("close"); err == nil && len(got) > 0 {
			clicked = append(clicked, got...)
			time.Sleep(300 * time.Millisecond)
		}
	}
	return clicked, nil
}

// cmpFrame recognises the frames consent managers draw their banners in.
var cmpFrame = regexp.MustCompile(`(?i)consent|cmp\.|/cmp|privacy-mgmt|sourcepoint|onetrust|cookielaw|didomi|quantcast|trustarc|usercentrics|cookiebot|iubenda|sp_message|message_id=`)

func (b *Browser) consentRound(prefer string) ([]string, bool, error) {
	var got []string
	cmp := false
	expr := "(" + consentJS + ")(" + strconv.Quote(prefer) + ")"
	err := chromedp.Run(b.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		tree, err := page.GetFrameTree().Do(ctx)
		if err != nil {
			return err
		}
		var frames []*cdp.Frame
		var walk func(t *page.FrameTree)
		walk = func(t *page.FrameTree) {
			frames = append(frames, t.Frame)
			for _, c := range t.ChildFrames {
				walk(c)
			}
		}
		walk(tree)
		for i, f := range frames {
			if i > 0 && cmpFrame.MatchString(f.URL) {
				cmp = true
			}
			fctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			id, err := page.CreateIsolatedWorld(f.ID).WithWorldName("ascii-consent").Do(fctx)
			if err != nil {
				cancel()
				continue
			}
			res, exc, err := runtime.Evaluate(expr).WithContextID(id).WithReturnByValue(true).Do(fctx)
			cancel()
			if err != nil || exc != nil || res == nil {
				continue
			}
			var label string
			if res.Value != nil {
				if s, err := strconv.Unquote(string(res.Value)); err == nil {
					label = s
				}
			}
			if label == "\x00wait" {
				cmp = true // a banner may still be on its way
				continue
			}
			if label != "" {
				got = append(got, label)
			}
		}
		return nil
	}))
	if len(got) > 0 {
		cmp = false
	}
	return got, cmp, err
}

// ConsentReloaded reports whether answering the last banner made the page
// load a new document, which then has to be waited for like the first one.
func (b *Browser) ConsentReloaded() bool { return b.consentReloaded }
