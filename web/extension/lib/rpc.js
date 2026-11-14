// Calls the background worker; throws its error message.
export async function rpc(msg) {
  const r = await chrome.runtime.sendMessage(msg);
  if (!r?.ok) {
    const e = new Error(r?.error || "Hello did not answer.");
    e.notSignedIn = Boolean(r?.notSignedIn);
    throw e;
  }
  return r.value;
}
