/* Hello chat widget loader. Embed:
 *   <script async src="https://HOST/chat/v1/widget.js" data-site="SITE_KEY"></script>
 * It injects one sandboxed iframe that runs the chat UI on Hello's origin, so
 * this page's scripts and styles can neither read nor break it. Only open,
 * close, size and the unread count cross the boundary, by postMessage with
 * strict origin and source checks both ways. No dependencies. */
(function () {
  'use strict';
  if (window.__helloChat) { return; }
  var tag = document.currentScript;
  if (!tag) {
    var all = document.querySelectorAll('script[data-site]');
    for (var i = 0; i < all.length; i++) {
      if (/\/chat\/v1\/widget\.js/.test(all[i].src)) { tag = all[i]; }
    }
  }
  var site = tag && tag.getAttribute('data-site');
  if (!site || !/^[0-9a-f]{24}$/.test(site) || !tag.src) { return; }
  var base = new URL(tag.src, location.href).origin;
  var frame = document.createElement('iframe');
  var open = false;
  var side = 'right';
  var hidden = false;

  frame.setAttribute('sandbox', 'allow-scripts allow-same-origin allow-forms allow-popups');
  frame.setAttribute('title', 'Chat');
  frame.setAttribute('aria-label', 'Chat');
  frame.src = base + '/chat/v1/frame?site=' + site;

  function place() {
    var s = frame.style;
    s.position = 'fixed';
    s.bottom = '16px';
    s.border = '0';
    s.background = 'transparent';
    s.colorScheme = 'normal';
    s.zIndex = '2147483000';
    s.display = hidden ? 'none' : 'block';
    s.left = side === 'left' ? '16px' : 'auto';
    s.right = side === 'left' ? 'auto' : '16px';
    if (open) {
      s.width = '380px';
      s.maxWidth = 'calc(100vw - 32px)';
      s.height = Math.max(320, Math.min(600, window.innerHeight - 32)) + 'px';
    } else {
      s.width = '72px';
      s.maxWidth = '72px';
      s.height = '72px';
    }
  }

  function send(msg) {
    msg.hello = 'chat';
    if (frame.contentWindow) { frame.contentWindow.postMessage(msg, base); }
  }

  window.addEventListener('message', function (e) {
    if (e.origin !== base || e.source !== frame.contentWindow) { return; }
    var d = e.data;
    if (!d || d.hello !== 'chat' || d.type !== 'state') { return; }
    open = d.open === true;
    hidden = d.hidden === true;
    side = d.position === 'left' ? 'left' : 'right';
    place();
  });
  window.addEventListener('resize', place);
  frame.addEventListener('load', function () {
    send({ type: 'init', page: location.origin + location.pathname, referrer: document.referrer ? new URL(document.referrer).origin : '' });
  });

  window.__helloChat = {
    open: function () { send({ type: 'open' }); },
    close: function () { send({ type: 'close' }); }
  };
  place();
  function mount() { document.body.appendChild(frame); }
  if (document.body) { mount(); } else { document.addEventListener('DOMContentLoaded', mount); }
})();
