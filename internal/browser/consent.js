// Consent banners. Run in every frame, in an isolated world: finds the
// button that answers a cookie or privacy banner and clicks it, preferring
// the refusal when PREFER is "reject". A button only counts when it sits in
// something that talks about cookies or consent, or in a frame that is a
// consent manager, so an ordinary "Accept" elsewhere is never pressed.
(PREFER) => {
  const REJECT = /^(rifiuta( tutt[oi])?|rifiuto|non accetto|continua senza accettare|solo (i )?(cookie )?(necessari|tecnici)|reject( all)?|decline( all)?|refuse( all)?|deny( all)?|i do not agree|do not agree|disagree|only (necessary|essential|required)|necessary (cookies )?only|use necessary cookies only|continue without (accepting|agreeing)|alle ablehnen|ablehnen|nur (notwendige|erforderliche|essenzielle)|rechazar( todo| todas)?|solo (las )?necesarias|tout refuser|refuser( tout)?|continuer sans accepter|weigeren|alles weigeren|non consentire|nega|non accettare)\b/i;
  const ACCEPT = /^(accetta( tutt[oi])?|accetto|acconsento|ho capito|ok|okay|va bene|accept( all)?|allow( all)?|i agree|agree|got it|i understand|consent|continue|yes,? i('| a)m happy|alle akzeptieren|akzeptieren|zustimmen|einverstanden|alle erlauben|aceptar( todo| todas)?|acepto|tout accepter|accepter( tout)?|j'accepte|d'accord|alles accepteren|accepteren|consenti( tutt[oi])?|autorizza|zgadzam si[eę]|принять)\b/i;
  // A pay-or-consent wall offers the refusal as a subscription: never
  // follow it, the acceptance is the only way to read such a page for free.
  const PAYWALL = /abbona|abbonat|subscri|abonn|\babo\b|abo abschlie|\bpay\b|pagare|paga |zahlen|pur-abo|pur abo|suscr|premium|sign up|registr|log ?in|accedi|anmelden/i;
  const TOPIC = /cookie|consent|consenso|privacy|gdpr|datenschutz|tracking|personal data|dati personali|partner|vendor|preferenze|preferences|einwilligung|consentimiento|confidentialit/i;
  const frameIsCMP = /consent|cmp|privacy|cookie|gdpr|sourcepoint|onetrust|didomi|quantcast|trustarc|usercentrics|cookiebot|iubenda|sp_message/i.test(location.href);
  const clean = s => (s || '').replace(/\s+/g, ' ').trim().replace(/[.!…]+$/, '');
  const shown = el => {
    const r = el.getBoundingClientRect();
    if (r.width < 4 || r.height < 4) return false;
    const cs = getComputedStyle(el);
    if (cs.visibility === 'hidden' || cs.display === 'none' || parseFloat(cs.opacity) <= 0.05) return false;
    // On top at its own centre, or a banner that has closed leaves its
    // buttons behind, sized but covered or clipped away, and they would be
    // pressed again.
    const x = r.left + r.width / 2, y = r.top + r.height / 2;
    if (x < 0 || y < 0 || x > innerWidth || y > innerHeight) return true; // off screen: cannot tell
    const root = el.getRootNode && el.getRootNode();
    const hit = (root && root.elementFromPoint ? root : document).elementFromPoint(x, y);
    return !hit || hit === el || el.contains(hit) || hit.contains(el);
  };
  const deep = (root, out) => {
    for (const el of root.querySelectorAll('button,a,[role=button],input[type=button],input[type=submit],div[tabindex],span[tabindex]')) out.push(el);
    for (const el of root.querySelectorAll('*')) if (el.shadowRoot) deep(el.shadowRoot, out);
    return out;
  };
  const labelOf = el => clean(el.value || el.innerText || el.textContent || el.getAttribute('aria-label') || el.title);
  const aboutConsent = el => {
    if (frameIsCMP) return true;
    let n = 0;
    for (let e = el; e && n < 12; n++) {
      const t = (e.id || '') + ' ' + (typeof e.className === 'string' ? e.className : '') + ' ' + (e.getAttribute && (e.getAttribute('aria-label') || '') || '');
      if (TOPIC.test(t)) return true;
      if (e.innerText && e.innerText.length < 4000 && TOPIC.test(e.innerText)) return true;
      e = e.parentElement || (e.getRootNode && e.getRootNode().host);
    }
    return false;
  };
  const cands = deep(document, []).filter(shown);
  const pick = re => cands.find(el => { const l = labelOf(el); return l.length > 0 && l.length < 45 && re.test(l) && !PAYWALL.test(l) && aboutConsent(el); });
  const order = PREFER === 'accept' ? [ACCEPT, REJECT] : [REJECT, ACCEPT];
  // PREFER "close": no banner left, but a dialog covering the page, a
  // sign-in prompt, a newsletter pop-up: press its close button.
  if (PREFER === 'close') {
    const CLOSE = /^(chiudi|close|dismiss|no,? grazie|no,? thanks|not now|non ora|maybe later|schlie(ss|ß)en|cerrar|fermer|×|✕|✖|x)$/i;
    const dialogs = [];
    const dd = (root) => { for (const d of root.querySelectorAll('[role=dialog],[aria-modal=true],dialog[open]')) dialogs.push(d);
      for (const e of root.querySelectorAll('*')) if (e.shadowRoot) dd(e.shadowRoot); };
    dd(document);
    for (const d of dialogs) {
      const r = d.getBoundingClientRect();
      if (!shown(d) || r.width * r.height < innerWidth * innerHeight * 0.08) continue;
      const btn = deep(d, []).filter(shown).find(el => {
        const l = labelOf(el) || clean(el.getAttribute('aria-label'));
        const a = clean(el.getAttribute('aria-label') || el.title);
        return (l.length < 20 && CLOSE.test(l)) || /^(chiudi|close|dismiss|ignora|ignore|schlie|cerrar|fermer)/i.test(a) || /^(ignora|ignore) /i.test(l);
      });
      if (btn) { const l = labelOf(btn) || btn.getAttribute('aria-label') || 'close'; try { btn.click(); } catch (e) { continue; } return l; }
    }
    return '';
  }
  for (const re of order) {
    const el = pick(re);
    if (el) {
      const label = labelOf(el);
      try { el.click(); } catch (e) { return ''; }
      return label;
    }
  }
  // Nothing to press, but the page mentions cookies: a banner still being
  // mounted, or waiting for a script. Tell Go to look again shortly.
  if (window === top && document.readyState !== 'complete') return '\u0000wait';
  const body = document.body ? document.body.innerText.slice(0, 20000) : '';
  if (/cookie|consenso|consent/i.test(body)) {
    window.__asciiConsentWaited = (window.__asciiConsentWaited || 0) + 1;
    if (window.__asciiConsentWaited <= 4) return '\u0000wait';
  }
  return '';
}
