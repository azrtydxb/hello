// The service worker: sign-in, the call-event stream, click-to-call and the
// registration of the page script on the sites the user allowed.
import { applyProgress, FINISHED, describe, ringingPop } from "./lib/calls.js";
import { originPatterns, serverOrigin } from "./lib/hosts.js";
import { dialable } from "./lib/numbers.js";
import { createSession, ApiError, NotSignedIn } from "./lib/session.js";
import { loadSettings, saveSettings } from "./lib/settings.js";
import { backoff, readStream } from "./lib/stream.js";

const session = createSession(chrome.storage.local, chrome.identity);
const CALL_KEY = "hello.call";
const CURSOR_KEY = "hello.cursor";
const SCRIPT_ID = "hello-pages";

// ---- the call in progress (survives the worker sleeping) ----

async function getCall() {
  return (await chrome.storage.session.get(CALL_KEY))[CALL_KEY] ?? null;
}

async function setCall(call) {
  if (call) await chrome.storage.session.set({ [CALL_KEY]: call });
  else await chrome.storage.session.remove(CALL_KEY);
  const msg = { type: "call", call, text: describe(call) };
  chrome.runtime.sendMessage(msg).catch(() => undefined);
  if (call?.tabId !== undefined)
    chrome.tabs.sendMessage(call.tabId, msg).catch(() => undefined);
  await badge(call);
}

async function badge(call) {
  const on = call && !FINISHED.has(call.state);
  await chrome.action.setBadgeText({ text: on ? "●" : "" });
  if (on) await chrome.action.setBadgeBackgroundColor({ color: "#1a7f37" });
}

// ---- the event stream ----

let streamAbort = null;
let streamRunning = false;

async function runStream() {
  if (streamRunning) return;
  streamRunning = true;
  let failures = 0;
  try {
    for (;;) {
      const s = await session.read();
      if (!s) return;
      streamAbort = new AbortController();
      try {
        const cursor =
          (await chrome.storage.session.get(CURSOR_KEY))[CURSOR_KEY] ?? "";
        const last = await readStream(session, {
          types: ["call.ringing", "call.originate.progress"],
          lastEventId: cursor,
          signal: streamAbort.signal,
          onOpen: () => {
            failures = 0;
            void catchUp();
          },
          onEvent: (ev, cursor) => void handleEvent(ev, cursor),
        });
        if (last) await chrome.storage.session.set({ [CURSOR_KEY]: last });
      } catch (e) {
        if (e instanceof NotSignedIn || streamAbort.signal.aborted) return;
        failures++;
      }
      await new Promise((r) => setTimeout(r, backoff(failures + 1) * 1000));
    }
  } finally {
    streamRunning = false;
  }
}

function stopStream() {
  streamAbort?.abort();
}

// A state change that happened before the stream opened is read once.
async function catchUp() {
  const call = await getCall();
  if (!call || FINISHED.has(call.state)) return;
  try {
    const s = await session.json(
      `/api/v1/calls/originate/${encodeURIComponent(call.originateId)}`,
    );
    await setCall({
      ...call,
      state: s.state,
      reason: s.reason,
      updatedAt: s.updatedAt,
    });
  } catch {
    // the stream will tell
  }
}

async function handleEvent(ev, cursor) {
  if (cursor) await chrome.storage.session.set({ [CURSOR_KEY]: cursor });
  if (ev.type === "call.originate.progress") {
    const call = await getCall();
    const next = applyProgress(call, ev);
    if (next !== call) await setCall(next);
    return;
  }
  const pop = ringingPop(ev);
  if (!pop || !(await loadSettings(chrome.storage.local)).notify) return;
  await chrome.notifications.create(`ring-${pop.id}`, {
    type: "basic",
    iconUrl: chrome.runtime.getURL("icons/128.png"),
    title: pop.title,
    message: pop.message || "Incoming call",
    priority: 2,
  });
}

// ---- actions ----

async function me() {
  const m = await session.json("/api/v1/auth/me");
  return {
    username: m.username,
    extensions: (m.extensions ?? []).map((x) => ({
      number: x.number,
      name: x.name,
    })),
  };
}

