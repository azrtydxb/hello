// The page script, registered only on the sites the user allowed. It catches
// clicks on tel: and callto: links, asks "Call <number> from <extension>?" in a
// box the page cannot style or read, and shows the call as it progresses.
// Optionally it turns plain-text numbers into links first.

const send = (msg) => chrome.runtime.sendMessage(msg);
const PHONE = /^(tel|callto):/i;

let ui = null;

function el(tag, props = {}, ...kids) {
  const n = Object.assign(document.createElement(tag), props);
  n.append(...kids);
  return n;
}

const CSS = `
:host{all:initial}
.box{position:fixed;right:16px;bottom:16px;z-index:2147483647;width:300px;box-sizing:border-box;
 font:14px/1.4 system-ui,sans-serif;color:#1b1f24;background:#fff;border:1px solid #c9d1d9;
 border-radius:8px;box-shadow:0 8px 24px rgba(0,0,0,.25);padding:14px}
.t{font-weight:600;margin:0 0 6px}.s{margin:0 0 10px;color:#424a53;min-height:1.4em}
.n{font-family:ui-monospace,monospace}
.r{display:flex;gap:8px;justify-content:flex-end}
button{font:inherit;padding:6px 12px;border-radius:6px;border:1px solid #c9d1d9;background:#f6f8fa;cursor:pointer}
button.p{background:#1a7f37;border-color:#1a7f37;color:#fff}button.d{background:#cf222e;border-color:#cf222e;color:#fff}
`;

function close() {
  ui?.host.remove();
  ui = null;
}

function open() {
  close();
  const host = el("div");
  const root = host.attachShadow({ mode: "closed" });
  const box = el("div", {
    className: "box",
    role: "dialog",
    ariaLabel: "Hello click-to-call",
  });
  root.append(el("style", { textContent: CSS }), box);
  document.documentElement.append(host);
  ui = { host, box, timer: undefined };
  return ui;
}

function render(title, text, buttons) {
  ui.box.replaceChildren(
    el("p", { className: "t" }, title),
    el("p", { className: "s", ariaLive: "polite" }, text),
    el("div", { className: "r" }, ...buttons),
  );
}

const button = (label, cls, onClick) =>
  el("button", {
    type: "button",
    className: cls,
    textContent: label,
    onclick: onClick,
  });

async function confirmCall(number) {
  open();
  const st = await send({ type: "status" }).catch(() => null);
  if (!st?.ok)
    return render("Hello", "Hello is not available.", [
      button("Close", "", close),
    ]);
  if (!st.value.signedIn) {
    return render("Hello", "Sign in to Hello in the extension options first.", [
      button("Close", "", close),
      button("Sign in", "p", () => {
        void send({ type: "openOptions" });
        close();
      }),
    ]);
  }
  const from = st.value.extension ? ` from ${st.value.extension}` : "";
  render(
    `Call ${number}${from}?`,
    "Hello rings your phone first and dials when you answer.",
    [
      button("Cancel", "", close),
      button("Call", "p", () => void place(number)),
    ],
  );
}

async function place(number) {
  render(`Calling ${number}`, "Getting a line…", []);
  const r = await send({ type: "call", number }).catch((e) => ({
    ok: false,
    error: String(e),
  }));
  if (!r.ok)
    render("The call could not be placed", r.error, [
      button("Close", "", close),
    ]);
}

function showCall(call, text) {
  if (!ui || !call) return;
  clearTimeout(ui.timer);
  const done = call.state === "ended" || call.state === "failed";
  render(`Call to ${call.destination}`, text, [
    done
      ? button("Close", "", close)
      : button("Hang up", "d", () => void send({ type: "hangUp" })),
  ]);
  if (done) ui.timer = setTimeout(close, 6000);
}

chrome.runtime.onMessage.addListener((msg) => {
  if (msg?.type === "call") showCall(msg.call, msg.text);
});

// A real person's click on a phone link; script-made clicks are ignored.
document.addEventListener(
  "click",
  (e) => {
    if (!e.isTrusted || e.defaultPrevented) return;
    const a = e.target instanceof Element ? e.target.closest("a[href]") : null;
    if (!a || !PHONE.test(a.getAttribute("href") ?? "")) return;
    e.preventDefault();
    e.stopPropagation();
    void import(chrome.runtime.getURL("lib/numbers.js")).then(
      ({ dialable }) => {
        const n = dialable(a.getAttribute("href"));
        if (n) void confirmCall(n);
      },
    );
  },
  true,
);

// ---- optional: link plain-text numbers ----

async function linkify() {
  const [{ findNumbers }, st] = await Promise.all([
    import(chrome.runtime.getURL("lib/numbers.js")),
    send({ type: "settings" }),
  ]);
  if (!st?.ok || !st.value.linkify) return;
  const skip = new Set([
    "A",
    "SCRIPT",
    "STYLE",
    "TEXTAREA",
    "INPUT",
    "SELECT",
    "NOSCRIPT",
    "CODE",
    "PRE",
  ]);
  const walker = document.createTreeWalker(
    document.body,
    NodeFilter.SHOW_TEXT,
    {
      acceptNode: (n) =>
        n.parentElement &&
        !skip.has(n.parentElement.tagName) &&
        !n.parentElement.isContentEditable &&
        !n.parentElement.closest("a")
          ? NodeFilter.FILTER_ACCEPT
          : NodeFilter.FILTER_REJECT,
    },
  );
  const todo = [];
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    if (n.data.length < 200 && findNumbers(n.data).length) todo.push(n);
  }
  for (const node of todo) {
    const found = findNumbers(node.data);
    const frag = document.createDocumentFragment();
    let at = 0;
    for (const f of found) {
      frag.append(node.data.slice(at, f.start));
      frag.append(
        el("a", {
          href: `tel:${f.number}`,
          textContent: f.text,
          title: "Call with Hello",
        }),
      );
      at = f.end;
    }
    frag.append(node.data.slice(at));
    node.replaceWith(frag);
  }
}

if (document.body) void linkify().catch(() => undefined);
