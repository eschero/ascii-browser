// Text walker. Evaluated through Runtime.evaluate as
//   (CW, CH, COLS, ROWS, OPACITY) => [{text, x, y, w, h, link, href, lid, field}, ...]
// with coordinates in grid cells, viewport relative.
//
// Approach: walk the composed (flat) tree, entering open shadow roots and
// following slots to their assigned nodes, so text inside web components
// is placed where it renders. For every text node whose box intersects
// the viewport, measure each character with a one-character Range. The
// injected CSS forces BlankMono (advance CW) and line-height CH on
// everything, so every glyph sits in exactly one cell and rounding its
// rect gives the cell. Collapsed whitespace has a zero-width rect and is
// skipped. Runs of adjacent cells are merged, then tested for occlusion
// with the hit-test stack of the node's own tree, so text under a fixed
// header or a modal is not drawn, while text under a transparent
// click-catcher still is.
(CW, CH, COLS, ROWS, OPACITY) => {
  const VW = COLS * CW, VH = ROWS * CH;
  // Elements never descended into: their text is not page text. Form
  // controls are handled from their live value when met.
  const SKIP = new Set(['SCRIPT', 'STYLE', 'NOSCRIPT', 'TEMPLATE', 'TITLE', 'HEAD']);
  const FIELDS = new Set(['INPUT', 'TEXTAREA', 'SELECT']);
  const runs = [];
  const opacityCache = new Map(), visCache = new Map(), clipCache = new Map(),
        transCache = new Map(), linkCache = new Map(), transformCache = new Map();

  // ---- composed tree helpers

  // flatParent is the rendering parent: the slot for a slotted node, the
  // host for a shadow root's child, else the DOM parent.
  function flatParent(n) {
    if (n.assignedSlot) return n.assignedSlot;
    const p = n.parentNode;
    if (!p) return null;
    if (p.nodeType === 11) return p.host || null; // DocumentFragment: a ShadowRoot
    return p.nodeType === 1 ? p : null;
  }
  function flatContains(a, b) { // a is b or a flat-tree ancestor of b
    for (let n = b; n; n = flatParent(n)) if (n === a) return true;
    return false;
  }
  function isRootEl(el) { return !el || el === el.ownerDocument.documentElement; }

  // ---- styles

  function transformOf(el) {
    let v = transformCache.get(el);
    if (v === undefined) { v = getComputedStyle(el).textTransform; transformCache.set(el, v); }
    return v;
  }
  function transformChar(ch, mode) {
    if (mode === 'uppercase') { const u = ch.toUpperCase(); return u.length === ch.length ? u : ch; }
    if (mode === 'lowercase') { const l = ch.toLowerCase(); return l.length === ch.length ? l : ch; }
    return ch;
  }

  // opacity:0 anywhere up the flat tree hides the subtree. visibility is
  // read on the element itself: computed values already follow the flat
  // tree, and a child may re-enable it.
  function hiddenByOpacity(el) {
    if (isRootEl(el)) return false;
    // "ignore": the text is read from the DOM even where the page paints
    // nothing, for sites whose reveal animations never fire headless.
    if (OPACITY === 'ignore') return false;
    let v = opacityCache.get(el);
    if (v === undefined) {
      const cs = getComputedStyle(el);
      // Opacity 0 with a transition or an animation on it is not hidden,
      // it is waiting to appear: a page that fades its sections in as the
      // reader reaches them would otherwise be read half empty.
      // A transition only counts when it has a duration: every element
      // reports transition-property "all" by default, so the property
      // alone would make anything at opacity 0 look like it is arriving.
      const running = d => (d || '').split(',').some(v => parseFloat(v) > 0);
      const fading = OPACITY !== 'strict' &&
        (((cs.animationName && cs.animationName !== 'none') && running(cs.animationDuration)) ||
         (running(cs.transitionDuration) &&
          (cs.transitionProperty === 'all' || /opacity/.test(cs.transitionProperty || ''))));
      v = (parseFloat(cs.opacity) === 0 && !fading) || hiddenByOpacity(flatParent(el));
      opacityCache.set(el, v);
    }
    return v;
  }
  function invisible(el) {
    let v = visCache.get(el);
    if (v === undefined) {
      const cs = getComputedStyle(el);
      v = cs.display === 'none' || cs.visibility !== 'visible' || hiddenByOpacity(el);
      visCache.set(el, v);
    }
    return v;
  }

  // Clipping follows CSS containing blocks, not just ancestry: an
  // overflow != visible box clips its in-flow content and the absolutely
  // positioned descendants it contains, but a fixed element escapes every
  // ancestor clip and an absolute one escapes the clips between it and
  // its containing block (the nearest positioned ancestor).
  const NOCLIP = { l: -1e9, t: -1e9, r: 1e9, b: 1e9 };
  const clipOfCache = new Map();
  // clipOf: the clip that applies to el's own box.
  function clipOf(el) {
    if (isRootEl(el)) return NOCLIP;
    let c = clipOfCache.get(el);
    if (c === undefined) {
      const pos = getComputedStyle(el).position;
      if (pos === 'fixed') {
        c = NOCLIP;
      } else if (pos === 'absolute') {
        let cb = flatParent(el);
        while (!isRootEl(cb) && getComputedStyle(cb).position === 'static') cb = flatParent(cb);
        c = clipRect(cb);
      } else {
        c = clipRect(flatParent(el));
      }
      clipOfCache.set(el, c);
    }
    return c;
  }
  // clipRect: the clip that applies to el's content (its text and in-flow
  // children): el's own overflow box, within the clip on el itself.
  function clipRect(el) {
    if (isRootEl(el)) return NOCLIP;
    let c = clipCache.get(el);
    if (c === undefined) {
      c = clipOf(el);
      const cs = getComputedStyle(el);
      if (cs.overflowX !== 'visible' || cs.overflowY !== 'visible') {
        const b = el.getBoundingClientRect();
        c = { l: Math.max(c.l, b.left), t: Math.max(c.t, b.top),
              r: Math.min(c.r, b.right), b: Math.min(c.b, b.bottom) };
      }
      clipCache.set(el, c);
    }
    return c;
  }
  const inside = (c, x, y) => x >= c.l && x < c.r && y >= c.t && y < c.b;

  // Enclosing anchor or button in the flat tree (null if none) and a
  // per-capture id for each distinct one, so the UI can group the runs of
  // one link or button and click it with Enter.
  const linkIds = new Map();
  let nextLid = 1;
  function isButton(n) {
    return n.tagName === 'BUTTON' || n.getAttribute('role') === 'button' ||
           (n.tagName === 'INPUT' && ['submit', 'button', 'reset'].includes(n.type));
  }
  function anchorOf(el) {
    let a = linkCache.get(el);
    if (a === undefined) {
      a = null;
      for (let n = el; n && !isRootEl(n); n = flatParent(n)) {
        if ((n.tagName === 'A' && n.hasAttribute('href')) || isButton(n)) { a = n; break; }
      }
      linkCache.set(el, a);
    }
    return a;
  }
  function linkIdOf(a) {
    let id = linkIds.get(a);
    if (id === undefined) { id = nextLid++; linkIds.set(a, id); }
    return id;
  }

  // ---- occlusion

  function alpha(color) {
    if (color === 'transparent') return 0;
    const m = /^rgba?\(\s*[\d.]+\s*,\s*[\d.]+\s*,\s*[\d.]+\s*(?:,\s*([\d.]+)\s*)?\)$/.exec(color);
    if (m) return m[1] === undefined ? 1 : parseFloat(m[1]);
    return 1;
  }
  const REPLACED = new Set(['IMG', 'SVG', 'CANVAS', 'VIDEO', 'PICTURE', 'OBJECT', 'EMBED', 'IFRAME']);
  function ownText(el) {
    for (const n of el.childNodes) if (n.nodeType === 3 && /\S/.test(n.data)) return true;
    return false;
  }
  // Does the element itself put pixels on screen: a background, a visible
  // border, text of its own, or a replaced element? Its children are not
  // counted: in a hit-test stack they appear on their own, above it. An
  // element that paints nothing (a click-catcher, a ripple host, an
  // invisible overlay) does not occlude what is beneath.
  function paintsSelf(el) {
    let t = transCache.get(el);
    if (t === undefined) {
      const cs = getComputedStyle(el);
      t = !invisible(el) && (
        alpha(cs.backgroundColor) > 0 || cs.backgroundImage !== 'none' || REPLACED.has(el.tagName) || ownText(el) ||
        (parseFloat(cs.borderTopWidth) > 0 && cs.borderTopStyle !== 'none' && alpha(cs.borderTopColor) > 0) ||
        (parseFloat(cs.borderBottomWidth) > 0 && cs.borderBottomStyle !== 'none' && alpha(cs.borderBottomColor) > 0) ||
        (parseFloat(cs.borderLeftWidth) > 0 && cs.borderLeftStyle !== 'none' && alpha(cs.borderLeftColor) > 0) ||
        (parseFloat(cs.borderRightWidth) > 0 && cs.borderRightStyle !== 'none' && alpha(cs.borderRightColor) > 0));
      transCache.set(el, t);
    }
    return t;
  }
  const transparent = el => !paintsSelf(el);

  // An element that takes no pointer events can never be the answer to a
  // hit test, so hit testing cannot say whether it is covered: treat it
  // as visible. Text laid over a picture is usually marked this way.
  const pointerCache = new Map();
  function unhittable(el) {
    if (!el || el.nodeType !== 1 || isRootEl(el)) return false;
    let v = pointerCache.get(el);
    if (v === undefined) {
      v = getComputedStyle(el).pointerEvents === 'none' || unhittable(flatParent(el));
      pointerCache.set(el, v);
    }
    return v;
  }

  // Is the point painted by el (or a flat-tree descendant)? Hit-test from
  // the node's own root: a ShadowRoot returns its own elements, nested
  // components as their hosts, and outer elements as themselves. Walk the
  // stack top-down, skipping elements that paint nothing themselves.
  // Reaching a flat-tree ancestor of el means el itself is not
  // hit-testable (pointer-events: none): visible.
  function covered(root, el, x, y) {
    if (unhittable(el)) return true;
    const hits = root.elementsFromPoint ? root.elementsFromPoint(x, y) : document.elementsFromPoint(x, y);
    for (const hit of hits) {
      if (hit === el || flatContains(el, hit) || flatContains(hit, el)) return true;
      if (!transparent(hit)) return false;
    }
    return false;
  }

  // Close a run: occlusion test at both ends, bisection if they disagree.
  function emit(r) {
    const a = covered(r.root, r.el, r.lx + CW / 2, r.ly);
    const b = covered(r.root, r.el, r.lx + (r.w - 0.5) * CW, r.ly);
    if (a && b) {
      const isA = r.a !== null && r.a.tagName === 'A';
      runs.push({ text: r.cells.join(''), x: r.x, y: r.y, w: r.w, h: 1,
                  link: isA, href: isA ? r.a.href : '', button: r.a !== null && !isA,
                  lid: r.a ? linkIdOf(r.a) : 0 });
      return;
    }
    if ((!a && !b) || r.w < 2) return;
    const k = r.w >> 1;
    emit({ ...r, cells: r.cells.slice(0, k), w: k });
    emit({ ...r, cells: r.cells.slice(k), x: r.x + k, w: r.w - k, lx: r.lx + k * CW });
  }
  const flush = r => { if (r) emit(r); return null; };

  // ---- traversal

  let budget = 2 * COLS * ROWS + 50000; // characters measured per call, worst case
  let exhausted = false;

  function textNode(node, doc, ox, oy) {
    if (!/\S/.test(node.data)) return;
    const el = flatParent(node);
    if (!el || el.nodeType !== 1 || invisible(el)) return;
    const range = doc.createRange();
    range.selectNodeContents(node);
    const box = range.getBoundingClientRect();
    if (box.width === 0 || box.height === 0) return;
    if (box.right + ox <= 0 || box.left + ox >= VW ||
        box.bottom + oy <= 0 || box.top + oy >= VH) return; // off screen: no per-char work
    const root = node.getRootNode();
    const clip = clipRect(el), a = anchorOf(el), tt = transformOf(el), s = node.data;
    let run = null;
    for (let i = 0; i < s.length;) {
      const cp = s.codePointAt(i), len = cp > 0xFFFF ? 2 : 1;
      range.setStart(node, i); range.setEnd(node, i + len); i += len;
      const r = range.getBoundingClientRect();
      if (r.width === 0 || r.height === 0) { run = flush(run); continue; } // collapsed whitespace
      const lx = r.left, ly = r.top + r.height / 2;
      if (!inside(clip, lx + r.width / 2, ly)) { run = flush(run); continue; }
      const col = Math.round((lx + ox) / CW), row = Math.floor((ly + oy) / CH);
      const w = Math.max(1, Math.round(r.width / CW));
      if (row < 0 || row >= ROWS || col < 0 || col + w > COLS) { run = flush(run); continue; }
      const cells = [transformChar(String.fromCodePoint(cp), tt)];
      // Dead by construction: BlankMono forces the same advance on every
      // code point, so w is always 1. Kept for a font that gives wide
      // glyphs two cells; the padding keeps the following characters in
      // their measured cells.
      // for (let k = 1; k < w; k++) cells.push(' ');
      if (run && row === run.y && col === run.x + run.w) { run.cells.push(...cells); run.w += w; }
      else { run = flush(run); run = { cells, x: col, y: row, w, el, root, lx, ly, a }; }
      if (--budget === 0) { exhausted = true; break; }
    }
    flush(run);
  }

  // Form controls: their visible text is not a text node. A field is
  // reported even when empty, with W covering its box, so it can be
  // reached with Tab and highlighted.
  function field(el, ox, oy) {
    if (['hidden', 'checkbox', 'radio', 'file', 'image', 'range', 'color'].includes(el.type) || invisible(el)) return;
    if (isButton(el)) {
      // An <input type=submit>: its label is its value, centered in its box.
      const b = el.getBoundingClientRect();
      const label = [...(el.value || 'Submit')];
      const row = Math.floor((b.top + oy + b.height / 2) / CH);
      const col = Math.round((b.left + ox + (b.width - label.length * CW) / 2) / CW);
      if (b.width === 0 || row < 0 || row >= ROWS || col < 0 || col + label.length > COLS) return;
      if (!covered(el.getRootNode(), el, b.left - ox + b.width / 2, b.top - oy + b.height / 2)) return;
      runs.push({ text: label.join(''), x: col, y: row, w: label.length, h: 1, link: false, button: true, lid: linkIdOf(el) });
      return;
    }
    let v = el.value;
    if (el.tagName === 'SELECT') v = el.selectedIndex >= 0 ? el.options[el.selectedIndex].text : '';
    if (!v) v = el.placeholder || '';
    if (el.type === 'password') v = '*'.repeat(v.length);
    const b = el.getBoundingClientRect(), cs = getComputedStyle(el);
    if (b.width === 0 || b.height === 0) return;
    const x0 = b.left + ox + parseFloat(cs.paddingLeft) + parseFloat(cs.borderLeftWidth);
    const yc = b.top + oy + (el.tagName === 'TEXTAREA' ? parseFloat(cs.paddingTop) + parseFloat(cs.borderTopWidth) + CH / 2 : b.height / 2);
    const row = Math.floor(yc / CH), col = Math.round(x0 / CW);
    const w = Math.min(Math.max(1, Math.floor((b.right + ox - x0) / CW)), COLS - col);
    if (w <= 0 || row < 0 || row >= ROWS || col < 0) return;
    if (b.bottom + oy <= 0 || b.top + oy >= VH) return;
    if (!covered(el.getRootNode(), el, x0 - ox + CW / 2, yc - oy)) return;
    const chars = [...v.split('\n')[0]].slice(0, w);
    runs.push({ text: chars.join(''), x: col, y: row, w, h: 1, link: false, field: true });
  }

  // visit walks the composed tree: an element with an open shadow root
  // renders its shadow children; its light children appear only through
  // slots, which hand out their assigned nodes (or fallback content).
  function visit(node, doc, ox, oy) {
    if (exhausted) return;
    if (node.nodeType === 3) { textNode(node, doc, ox, oy); return; }
    if (node.nodeType !== 1) return;
    const el = node, tag = el.tagName;
    if (SKIP.has(tag)) return;
    if (FIELDS.has(tag)) { field(el, ox, oy); return; }
    if (tag === 'IFRAME') {
      let d = null;
      try { d = el.contentDocument; } catch (e) { /* cross-origin */ }
      if (d && d.body && !invisible(el)) {
        const b = el.getBoundingClientRect();
        visit(d.body, d, ox + b.left + el.clientLeft, oy + b.top + el.clientTop);
      }
      return;
    }
    if (tag === 'SLOT') {
      for (const n of el.assignedNodes({ flatten: true })) visit(n, doc, ox, oy);
      return;
    }
    const kids = el.shadowRoot ? el.shadowRoot.childNodes : el.childNodes;
    for (const k of kids) visit(k, doc, ox, oy);
  }

  if (document.body) visit(document.body, document, 0, 0);
  return runs;
}