async function placeCall(number, tabId) {
  const destination = dialable(number);
  if (destination === "")
    throw new Error("That is not a number Hello can dial.");
  const settings = await loadSettings(chrome.storage.local);
  const body = { destination, idempotencyKey: crypto.randomUUID() };
  if (settings.extension) body.extension = settings.extension;
  try {
    const s = await session.json("/api/v1/calls/originate", {
      method: "POST",
      body: JSON.stringify(body),
    });
    await setCall({ ...s, tabId });
    return s;
  } catch (e) {
    if (e instanceof ApiError && e.status === 429)
      throw new Error("Too many calls were placed; try again in a moment.", {
        cause: e,
      });
    throw e;
  }
}

async function hangUp() {
  const call = await getCall();
  if (!call || FINISHED.has(call.state)) return;
  const s = await session.json(
    `/api/v1/calls/originate/${encodeURIComponent(call.originateId)}`,
    { method: "DELETE" },
  );
  await setCall({
    ...call,
    state: s.state,
    reason: s.reason,
    updatedAt: s.updatedAt,
  });
}

async function status() {
  const [s, settings, call] = await Promise.all([
    session.read(),
    loadSettings(chrome.storage.local),
    getCall(),
  ]);
  return {
    signedIn: Boolean(s),
    server: s?.server ?? "",
    extension: settings.extension,
    call,
    text: describe(call),
    redirectUri: chrome.identity.getRedirectURL(),
  };
}

const handlers = {
  status,
  async signIn({ server }) {
    const origin = serverOrigin(server);
    if (origin === "")
      throw new Error("Enter the https address of your Hello server.");
    // The options page asks for this permission with a user gesture first.
    if (!(await chrome.permissions.contains({ origins: [`${origin}/*`] }))) {
      throw new Error("Allow access to the Hello server first.");
    }
    await session.signIn(origin);
    void runStream();
    return status();
  },
  async signOut() {
    stopStream();
    await session.signOut();
    await chrome.storage.session.remove([CALL_KEY, CURSOR_KEY]);
    await badge(null);
    return status();
  },
  me,
  settings: () => loadSettings(chrome.storage.local),
  async openOptions() {
    await chrome.runtime.openOptionsPage();
    return {};
  },
  call: ({ number }, sender) => placeCall(number, sender.tab?.id),
  hangUp,
  async saveSettings({ settings }) {
    const s = await saveSettings(chrome.storage.local, settings);
    await syncContentScript();
    return s;
  },
  async syncPages() {
    await syncContentScript();
    return {};
  },
};

chrome.runtime.onMessage.addListener((msg, sender, respond) => {
  // Only this extension's own pages and scripts may drive it.
  if (
    sender.id !== chrome.runtime.id ||
    typeof msg?.type !== "string" ||
    !Object.hasOwn(handlers, msg.type)
  )
    return false;
  Promise.resolve(handlers[msg.type](msg, sender)).then(
    (v) => respond({ ok: true, value: v }),
    (e) =>
      respond({
        ok: false,
        error: e?.message || String(e),
        code: e?.code || "",
        notSignedIn: e instanceof NotSignedIn,
      }),
  );
  return true;
});

// ---- the page script ----

/** Registers the page script on the allowed sites that the browser has granted. */
async function syncContentScript() {
  const settings = await loadSettings(chrome.storage.local);
  const matches = [];
  for (const h of settings.hosts) {
    const origins = originPatterns(h);
    if (origins.length && (await chrome.permissions.contains({ origins })))
      matches.push(...origins);
  }
  const existing = await chrome.scripting.getRegisteredContentScripts({
    ids: [SCRIPT_ID],
  });
  if (existing.length)
    await chrome.scripting.unregisterContentScripts({ ids: [SCRIPT_ID] });
  if (matches.length === 0) return;
  await chrome.scripting.registerContentScripts([
    {
      id: SCRIPT_ID,
      matches,
      js: ["content.js"],
      runAt: "document_idle",
      persistAcrossSessions: true,
    },
  ]);
}

// ---- lifecycle ----

function start() {
  void syncContentScript();
  void runStream();
}

chrome.runtime.onInstalled.addListener(() => {
  void chrome.alarms.create("hello-stream", { periodInMinutes: 0.5 });
  start();
});
chrome.runtime.onStartup.addListener(start);
// MV3 workers sleep; the alarm wakes this one and reopens the stream.
chrome.alarms.onAlarm.addListener((a) => {
  if (a.name === "hello-stream") void runStream();
});
chrome.permissions.onAdded.addListener(() => void syncContentScript());
chrome.permissions.onRemoved.addListener(() => void syncContentScript());
chrome.notifications.onClicked.addListener(
  (id) => void chrome.notifications.clear(id),
);
