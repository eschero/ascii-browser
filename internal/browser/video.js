// In-page video renderer. One copy per document; Go injects it once and
// reconfigures it afterwards. A playing video is the only thing on a page
// that repaints dozens of times a second, and a screenshot round trip per
// frame costs more than the whole frame budget, so the picture is turned
// into characters here and only the characters cross the wire.
//
// The dithering is an ordered Bayer 8x8 matrix, not Floyd-Steinberg:
// error diffusion depends on every pixel before it, so a still area would
// pick different characters from one frame to the next and shimmer. An
// ordered matrix is a pure function of the position, so what does not
// move does not flicker.
(CFG) => {
  const NAME = '__asciiVideoFrame';
  if (window.__asciiVideoInstalled) {
    window.__asciiVideoConfigure(CFG);
    window.__asciiVideoScan();
    return 'reconfigured';
  }
  window.__asciiVideoInstalled = true;

  let cfg = CFG;
  // Bayer 8x8, normalized to [0,1): the classic ordered dither matrix.
  const BAYER = new Float32Array(64);
  for (let y = 0; y < 8; y++) {
    for (let x = 0; x < 8; x++) {
      let v = 0, mask = 4, bit = 0;
      let xc = x, yc = y;
      for (let i = 0; i < 3; i++) {
        const bx = (xc >> (2 - i)) & 1, by = (yc >> (2 - i)) & 1;
        v = (v << 2) | ((by ^ bx) << 1) | bx;
      }
      BAYER[y * 8 + x] = v / 64;
      void mask; void bit;
    }
  }

  const state = new WeakMap();
  const stats = { ticks: 0, draws: 0, sent: 0, errs: 0, lastErr: '', mode: '', ms: 0 };
  window.__asciiVideoStats = () => stats;

  window.__asciiVideoConfigure = c => { cfg = c; };

  function deepVideos(root, out) {
    for (const v of root.querySelectorAll('video')) out.push(v);
    for (const el of root.querySelectorAll('*')) if (el.shadowRoot) deepVideos(el.shadowRoot, out);
    return out;
  }

  function attach(v) {
    if (state.has(v) || typeof v.requestVideoFrameCallback !== 'function') return;
    const st = { canvas: null, ctx: null, w: 0, h: 0, n: 0, last: 0, stop: false };
    state.set(v, st);
    const tick = () => {
      stats.ticks++;
      if (st.stop) return;
      try {
        draw(v, st);
      } catch (e) {
        stats.errs++;
        stats.lastErr = String(e);
        // A video from another origin taints the canvas, so its pixels
        // cannot be read. Ask for it again with CORS; if that is refused
        // too, give up on this one and leave it to the still crop.
        if (e && (e.name === 'SecurityError' || /tainted/.test(String(e)))) {
          // A video from another origin without CORS taints the canvas,
          // so its pixels cannot be read here. Hand it to Go, which can
          // still photograph the box through Chrome. Reloading it with
          // crossOrigin is not worth trying: when the server sends no CORS
          // headers the video stops playing altogether.
          st.stop = true;
          stats.tainted = (stats.tainted || 0) + 1;
          try {
            const r = v.getBoundingClientRect();
            window[NAME](JSON.stringify({
              tainted: true,
              x: Math.round(r.left / cfg.cw), y: Math.round(r.top / cfg.ch),
              cols: Math.max(1, Math.round(r.width / cfg.cw)),
              rows: Math.max(1, Math.round(r.height / cfg.ch))
            }));
          } catch (e3) {}
        }
      }
      if (st.stop) return;
      schedule(v, st, tick);
    };
    schedule(v, st, tick);
  }

  // requestVideoFrameCallback fires once per decoded frame, which is what
  // we want, but it is tied to frames being presented and stays silent in
  // some headless setups. If it does not fire, fall back to the frame
  // scheduler, still one draw per animation frame at most.
  function schedule(v, st, tick) {
    if (!st.fallback && typeof v.requestVideoFrameCallback === 'function') {
      try {
        const id = v.requestVideoFrameCallback(() => { st.vticks = (st.vticks || 0) + 1; tick(); });
        stats.mode = 'rvfc';
        if (!st.armed) {
          st.armed = true;
          // Fall back to animation frames only for a video that is really
          // playing and still gets no frame callbacks. A video that was
          // paused, or not started yet, when this was installed is silent
          // for that reason, and falling back for it for good meant a draw
          // at every screen refresh, four times the frames it decodes.
          const check = () => {
            if (st.vticks) return;
            if (v.paused || v.readyState < 2) { setTimeout(check, 500); return; }
            if (st.checked) { st.fallback = true; stats.mode = 'raf'; requestAnimationFrame(tick); return; }
            st.checked = true;
            setTimeout(check, 500);
          };
          setTimeout(check, 500);
        }
        return id;
      } catch (e) { st.fallback = true; }
    }
    stats.mode = 'raf';
    requestAnimationFrame(tick);
  }

  function draw(v, st) {
    // On animation frames, a frame already drawn is not drawn again.
    if (st.fallback) {
      if (v.paused && st.drewPaused) return;
      if (v.currentTime === st.lastTime && st.n > 0) return;
      st.lastTime = v.currentTime;
      st.drewPaused = v.paused;
    }
    stats.draws++;
    const t0 = performance.now();
    if (cfg.fps > 0) {
      const now = performance.now();
      if (now - st.last < 1000 / cfg.fps) return;
      st.last = now;
    }
    const r = v.getBoundingClientRect();
    const cols = Math.max(1, Math.round(r.width / cfg.cw));
    const rows = Math.max(1, Math.round(r.height / cfg.ch));
    const x = Math.round(r.left / cfg.cw), y = Math.round(r.top / cfg.ch);
    if (x + cols <= 0 || y + rows <= 0 || x >= cfg.cols || y >= cfg.rows) return;
    if (!v.videoWidth || !v.videoHeight) return;
    if (!st.canvas || st.w !== cols || st.h !== rows) {
      st.canvas = new OffscreenCanvas(cols, rows);
      st.ctx = st.canvas.getContext('2d', { willReadFrequently: true, alpha: false });
      st.w = cols; st.h = rows;
    }
    // One canvas pixel per cell: the graphics engine does the scaling.
    // The picture keeps its proportions inside the element's box, as the
    // page shows it (object-fit contain, a video's default): fullscreen, or
    // a player wider than the video, leaves bands that are not the picture
    // and are left blank rather than drawn as solid black.
    const cellAspect = cfg.ch / cfg.cw;
    const va = v.videoWidth / v.videoHeight;
    let px = 0, py = 0, pw = cols, ph = rows;
    const fit = getComputedStyle(v).objectFit;
    if (fit === 'contain' || fit === 'scale-down' || fit === '') {
      const boxAspect = (cols / rows) / cellAspect;
      if (boxAspect > va * 1.02) { pw = Math.max(1, Math.round(rows * cellAspect * va)); px = Math.floor((cols - pw) / 2); }
      else if (boxAspect < va / 1.02) { ph = Math.max(1, Math.round(cols / cellAspect / va)); py = Math.floor((rows - ph) / 2); }
    }
    st.ctx.fillStyle = '#000';
    st.ctx.fillRect(0, 0, cols, rows);
    st.ctx.drawImage(v, px, py, pw, ph);
    const d = st.ctx.getImageData(0, 0, cols, rows).data;
    const inPic = (i, j) => i >= px && i < px + pw && j >= py && j < py + ph;
    const ramp = cfg.ramp, top = ramp.length - 1;
    const n = cols * rows;
    if (!st.luma || st.luma.length !== n) st.luma = new Float32Array(n);
    const L = st.luma, hist = new Uint32Array(64);
    let counted = 0;
    for (let q = 0, p = 0; q < n; q++, p += 4) {
      const l = (0.2126 * d[p] + 0.7152 * d[p + 1] + 0.0722 * d[p + 2]) / 255;
      L[q] = l;
      if (!inPic(q % cols, (q / cols) | 0)) continue;
      hist[Math.min(63, (l * 64) | 0)]++;
      counted++;
    }
    const total = Math.max(1, counted);
    // Levels: stretch the frame's own range, 2nd to 98th percentile, onto
    // the whole ramp. A video is rarely black to white, and mapped as it is
    // it comes out washed out, a few characters doing all the work. The
    // range is eased from frame to frame, so a cut changes it within a few
    // frames and a still scene never breathes. The paper threshold is for
    // a page's white background and is not applied here: in a video it
    // blanks the sky.
    let acc = 0, lo = 0, hi = 1;
    for (let b = 0; b < 64; b++) { acc += hist[b]; if (acc >= total * 0.02) { lo = b / 64; break; } }
    acc = 0;
    for (let b = 63; b >= 0; b--) { acc += hist[b]; if (acc >= total * 0.02) { hi = (b + 1) / 64; break; } }
    if (hi - lo < 0.15) { const m = (hi + lo) / 2; lo = Math.max(0, m - 0.075); hi = Math.min(1, m + 0.075); }
    if (st.lo === undefined) { st.lo = lo; st.hi = hi; }
    else { st.lo += (lo - st.lo) * 0.25; st.hi += (hi - st.hi) * 0.25; }
    const span = Math.max(0.05, st.hi - st.lo);
    const rows_ = [];
    for (let j = 0; j < rows; j++) {
      let line = '';
      for (let i = 0; i < cols; i++) {
        if (!inPic(i, j)) { line += ' '; continue; }
        let l = (L[j * cols + i] - st.lo) / span;
        if (l < 0) l = 0; else if (l > 1) l = 1;
        const value = cfg.invert ? l : 1 - l;
        let k = Math.floor(value * top + BAYER[(j & 7) * 8 + (i & 7)]);
        if (k < 0) k = 0; else if (k > top) k = top;
        line += ramp[k];
      }
      rows_.push(line);
    }
    st.n++;
    stats.sent++;
    stats.ms += performance.now() - t0;
    if (st.n === 1 && cfg.once) st.stop = true;
    window[NAME](JSON.stringify({ x, y, cols, rows, n: st.n, lines: rows_ }));
  }

  window.__asciiVideoScan = () => {
    const vs = deepVideos(document, []);
    for (const v of vs) {
      // Start it once; after that whether it plays is the reader's call.
      if (!v.__asciiTried) {
        v.__asciiTried = true;
        if (cfg.audio) {
          // Sound on: the page decides whether a video plays, and with
          // what volume. Only one it meant to autoplay is nudged.
          if (v.autoplay || v.muted) { try { const p = v.play(); if (p && p.catch) p.catch(() => {}); } catch (e) {} }
        } else {
          try { v.muted = true; const p = v.play(); if (p && p.catch) p.catch(() => {}); } catch (e) {}
        }
      }
      attach(v);
    }
    return vs.length;
  };
  return 'installed:' + window.__asciiVideoScan();
}
