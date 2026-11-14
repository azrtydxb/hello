import { MESSAGE_TYPE, type CallbackMessage } from "../hubspot/oauth";

/** Hands the authorization code to the iframe that opened this popup. */
function handBack(): string {
  const q = new URLSearchParams(window.location.search);
  const msg: CallbackMessage = {
    type: MESSAGE_TYPE,
    state: q.get("state") ?? "",
    code: q.get("code") ?? undefined,
    error: q.get("error_description") ?? q.get("error") ?? undefined,
  };
  if (!window.opener) return "Close this window and try again from HubSpot.";
  (window.opener as Window).postMessage(msg, window.location.origin);
  window.setTimeout(() => window.close(), 0);
  return "Signed in. You can close this window.";
}

/**
 * The popup's landing page (/integrations/hubspot/callback): it hands the
 * authorization code to the HubSpot iframe that opened it and closes. The
 * code is single use and bound to the iframe's PKCE verifier.
 */
export function HubspotCallback() {
  return <p role="status">{handBack()}</p>;
}
