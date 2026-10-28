package frame

import "image"

// UIKind is what an interface box represents.
type UIKind int

const (
	UIClear    UIKind = iota // an opaque background: it blanks everything under it
	UIFrame                  // a container with a border or a background of its own
	UIField                  // text input or textarea
	UIButton                 // button, input type=submit/button/reset, role=button
	UICheckbox               // input type=checkbox
	UIRadio                  // input type=radio
	UISelect                 // select
	UIMedia                  // img, video, canvas, svg, iframe and friends
	UIRule                   // the underline of a heading
	UIMarker                 // the bullet or number of a list item
)

// String names the kind, for logs and the --ui-boxes listing.
func (k UIKind) String() string {
	switch k {
	case UIClear:
		return "clear"
	case UIFrame:
		return "frame"
	case UIField:
		return "field"
	case UIButton:
		return "button"
	case UICheckbox:
		return "checkbox"
	case UIRadio:
		return "radio"
	case UISelect:
		return "select"
	case UIMedia:
		return "media"
	case UIRule:
		return "rule"
	case UIMarker:
		return "marker"
	}
	return "?"
}

// UIBox is one element of the interface layer, in cell coordinates. The
// interface renderer draws these under the text; the text always wins.
type UIBox struct {
	Img   image.Image // a media element's own crop, rendered inside its frame
	Kind  UIKind
	X, Y  int
	W, H  int
	Text  string // frame label, media alt text, list marker
	On    bool   // a checked checkbox or radio
	Video bool   // a media box that is a <video>
	Level int    // heading level for UIRule
	Paint int64  // paint order, lowest drawn first
	Wide  int    // media size in cells, to decide a solid block
}

// Right and Bottom are the exclusive edges of the box.
func (b UIBox) Right() int  { return b.X + b.W }
func (b UIBox) Bottom() int { return b.Y + b.H }
