# Design notes

How ascii-browser works inside. The README has the short version.

## How it works

Each terminal cell is 8x16 page pixels, so the Chromium viewport is
`cols*8 x rows*16`. With `--zoom F` the CSS viewport is that size divided
by F and the page is rendered at device scale factor F: graphics grow, the
forced font shrinks by the same factor, and each character still fills one
cell. A stylesheet injected in every document before its
first paint forces `font-size: 16px; line-height: 16px; letter-spacing: 0`
and a custom font, BlankMono, on every element. BlankMono is generated in
memory at startup (`internal/blankfont`, 704 bytes): two empty glyphs with
an advance of half an em, and a cmap format 13 that maps all of Unicode to
the empty one. Every character therefore occupies exactly one cell and the
screenshot shows the page without its text. The renderer averages the
Rec. 709 luma of each cell, dithers on the cell grid and picks a ramp
character. The text walker (`internal/browser/textwalker.js`) measures each
visible character with a one-character `Range`, snaps it to the grid and
tests it for occlusion, and the compositor writes those characters over the
graphics, transliterated to ASCII one rune per cell.

## Structure

With `--structure` each cell gets a 3x3 Sobel gradient per pixel, summed
into a structure tensor. Its anisotropic part (the difference of the two
eigenvalues) measures how much the gradients agree on one direction: a
contour scores high, smooth shading and isotropic texture score near
zero, so the strength threshold separates outlines from modelling. The
edge direction, taken perpendicular to the dominant gradient, is mapped
into glyph space (the cell is twice as tall as wide, so a `/` has a pixel
slope of 2) and binned in 22.5-degree sectors: within 11.25 degrees of
horizontal `-`, or `_` when the energy sits low in the cell; within 11.25
of vertical `|`; the three sectors around 45 `/`, around 135 `\`. Cells
above 60% of the threshold join when they touch a cell above it, and an
edge cell with no edge neighbour is dropped as texture. Edge cells stay
out of the error diffusion, like paper cells; text always wins over them.

## Interactive loop

The interactive mode never polls. Chrome pushes a screencast frame when
the page repaints; a frame that matches the grid's pixel size is rendered
directly, a larger viewport (Chrome caps screencast frames at one million
pixels) makes the frame a thumbnail that only signals a change, and the
page is then captured once. Only the cells that changed are rewritten on
the terminal. While frames keep arriving back to back, as on a page with
an animated GIF, the gap between re-renders doubles from 80 ms up to one
second and resets at the first quiet moment or input.

Scrolling works on a frame buffer: the Chromium viewport, hence the
rendered frame, is `--buffer` screens tall, and the terminal shows one
screen of it. Wheel notches and scroll keys arriving within 30 ms are
summed into one move of that window over the frame in memory, instant and
grid-aligned. Only when less than half a screen of buffer is left in the
scroll direction is the page scrolled and captured again, with the new
buffer placed mostly ahead of the view, so a continuous gesture costs
about one capture per screen. Captures run in the background, never more
than one at a time; an input that changes the page while one is in flight
discards its result and starts a fresh capture on the final state.


## Text walker and the composed tree

The walker descends the composed (flat) tree by hand: an element with an
open shadow root renders its shadow children, and its light children
appear only through `<slot>` elements, which hand out their assigned nodes
(or fallback content). Visibility, opacity, clipping and the enclosing
link or button all follow the flat parent (the slot for slotted nodes,
the host for a shadow root's children). Clipping follows CSS containing
blocks: an `overflow` box clips its in-flow content and the absolutely
positioned descendants it contains, a `position: fixed` element escapes
every ancestor clip, and an absolute one escapes the clips between it and
its nearest positioned ancestor. Occlusion is hit-tested from the node's
own root (`ShadowRoot.elementsFromPoint` returns that tree's elements,
nested components as their hosts, outer elements as themselves) and each
element in the stack counts only if it paints something itself: a
background, a visible border, its own text, or a replaced element. Its
children are in the stack on their own. Buttons (`<button>`, `role=button`,
submit inputs) are grouped like links so Tab reaches them and Enter presses
them.

## The snap renderer

`--renderer snap` reads the page text from `DOMSnapshot.captureSnapshot`
instead of measuring it in the page. The snapshot already flattens shadow
trees and slots, gives one box per rendered line with its offset and
length in the layout text (already `text-transform`ed and white-space
collapsed), and a global paint order. So there is no per-character
measuring and no hit testing: a cell is covered when some box with an
opaque background or replaced content has a higher paint order over it.
Visibility, opacity, and clipping come from the computed styles, with the
same containing-block rules as the walker. Form controls come from the
snapshot's input values, and iframes are separate documents placed at
their owner's box.

Its cost is the whole document, not the visible characters: about 50 ms
per capture on a mid-size page and 140 ms on a long one, whatever the
viewport. The walker is the opposite, cheap on one screen and linear in
the characters it must measure. They cross over around three to six
screens of viewport.

## The interface renderer

`--renderer ui` builds the page out of one DOM snapshot and never takes a
screenshot. Every layout node becomes at most one interface box:

- a **frame** when the node draws a border on any side, or carries a
  background colour that differs from the one it sits on. Two nested
  frames say the same thing twice, so a frame is dropped when its box
  coincides with an ancestor frame within one cell per side, or when that
  ancestor holds nothing else;
- a **widget** for form controls: `[____]` for a text field, `( Label )`
  for a button, `[x]` and `(o)` for checked toggles, `[ value v]` for a
  select. A control is placed on the row its own text lands on, so the
  brackets and the value never end up on different lines, and one shrunk
  below eight pixels is a hidden control behind a styled label, not
  something to draw;
- a **picture**: the element is cut out of one screenshot of the frame
  and rendered as ASCII over the cells it covers, with its frame laid on
  top, so the ramp, the paper threshold, the dithering and `--structure`
  apply inside it. A failed or featureless crop falls back to a framed
  `[ IMG alt text ]`, and a box under `--ui-media-min` cells becomes a
  solid block;
- a **rule** under a heading, as wide as the text actually laid out in
  it, and a **marker** for a list item unless the list hides its markers.

Frames are drawn in paint order, then the text on top. A frame edge that
would fall on the same row or column as text is pushed out by one cell,
so the text ends up inside the frame rather than interleaved with its
line, and a frame that coincides with a widget is left to the widget.

## Photographing the pictures

The pictures are cut out of a single screenshot of the frame rather than
captured one by one. A picture is rendered at the pixels its own cells
average over, which is exactly the resolution that screenshot already
holds, so cutting loses nothing, and the cut is taken on cell boundaries
so a picture lines up with what the tonal renderer draws in the same
place. One capture per element would instead cost a round trip each: on a
long Wikipedia page that was 3.6 s against 1.6 s, and overlapping captures
are not an option, because Chrome composites one screenshot at a time and
concurrent ones come back holding each other's pixels. The cutting and
the scaling do run in parallel.

Before the snapshot, every image is switched from `loading="lazy"` to
eager and awaited for up to three seconds, or the pictures below the fold
would be photographed empty, and every video is muted and played, because
Chromium in headless often decodes no frame and paints the poster
instead. A crop with no detail at all is what a video it refused to
decode looks like, so that becomes the placeholder.

## Occlusion without pixels

The tonal renderer gets occlusion for free: whatever is on top is what the
screenshot shows. The interface renderer has to work it out. Everything to
be drawn, boxes and text alike, carries the paint order the snapshot gave
it, and is drawn in that order, with the text last within one index so a
label is never swallowed by the box it sits in. A node whose
`background-color` is fully opaque blanks every cell of its box before
drawing itself, which is what makes a dropdown, a modal or a cookie
banner hide the page under them. A translucent background blanks nothing,
because the page really does show through it.

## Video

A playing video repaints dozens of times a second, and a screenshot round
trip costs more than the whole frame budget, so the video path does not
go through Go at all. An injected script attaches
`requestVideoFrameCallback` to every video, which fires once per decoded
frame rather than on a timer. In the callback it draws the video into an
`OffscreenCanvas` of exactly as many pixels as the video has cells, so
the graphics engine does the scaling, reads those few pixels back, and
turns them into characters: Rec. 709 luma, an ordered Bayer 8x8 dither,
and the ramp the rest of the frame uses. The dither has to be ordered:
error diffusion depends on every pixel before it, so a still area would
pick different characters from frame to frame and shimmer, while an
ordered matrix is a pure function of the position.

The characters reach Go through `Runtime.addBinding`, not polling, and
only the cells that changed are rewritten. A page capture is never
triggered by a video: a screencast frame whose changed blocks all overlap
a video box leaves the rest of the frame alone. Reading a video's pixels
needs it to be same-origin or served with CORS; a video that taints the
canvas is retried once with `crossOrigin` and then left to its still
crop.

## shot against snap, ten pages

Measured at 200x49, one screen, each page rendered with both:

| page | shot | snap | text pass shot | text pass snap | text cells equal |
|---|---|---|---|---|---|
| Wikipedia (World War II) | 1.06 s | 1.06 s | 56 ms | 143 ms | 100% |
| YouTube | 1.87 s | 1.80 s | 11 ms | 34 ms | 100% |
| Repubblica | 1.44 s | 1.41 s | 32 ms | 37 ms | 94.8% |
| IKEA | 2.50 s | 2.45 s | 16 ms | 68 ms | 97.6% |
| GitHub | 1.94 s | 0.96 s | 9 ms | 13 ms | 99.5% |
| danluu.com | 0.59 s | 0.64 s | 5 ms | 6 ms | 100% |
| Bootstrap, fixed navbar | 0.78 s | 0.56 s | 3 ms | 1 ms | 100% |
| GitHub Actions, dark | 1.36 s | 1.06 s | 4 ms | 10 ms | 96.7% |
| W3Schools, iframes | 1.55 s | 1.35 s | 32 ms | 29 ms | 87.1% |
| Yahoo! Japan | 3.32 s | 2.86 s | 7 ms | 2 ms | 100% |

The totals are dominated by loading the page, so they say little: snap is
equal or faster on nine of the ten, and the difference is mostly noise.
The text pass is the real signal, and there the walker is cheaper on
seven of the ten at one screen, by a wide margin on a long document.
That is the crossover the `auto` renderer already weighs: the walker
measures what is on screen, the snapshot serializes the whole document.
So `auto` stays the default rather than a fixed choice.

Where the text differs it is mostly the page moving between two separate
runs: a live score ticker, a banner that animates. Two differences are
ours: on W3Schools snap misses a few lines of a dialog the walker sees,
and on Repubblica the two clip an overflowing ticker one character
apart.

## Waiting for a page to build itself

A load event says the document arrived, not that the application drew
anything: a client-rendered page fires it while still showing its
placeholders. So after a navigation the browser waits for the DOM to go
quiet, watching the whole tree with a MutationObserver and finishing
after 300 ms without a mutation, with a five second ceiling. A quiet
stretch with almost no readable text does not count as finished, because
that is an application that has stopped touching the DOM for a moment
rather than one that has drawn. On a page that needs none of this, an
article, the wait costs about 400 ms of which 300 is the quiet stretch
itself; `--dom-quiet 0` skips it. A page that still ends up under two
hundred characters is reported on stderr rather than written out empty in
silence.

An element at `opacity: 0` that is animating that opacity is not hidden,
it is about to appear, so both text paths treat it as visible;
`--strict-opacity` restores the literal reading. The animation has to be
real: every element reports `transition-property: all` by default, so the
property alone would make anything invisible look like it was arriving,
and the duration has to be above zero as well. A page that leaves a block
at `opacity: 0` with no animation at all is painting nothing there, and
that is what gets rendered.

## The profile

Chrome keeps its profile in one directory per user, so a consent accepted
once stays accepted and a login survives the session. Only one Chrome may
use a profile at a time, so a second session finding it taken falls back
to a throwaway profile and says so rather than refusing to start.

## A video is an element like any other

Everything the page paints after a video, a caption over it, the menu
above it, a panel that overlaps it, has to stay on top while the picture
moves underneath. So each capture records, for every cell of the frame,
the paint order of whatever drew it, and a video frame skips the cells
whose owner comes later than the video. The map is built once per
capture, never per frame, so the fast path stays free.

The page and the snapshot disagree about where a video is: the page
reports the element's own box, the snapshot the part of it that is on
screen, and the two round to cells independently. So the incoming frame
is matched to the video box it overlaps most rather than by position, and
when none overlaps the lowest video paint order is used, which protects
more rather than less.

## Waking the page

With a viewport several screens tall every IntersectionObserver fires at
once in the first instant, and whatever was not mounted yet misses it for
good; a page that reveals its blocks on scroll, or loads its images
lazily, stays half empty until someone scrolls. So after loading, and
before the first capture, the browser scrolls through the whole page one
screen at a time with a short pause on each, comes back, and then waits
for every finite animation to end, because coming back is what starts the
fade-ins and a page read mid-fade is caught half transparent. Endless
animations, a marquee, a spinner, are not waited for. Both the steps and
the time are capped, since a page can grow while it is being scrolled.
This is `--wake`, on by default with `--once` and `--full`, where it costs
one page between one and two seconds, and off in the interactive browser,
where it would cost every navigation and the view follows the reader's
own scrolling anyway.

Inside the buffer, scrolling used to move the view without the page
knowing, so nothing tied to scrolling could ever fire, and a parallax
layer or a bar that shrinks stayed where it was at the last capture. Now
once the scrolling pauses for 150 ms, and the view has travelled at least
half a screen since the page was last placed under it, the page's own
scroll is moved to follow the view and a capture is taken: the observers
fire, the lazy images load, and everything tied to the scroll position is
redrawn where it is. The half screen matters: following at every pause
made a slow trackpad scroll a capture every few rows, each one recentring
the buffer and redrawing the whole screen, which reads as the page
looping. The page is placed the way a recenter would place it for the
direction of travel, mostly ahead of the view, so the two never undo each
other. Smoothness is protected by the pause and the half screen, not by
avoiding the capture: while the reader scrolls nothing is asked of the
page, and once they stop one capture, the cost of a recenter, brings the
frame up to date.

A page that repaints forever, a spinner, a ticker, a marquee, used to be
read once a second for as long as it was open. Now a capture that changes
fewer than eight cells on screen doubles the wait before the next, up to
eight seconds, and one that changes anything resets it, so a restless
page costs a read every eight seconds and a real change still shows
within that.

Two rules keep this from fighting the buffer. The page is never moved
while a capture is in flight: a capture reads the page's scroll to place
its buffer, and a page moved under it landed the buffer wherever the last
follow had left it, with the view on its edge and the next step forcing
yet another capture, a loop of one capture per scroll step. And the page
is moved to where a recenter would put it, with the view in the middle
of the viewport, so a capture that follows finds the view well inside its
buffer rather than at its top.

Some reveals never fire in a headless browser at all, whatever is done
to them. `--dom-text` reads such a page from the DOM instead: opacity no
longer decides what is visible, while display, visibility and the clip of
the ancestors still do. It is an option, not the default, because what
the page hides on purpose, a dismissed cookie banner, a closed menu, comes
back with it.

## A hundred pages

A bench renders a hundred complex pages, news sites, shops, social
networks, developer documentation, in parallel and flags what went wrong:
a failed run, a malformed frame, a screen with almost no text, a banner
left on screen. What it found, and what fixed it:

- **Consent banners covered 46 of them.** They are now answered before
  anything else, in every frame including the consent manager's own
  cross-origin iframe, which is why site isolation is off: every frame
  lives in one renderer and appears in the frame tree. A button counts
  only inside something about cookies or consent, a refusal that leads
  to a subscription is never followed, a banner still loading is waited
  for, a reload the answer triggers is followed, and the sign-in prompt
  that often comes next is closed. Seven are left.
- **HeadlessChrome in the user agent** got a verification page instead
  of the site on openai.com, IMDb, the Washington Post, AccuWeather and
  others. Chrome now presents itself as Chrome, in the client hints too.
- **The wake-up scroll went through the whole page to render one
  screen**: five seconds on a forty-screen newspaper. It now stops where
  the frame does, and goes the whole way only with `--full`.
- **The load event** on a page full of ads and trackers comes ten
  seconds late; the document being parsed is enough, since the DOM quiet
  wait follows. A navigation that never answers is bounded, a network
  error that came fast is tried once more, and a page that replaces
  itself by script during a wait is followed to its new document.
- **A capture taken while the page was replacing itself** came back
  without its text, and since text is invisible in the screenshot no
  repaint ever said so. It is now read again, in both modes.

The median time per page under the bench's load of eight Chromes went
from 19.8 to 10.1 seconds; alone, a page takes between two and eight.

## Sound, and video levels

Chrome is started muted by chromedp's own defaults, so sound needs the
switch removed as well as the autoplay policy relaxed. With `--audio on`,
the default in the interactive browser, the page decides: a video it
starts plays with its own sound, one it leaves paused stays paused, and no
video is forced muted any more. With sound off every video is still
started muted, so there is a frame to render. The page's audio context is
the check that the sound reaches a real output: running, with the
device's latency, rather than suspended with none.

A video is rarely black to white, and mapped as it is it comes out washed
out, a few characters doing all the work. Each frame's range, from the
2nd to the 98th percentile of its luma, is stretched onto the whole ramp,
eased from frame to frame so a cut settles within a few frames and a
still scene never breathes. The paper threshold, meant for a page's white
background, is not applied to video: there it blanked the sky.

## Fullscreen

A player that goes fullscreen fills Chrome's viewport, and in the
interactive browser the viewport is the frame buffer, several screens
tall: the video was centred far below the view. While the page has an
element fullscreen the viewport is exactly the terminal, and it goes
back to the buffer when fullscreen ends. The in-page video renderer keeps
the picture's proportions inside the element's box, as the page does,
instead of stretching the frame over it; the bands around the picture
are left blank rather than drawn as solid black.

## Dark pages

The ramp maps dark pixels onto dense characters, which is right for a page
printed on white and wrong for one with a dark theme: YouTube in dark mode
came out as a wall of `%@#` with its text drowned in it. The page reports
the luminance of its background, the first opaque one among body and html;
below 0.2 the mapping is inverted for that page, so the background comes
out blank and the bright parts dense, text and pictures included, and the
in-page video renderer follows. `--dark-pages keep` maps such a page like
any other, and `--invert` still flips everything by hand. On the YouTube
page the cells drawn with the three densest characters went from 8135 to
31.

## Where the time went after sound

With sound on, a video now really plays, and three things made the
interactive browser feel slow; none of them was the new rendering.

- **The tonal renderers photographed the whole page for every video
  frame.** The fast video path, drawn inside the page with the rest left
  alone, existed only for the interface renderer. The page state now
  reports its videos' boxes, so the tonal renderers take it too: on
  YouTube, fourteen captures of the whole page in fifteen seconds became
  one. Text laid over a video keeps its cells, the video is placed below
  every run.
- **The in-page renderer fell back to animation frames for good** when
  the video was not playing yet at install time, and drew at every screen
  refresh, four times the frames the video decodes. The fallback now
  waits for the video to be playing, and does not draw a frame twice.
- **Chrome decoded video in software**, because it ran with the GPU
  disabled. With the GPU, a playing YouTube video costs Chrome 45% of a
  core instead of 65%; a page renders the same but for 1.25% of its cells,
  antialiasing, and `ASCII_NO_GPU=1` turns it off for comparison.

The first frame is also no longer held by the consent banner: in the
interactive browser it is answered in the background and the page is
read again once it is gone. Follow-up DOM quiet waits are capped at
600 ms unless the answer reloaded the page, since a player updating its
clock never lets the DOM go quiet.
