import { dialable } from "./lib/numbers.js";
import { FINISHED, describe } from "./lib/calls.js";
import { rpc } from "./lib/rpc.js";

const $ = (id) => document.getElementById(id);

function showError(msg) {
  $("err").hidden = !msg;
  $("err").textContent = msg ?? "";
}

function render(st) {
  $("who").textContent = st.signedIn
    ? `Signed in to ${new URL(st.server).host}`
    : "Not signed in.";
  $("dial").hidden = !st.signedIn;
  const running = st.call && !FINISHED.has(st.call.state);
  $("call").hidden = !st.call;
  if (st.call) {
    $("callnum").textContent = st.call.destination;
    $("calltext").textContent = describe(st.call);
    $("hangup").hidden = !running;
  }
  $("opts").textContent = st.signedIn ? "Settings" : "Sign in…";
}

async function refresh() {
  render(await rpc({ type: "status" }));
}

$("dial").addEventListener("submit", async (e) => {
  e.preventDefault();
  showError("");
  const n = dialable($("number").value);
  if (!n) return showError("Digits, +, * and # only.");
  try {
    await rpc({ type: "call", number: n });
    $("number").value = "";
    await refresh();
  } catch (err) {
    showError(err.message);
  }
});
$("hangup").addEventListener(
  "click",
  () => void rpc({ type: "hangUp" }).catch((e) => showError(e.message)),
);
$("opts").addEventListener("click", (e) => {
  e.preventDefault();
  void chrome.runtime.openOptionsPage();
});
chrome.runtime.onMessage.addListener((m) => {
  if (m?.type === "call") void refresh();
});
void refresh().catch((e) => showError(e.message));
