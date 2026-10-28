package caption

import (
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	_ "golang.org/x/image/webp"
)

// Load reads a picture from a URL or a file. No browser is needed: the
// caption is made from the picture's own pixels.
func Load(src string) (image.Image, error) {
	var r io.ReadCloser
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		c := &http.Client{Timeout: 30 * time.Second}
		req, err := http.NewRequest("GET", src, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/153.0.0.0 Safari/537.36")
		resp, err := c.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return nil, fmt.Errorf("%s: %s", src, resp.Status)
		}
		r = resp.Body
	} else {
		f, err := os.Open(src)
		if err != nil {
			return nil, err
		}
		r = f
	}
	defer r.Close()
	img, _, err := image.Decode(r)
	return img, err
}
