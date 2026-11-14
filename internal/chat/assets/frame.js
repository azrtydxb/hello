/* Hello chat UI. It runs in the sandboxed frame on Hello's own origin. Every
 * piece of visitor, agent and server text is put on the page as a text node:
 * nothing here builds markup from text, and no link is made from a message. */
(function () {
  'use strict';
  var BASE = '/chat/v1';
  var SITE = new URLSearchParams(location.search).get('site') || '';
  var KEY = 'hello-chat:' + SITE;
  var app = document.getElementById('app');

  var s = {
    open: false, cfg: null, challenge: null, token: '', booted: false, closed: false,
    parent: null, page: '', referrer: '', unread: 0, lastLine: 0, lastEvent: '', seen: {},
    streaming: false, typingTimer: null, lastTypingSent: 0, backoff: 3000, contact: null
  };
  var ui = {};

  /* ---- storage (blocked storage still chats for the session) ---- */
  function load() {
    try { s.token = localStorage.getItem(KEY) || ''; } catch (e) { /* blocked */ }
  }
  function save(t) {
    s.token = t;
    try { if (t) { localStorage.setItem(KEY, t); } else { localStorage.removeItem(KEY); } } catch (e) { /* blocked */ }
  }

  /* ---- DOM helpers: text nodes only ---- */
  function el(tag, cls, text) {
    var n = document.createElement(tag);
    if (cls) { n.className = cls; }
    if (text != null) { n.appendChild(document.createTextNode(text)); }
    return n;
  }
  function clear(n) { while (n.firstChild) { n.removeChild(n.firstChild); } }
  function randomId() {
    var a = new Uint8Array(12);
    (window.crypto || window.msCrypto).getRandomValues(a);
    var out = '';
    for (var i = 0; i < a.length; i++) { out += ('0' + a[i].toString(16)).slice(-2); }
    return out;
  }

  /* ---- talking to the page that embeds us ---- */
  function tell() {
    if (!s.parent) { return; }
    window.parent.postMessage({
      hello: 'chat', type: 'state', open: s.open, unread: s.unread,
      hidden: !!(s.cfg && s.cfg.hidden && !s.token), position: s.cfg ? s.cfg.position : 'right'
    }, s.parent);
  }
  window.addEventListener('message', function (e) {
    if (e.source !== window.parent) { return; }
    var d = e.data;
    if (!d || d.hello !== 'chat') { return; }
    if (d.type === 'init') {
      s.parent = e.origin;
      s.page = typeof d.page === 'string' ? d.page.slice(0, 500) : '';
      s.referrer = typeof d.referrer === 'string' ? d.referrer.slice(0, 500) : '';
      tell();
    } else if (d.type === 'open') { setOpen(true); } else if (d.type === 'close') { setOpen(false); }
  });

  /* ---- HTTP ---- */
  function call(method, path, body, extra) {
    var h = { 'Accept': 'application/json' };
    if (body !== undefined) { h['Content-Type'] = 'application/json'; }
    if (s.token && (!extra || extra.auth !== false)) { h.Authorization = 'Bearer ' + s.token; }
    var sep = path.indexOf('?') >= 0 ? '&' : '?';
    return fetch(BASE + path + sep + 'site=' + encodeURIComponent(SITE), {
      method: method, headers: h, body: body === undefined ? undefined : JSON.stringify(body), cache: 'no-store', credentials: 'omit'
    }).then(function (r) {
      return r.text().then(function (t) {
        var j = null;
        try { j = t ? JSON.parse(t) : null; } catch (e) { j = null; }
        return { status: r.status, json: j, retry: parseInt(r.headers.get('Retry-After') || '0', 10) };
      });
    });
  }

  /* ---- layout ---- */
  function build() {
    ui.launcher = el('button', 'launcher right', '\u{1F4AC}');
    ui.launcher.type = 'button';
    ui.launcher.setAttribute('aria-label', 'Open chat');
    ui.badge = el('span', 'badge', '0');
    ui.badge.hidden = true;
    ui.launcher.appendChild(ui.badge);
    ui.launcher.addEventListener('click', function () { setOpen(true); });

    ui.panel = el('div', 'panel');
    ui.panel.hidden = true;
    var head = el('div', 'head');
    ui.title = el('div', 'title', 'Chat');
    ui.transcript = el('button', null, 'Email transcript');
    ui.transcript.type = 'button';
    ui.transcript.hidden = true;
    ui.transcript.addEventListener('click', sendTranscript);
    ui.end = el('button', null, 'End chat');
    ui.end.type = 'button';
    ui.end.hidden = true;
    ui.end.addEventListener('click', endChat);
    var close = el('button', null, '✕');
    close.type = 'button';
    close.setAttribute('aria-label', 'Close chat');
    close.addEventListener('click', function () { setOpen(false); });
    head.appendChild(ui.title); head.appendChild(ui.transcript); head.appendChild(ui.end); head.appendChild(close);

    ui.body = el('div', 'center', 'Connecting…');
    ui.panel.appendChild(head);
    ui.panel.appendChild(ui.body);
    app.appendChild(ui.panel);
    app.appendChild(ui.launcher);
  }

  function setOpen(o) {
    s.open = o;
    ui.panel.hidden = !o;
    ui.launcher.hidden = o;
    if (o) {
      s.unread = 0;
      renderBadge();
      if (!s.booted) { boot(); } else if (ui.input) { ui.input.focus(); }
      markVisible();
    }
    tell();
  }
  function renderBadge() {
    ui.badge.hidden = s.unread === 0;
    clear(ui.badge);
    ui.badge.appendChild(document.createTextNode(String(s.unread)));
  }
  function placeLauncher() {
    ui.launcher.className = 'launcher ' + (s.cfg && s.cfg.position === 'left' ? 'left' : 'right');
  }
  function setBody(node) {
    ui.panel.removeChild(ui.body);
    ui.body = node;
    ui.panel.appendChild(node);
  }
  function message(text) {
    ui.end.hidden = true;
    setBody(el('div', 'center', text));
  }

  /* ---- start up ---- */
  function boot() {
    s.booted = true;
    call('GET', '/challenge', undefined, { auth: false }).then(function (r) {
      if (r.status === 429) { s.booted = false; message('Please try again in a minute.'); return; }
      if (r.status !== 200 || !r.json) { message('Chat is not available right now.'); return; }
      s.cfg = r.json.config;
      s.challenge = r.json.challenge;
      document.documentElement.style.setProperty('--accent', s.cfg.accent);
      ui.title.textContent = s.cfg.name;
      ui.transcript.hidden = !s.cfg.transcripts;
      placeLauncher();
      tell();
      if (s.cfg.hidden && !s.token) { message('Chat is closed right now.'); return; }
      if (s.token) { resume(); } else { prechat(); }
    }).catch(function () { s.booted = false; message('Chat is not available right now.'); });
  }

  function resume() {
    call('POST', '/session', { site: SITE, visitorToken: s.token, page: s.page, referrer: s.referrer }, { auth: false }).then(function (r) {
      if (r.status === 200 && r.json && r.json.state === 'open') { s.cfg = r.json.config || s.cfg; opened(r.json); return; }
      if (r.status === 200 && r.json && r.json.state === 'closed') { closedState(); return; }
      // An unknown token is a new visitor.
      save('');
      prechat();
    }).catch(function () { message('Chat is not available right now.'); });
  }

  /* ---- before the first message ---- */
  function prechat() {
    var needContact = s.cfg.collect !== 'off' || s.cfg.offline;
    var form = el('form', 'form');
    form.noValidate = false;
    form.appendChild(el('div', 'fine', s.cfg.offline ? 'Nobody is here right now. Leave your details and a message and we will reply by email.' : (s.cfg.greeting || '')));
    var name, email, consent;
    if (needContact) {
      var nl = el('label', null, 'Your name' + (s.cfg.collect === 'required' ? '' : ' (optional)'));
      name = el('input'); name.type = 'text'; name.maxLength = 100; name.autocomplete = 'name';
      name.required = s.cfg.collect === 'required';
      nl.appendChild(name); form.appendChild(nl);
      var emailRequired = s.cfg.collect === 'required' || s.cfg.offline;
      var el2 = el('label', null, 'Your email' + (emailRequired ? '' : ' (optional)'));
      email = el('input'); email.type = 'email'; email.maxLength = 254; email.autocomplete = 'email';
      email.required = emailRequired;
      el2.appendChild(email); form.appendChild(el2);
      var cl = el('label', 'check');
      consent = el('input'); consent.type = 'checkbox';
      cl.appendChild(consent);
      var txt = 'I agree that my details and this chat are stored for ' + s.cfg.retentionDays + ' days after it ends.';
      cl.appendChild(el('span', 'fine', txt));
      form.appendChild(cl);
      if (s.cfg.privacyUrl) {
        var p = el('div', 'fine');
        var a = el('a', null, 'Privacy policy');
        a.href = s.cfg.privacyUrl; a.target = '_blank'; a.rel = 'noopener noreferrer nofollow';
        p.appendChild(a); form.appendChild(p);
      }
    } else {
      form.appendChild(el('div', 'fine', 'This chat is stored for ' + s.cfg.retentionDays + ' days after it ends.'));
    }
    // The honeypot: a person never sees it.
    var hp = el('div', 'hp');
    var hpIn = el('input'); hpIn.type = 'text'; hpIn.name = 'website'; hpIn.tabIndex = -1; hpIn.autocomplete = 'off';
    hpIn.setAttribute('aria-hidden', 'true');
    hp.appendChild(hpIn); form.appendChild(hp);
    var status = el('div', 'fine');
    var go = el('button', 'primary', 'Start chat');
    go.type = 'submit';
    form.appendChild(status); form.appendChild(go);
    if (s.challenge && s.challenge.kind === 'turnstile') { form.appendChild(turnstileBox()); }
    form.addEventListener('submit', function (ev) {
      ev.preventDefault();
      if (needContact && (name.value || email.value) && !consent.checked) { status.textContent = 'Please tick the box to continue.'; return; }
      go.disabled = true;
      status.textContent = 'One moment…';
      s.contact = needContact ? { name: name.value.trim(), email: email.value.trim(), consent: consent.checked } : { name: '', email: '', consent: false };
      solved(function (ch) {
        create(ch, hpIn.value, status, go);
      }, status);
    });
    setBody(form);
    ui.end.hidden = true;
  }

  var turnstileToken = '';
  function turnstileBox() {
    var box = el('div');
    var sc = document.createElement('script');
    sc.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit';
    sc.async = true;
    sc.onload = function () {
      if (window.turnstile) {
        window.turnstile.render(box, { sitekey: s.challenge.siteKey, callback: function (t) { turnstileToken = t; } });
      }
    };
    document.head.appendChild(sc);
    return box;
  }

  /* solved produces the challenge fields the session call needs. */
  function solved(done, status) {
    var c = s.challenge || { kind: 'none' };
    if (c.kind === 'pow') {
      status.textContent = 'Checking your browser…';
      window.HelloPow.solve(c.nonce, c.bits, function (sol) { done({ nonce: c.nonce, solution: sol }); },
        function (f) { status.textContent = 'Checking your browser… ' + Math.round(f * 100) + '%'; });
    } else if (c.kind === 'turnstile') {
      if (!turnstileToken) { status.textContent = 'Please complete the check below.'; done(null); return; }
      done({ turnstile: turnstileToken });
    } else { done({}); }
  }

  function create(ch, honeypot, status, go) {
    if (ch === null) { go.disabled = false; return; }
    call('POST', '/session', {
      site: SITE, challenge: ch, name: s.contact.name, email: s.contact.email, consent: s.contact.consent,
      page: s.page, referrer: s.referrer, website: honeypot
    }, { auth: false }).then(function (r) {
      var j = r.json || {};
      if (r.status === 200 && j.state === 'open') { save(j.visitorToken || ''); s.cfg = j.config || s.cfg; opened(j); return; }
      if (r.status === 200 && j.state === 'hidden') { message('Chat is closed right now.'); return; }
      if (r.status === 200) { closedState(); return; }
      go.disabled = false;
      if (r.status === 429) { status.textContent = 'Too many tries. Please wait a moment.'; return; }
      if (j.error === 'contact') { status.textContent = 'Please check your name and email.'; return; }
      // A challenge that failed or expired: fetch a fresh one.
      status.textContent = 'Please try again.';
      turnstileToken = '';
      call('GET', '/challenge', undefined, { auth: false }).then(function (c) { if (c.json) { s.challenge = c.json.challenge; } });
    }).catch(function () { go.disabled = false; status.textContent = 'Chat is not available right now.'; });
  }

  function closedState() {
    s.closed = true;
    message('This chat is closed.');
  }

  /* ---- the conversation ---- */
  function opened() {
    s.closed = false;
    ui.log = el('div', 'log');
    ui.typing = el('div', 'typing');
    ui.input = el('textarea');
    ui.input.rows = 1;
    ui.input.maxLength = s.cfg.maxChars;
    ui.input.placeholder = 'Type a message';
    ui.send = el('button', null, 'Send');
    ui.send.type = 'button';
    var compose = el('div', 'compose');
    compose.appendChild(ui.input); compose.appendChild(ui.send);
    var wrap = el('div');
    wrap.style.display = 'flex'; wrap.style.flexDirection = 'column'; wrap.style.flex = '1'; wrap.style.minHeight = '0';
    wrap.appendChild(ui.log); wrap.appendChild(ui.typing); wrap.appendChild(compose);
    setBody(wrap);
    ui.end.hidden = false;
    if (s.cfg.greeting) { ui.log.appendChild(el('div', 'note', s.cfg.greeting)); }
    ui.send.addEventListener('click', send);
    ui.input.addEventListener('keydown', function (e) {
      if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send(); }
    });
    ui.input.addEventListener('input', typingPing);
    ui.input.focus();
    history(0, function () { stream(); });
  }

  function addLine(l, cls) {
    if (l.id && s.seen[l.id]) { return null; }
    if (l.id) { s.seen[l.id] = true; if (l.id > s.lastLine) { s.lastLine = l.id; } }
    var n = el('div', 'line ' + (l.from === 'visitor' ? 'visitor' : 'agent') + (cls ? ' ' + cls : ''), l.text);
    ui.log.appendChild(n);
    ui.log.scrollTop = ui.log.scrollHeight;
    return n;
  }

  function history(after, then) {
    call('GET', '/history?after=' + after).then(function (r) {
      if (r.status === 401) { save(''); s.booted = false; prechat(); return; }
      if (r.status === 200 && r.json) {
        if (r.json.state === 'closed') { closedState(); return; }
        (r.json.lines || []).forEach(function (l) { addLine(l); });
      }
      if (then) { then(); }
    }).catch(function () { if (then) { then(); } });
  }

  var pendingNodes = {};
  function send() {
    var text = ui.input.value.trim();
    if (!text || s.closed) { return; }
    var id = randomId();
    var node = el('div', 'line visitor pending', text);
    ui.log.appendChild(node);
    ui.log.scrollTop = ui.log.scrollHeight;
    pendingNodes[id] = node;
    ui.input.value = '';
    post(id, text, node, 0);
  }
  function post(id, text, node, tries) {
    call('POST', '/messages', { text: text, clientMsgId: id }).then(function (r) {
      var j = r.json || {};
      if ((r.status === 201 || r.status === 200) && j.state === 'open') {
        node.className = 'line visitor';
        if (j.id) { s.seen[j.id] = true; if (j.id > s.lastLine) { s.lastLine = j.id; } }
        return;
      }
      if (r.status === 200) { closedState(); return; }
      if (r.status === 401) { node.className = 'line visitor failed'; save(''); return; }
      if (j.error === 'email_required') {
        node.className = 'line visitor failed';
        ui.log.appendChild(el('div', 'note', 'Nobody is here right now. Please start a new chat and leave your email address.'));
        return;
      }
      if (r.status === 429) { node.className = 'line visitor failed'; ui.log.appendChild(el('div', 'note', 'You are sending too fast. Please wait a moment.')); return; }
      if (r.status >= 500 && tries < 2) { setTimeout(function () { post(id, text, node, tries + 1); }, 1500 * (tries + 1)); return; }
      node.className = 'line visitor failed';
    }).catch(function () {
      if (tries < 2) { setTimeout(function () { post(id, text, node, tries + 1); }, 1500 * (tries + 1)); } else { node.className = 'line visitor failed'; }
    });
  }

  function typingPing() {
    var now = Date.now();
    if (now - s.lastTypingSent < 3000 || !ui.input.value) { return; }
    s.lastTypingSent = now;
    call('POST', '/typing', {}).catch(function () { /* best effort */ });
  }

  function endChat() {
    call('POST', '/end', {}).then(function () {
      s.closed = false;
      s.seen = {}; s.lastLine = 0;
      ui.log.appendChild(el('div', 'note', 'You ended the chat. Write again to start a new one.'));
    });
  }

  function sendTranscript() {
    ui.transcript.disabled = true;
    call('POST', '/transcript', {}).then(function (r) {
      ui.log.appendChild(el('div', 'note', r.status === 204 ? 'The transcript was sent to your email address.' :
        (r.json && r.json.error === 'no_email' ? 'You did not give an email address.' : 'The transcript could not be sent.')));
      ui.transcript.disabled = false;
    }).catch(function () { ui.transcript.disabled = false; });
  }

  function markVisible() { /* the agent sees "read" through the inbox; nothing to send */ }

  /* ---- live events: fetch streaming, the token in a header ---- */
  function stream() {
    if (s.streaming || !s.token) { return; }
    s.streaming = true;
    var h = { Authorization: 'Bearer ' + s.token, Accept: 'text/event-stream' };
    if (s.lastEvent) { h['Last-Event-ID'] = s.lastEvent; }
    fetch(BASE + '/events?site=' + encodeURIComponent(SITE), { headers: h, cache: 'no-store', credentials: 'omit' }).then(function (r) {
      if (r.status === 401) { s.streaming = false; save(''); return; }
      if (r.status !== 200 || !r.body) {
        s.streaming = false;
        var wait = Math.max(parseInt(r.headers.get('Retry-After') || '0', 10) * 1000, s.backoff);
        setTimeout(stream, wait);
        return;
      }
      s.backoff = 3000;
      var reader = r.body.getReader();
      var dec = new TextDecoder();
      var buf = '';
      function pump() {
        reader.read().then(function (x) {
          if (x.done) { throw new Error('closed'); }
          buf += dec.decode(x.value, { stream: true });
          var i;
          while ((i = buf.indexOf('\n\n')) >= 0) {
            frame(buf.slice(0, i));
            buf = buf.slice(i + 2);
          }
          pump();
        }).catch(function () {
          s.streaming = false;
          // Catch up on what the dropped stream missed, then reconnect.
          setTimeout(function () { history(s.lastLine, stream); }, s.backoff);
          s.backoff = Math.min(s.backoff * 2, 30000);
        });
      }
      pump();
    }).catch(function () {
      s.streaming = false;
      setTimeout(stream, s.backoff);
      s.backoff = Math.min(s.backoff * 2, 30000);
    });
  }

  function frame(raw) {
    var id = '', type = 'message', data = '';
    raw.split('\n').forEach(function (line) {
      if (line.indexOf('id:') === 0) { id = line.slice(3).trim(); }
      else if (line.indexOf('event:') === 0) { type = line.slice(6).trim(); }
      else if (line.indexOf('data:') === 0) { data += line.slice(5).trim(); }
    });
    if (id) { s.lastEvent = id; }
    var d = {};
    try { d = data ? JSON.parse(data) : {}; } catch (e) { d = {}; }
    if (type === 'message' && d.text != null) {
      var n = addLine(d);
      if (n && !s.open) { s.unread++; renderBadge(); tell(); }
      ui.typing.textContent = '';
    } else if (type === 'typing') {
      ui.typing.textContent = 'Support is typing…';
      clearTimeout(s.typingTimer);
      s.typingTimer = setTimeout(function () { ui.typing.textContent = ''; }, 4000);
    } else if (type === 'read') {
      ui.log.classList.add('read');
    } else if (type === 'closed') {
      ui.log.appendChild(el('div', 'note', 'The conversation was closed. Write again to start a new one.'));
    }
  }

  load();
  build();
  if (s.token) { /* a returning visitor: connect quietly so the badge can count */ call('GET', '/challenge', undefined, { auth: false }).then(function (r) {
    if (r.status === 200 && r.json) {
      s.cfg = r.json.config; s.challenge = r.json.challenge; s.booted = true;
      document.documentElement.style.setProperty('--accent', s.cfg.accent);
      ui.title.textContent = s.cfg.name;
      ui.transcript.hidden = !s.cfg.transcripts;
      placeLauncher();
      tell();
      resume();
    }
  }).catch(function () { /* the launcher stays; opening retries */ }); }
})();
