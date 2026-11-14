import { originPatterns, serverOrigin } from "./lib/hosts.js";
import { rpc } from "./lib/rpc.js";
import { cleanSettings } from "./lib/settings.js";

const $ = (id) => document.getElementById(id);

function say(text, cls = "") {
  $("msg").textContent = text;
  $("msg").className = cls;
}

async function load() {
  const [st, settings] = await Promise.all([
    rpc({ type: "status" }),
    rpc({ type: "settings" }),
  ]);
  $("redirect").textContent = st.redirectUri;
  $("state").textContent = st.signedIn
    ? `Signed in to ${st.server}.`
    : "Not signed in.";
  $("server").value = st.server || $("server").value;
  $("signin").hidden = st.signedIn;
  $("signout").hidden = !st.signedIn;
  $("hosts").value = settings.hosts.join("\n");
  $("linkify").checked = settings.linkify;
  $("notify").checked = settings.notify;
  const sel = $("extension");
  sel.replaceChildren(new Option("My own extension", ""));
  if (st.signedIn) {
    const me = await rpc({ type: "me" }).catch(() => ({ extensions: [] }));
    for (const x of me.extensions)
      sel.add(new Option(`${x.number} ${x.name}`, x.number));
  }
  if (
    settings.extension &&
    ![...sel.options].some((o) => o.value === settings.extension)
  ) {
    sel.add(new Option(settings.extension, settings.extension));
  }
  sel.value = settings.extension;
}

$("signin").addEventListener("click", async () => {
  say("");
  const origin = serverOrigin($("server").value);
  if (origin === "")
    return say("Enter the https address of your Hello server.", "err");
  // The permission prompt needs this click, so it is asked here, not in the worker.
  const granted = await chrome.permissions.request({
    origins: [`${origin}/*`],
  });
  if (!granted)
    return say("Access to the Hello server was not allowed.", "err");
  try {
    await rpc({ type: "signIn", server: origin });
    say("Signed in.", "ok");
    await load();
  } catch (e) {
    say(e.message, "err");
  }
});

$("signout").addEventListener("click", async () => {
  await rpc({ type: "signOut" });
  say("Signed out.", "ok");
  await load();
});

$("save").addEventListener("click", async () => {
  const settings = cleanSettings({
    hosts: $("hosts").value.split(/\s+/),
    linkify: $("linkify").checked,
    notify: $("notify").checked,
    extension: $("extension").value,
  });
  const wanted = settings.hosts.flatMap(originPatterns);
  if (
    wanted.length &&
    !(await chrome.permissions.request({ origins: wanted }))
  ) {
    return say(
      "Some sites were not allowed; allow them to use Hello there.",
      "err",
    );
  }
  await rpc({ type: "saveSettings", settings });
  say("Saved.", "ok");
});

void load().catch((e) => say(e.message, "err"));
