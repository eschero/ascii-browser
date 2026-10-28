package browser

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
)

//go:embed textwalker.js
var textWalkerJS string

// gridStyleScript returns the script registered with
// Page.addScriptToEvaluateOnNewDocument: it appends a <style> with the
// BlankMono @font-face and the grid rule as soon as the document has a root
// element, so the page is laid out on the grid from its first paint.
func gridStyleScript(css string) string {
	quoted, _ := json.Marshal(css)
	return fmt.Sprintf(`(() => {
  const css = %s;
  const add = () => {
    const root = document.documentElement;
    if (!root) return false;
    const s = document.createElement('style');
    s.setAttribute('data-ascii-browser', '');
    s.textContent = css;
    root.appendChild(s);
    return true;
  };
  if (!add()) {
    const mo = new MutationObserver(() => { if (add()) mo.disconnect(); });
    mo.observe(document, { childList: true });
  }
})();`, quoted)
}

// textWalkerExpr is the expression evaluated to collect text runs. The
// cell size is in CSS pixels, so cellW/zoom x cellH/zoom.
func textWalkerExpr(cellW, cellH float64, cols, rows int, opacity string) string {
	return fmt.Sprintf("(%s)(%s,%s,%d,%d,%q)", textWalkerJS,
		strconv.FormatFloat(cellW, 'f', -1, 64), strconv.FormatFloat(cellH, 'f', -1, 64),
		cols, rows, opacity)
}
