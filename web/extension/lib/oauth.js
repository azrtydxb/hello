// Sign-in against Hello's OAuth server: authorization code with PKCE through
// chrome.identity.launchWebAuthFlow, client hello-extension (a preregistered
// public client; its redirect URI is https://<extension id>.chromiumapp.org/).
import { challengeOf, randomToken } from "./pkce.js";

export const CLIENT_ID = "hello-extension";
export const SCOPES = "calls events read";

/** The URL the user is sent to. */
export async function authorizeUrl(server, redirectUri, state, verifier) {
  const q = new URLSearchParams({
    response_type: "code",
    client_id: CLIENT_ID,
    redirect_uri: redirectUri,
    scope: SCOPES,
    state,
    code_challenge: await challengeOf(verifier),
    code_challenge_method: "S256",
    resource: `${server}/api/v1`,
  });
  return `${server}/oauth/authorize?${q}`;
}

/** Reads the redirect the flow ended on; throws with a message a person can read. */
export function parseRedirect(redirected, state) {
  const u = new URL(redirected);
  const err = u.searchParams.get("error");
  if (err) {
    throw new Error(
      err === "access_denied"
        ? "Sign-in was declined."
        : `Sign-in failed: ${err}`,
    );
  }
  if (u.searchParams.get("state") !== state)
    throw new Error("Sign-in failed: the answer did not match the request.");
  const code = u.searchParams.get("code");
  if (!code) throw new Error("Sign-in failed: no code came back.");
  return code;
}

async function tokenRequest(server, form) {
  const res = await fetch(`${server}/oauth/token`, {
    method: "POST",
    headers: {
      "Content-Type": "application/x-www-form-urlencoded",
      Accept: "application/json",
    },
    body: new URLSearchParams({
      client_id: CLIENT_ID,
      resource: `${server}/api/v1`,
      ...form,
    }),
  });
  let body = {};
  try {
    body = await res.json();
  } catch {
    // not JSON; handled below
  }
  if (!res.ok || !body.access_token) {
    const e = new Error(
      body.error_description ||
        body.error ||
        `token request failed (${res.status})`,
    );
    e.code = body.error || "";
    e.status = res.status;
    throw e;
  }
  return {
    access: body.access_token,
    refresh: body.refresh_token || form.refresh_token || "",
    expiresAt: Date.now() + Number(body.expires_in || 300) * 1000,
    scope: body.scope || "",
  };
}

/** Runs the whole sign-in and returns the tokens. */
export async function signIn(server, identity) {
  const redirectUri = identity.getRedirectURL();
  const state = randomToken(16);
  const verifier = randomToken(48);
  const redirected = await identity.launchWebAuthFlow({
    url: await authorizeUrl(server, redirectUri, state, verifier),
    interactive: true,
  });
  if (!redirected) throw new Error("Sign-in was cancelled.");
  const code = parseRedirect(redirected, state);
  return tokenRequest(server, {
    grant_type: "authorization_code",
    code,
    redirect_uri: redirectUri,
    code_verifier: verifier,
  });
}

/** Trades the refresh token for new tokens (the refresh token rotates). */
export function refresh(server, refreshToken) {
  return tokenRequest(server, {
    grant_type: "refresh_token",
    refresh_token: refreshToken,
  });
}

/** Revokes a token at the server (RFC 7009); best effort. */
export async function revoke(server, token) {
  if (!token) return;
  await fetch(`${server}/oauth/revoke`, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ client_id: CLIENT_ID, token }),
  });
}
