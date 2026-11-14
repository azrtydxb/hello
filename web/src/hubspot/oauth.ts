// Sign-in of the HubSpot calling iframe (spec integrations I-50). The iframe
// is framed by HubSpot, where the console's session cookie is not sent, so the
// user signs in through a popup with Hello's own first-party OAuth client
// (public, PKCE, redirect /integrations/hubspot/callback) and the iframe holds
// a calls and events token of its own in its (partitioned) storage.

export const CLIENT_ID = "hello-hubspot";
export const SCOPE = "calls events";
export const CALLBACK_PATH = "/integrations/hubspot/callback";
/** The message the callback page posts to the iframe that opened it. */
export const MESSAGE_TYPE = "hello-hubspot-oauth";

const STORAGE_KEY = "hello.hubspot.tokens";

export interface Tokens {
  accessToken: string;
  refreshToken: string;
  /** Milliseconds since the epoch. */
  expiresAt: number;
}

export interface Pkce {
  verifier: string;
  challenge: string;
  state: string;
}

export interface CallbackMessage {
  type: typeof MESSAGE_TYPE;
  state: string;
  code?: string;
  error?: string;
}

function b64url(bytes: Uint8Array): string {
  let s = "";
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function random(n: number): string {
  return b64url(crypto.getRandomValues(new Uint8Array(n)));
}

/** A fresh verifier, its S256 challenge and a state value. */
export async function newPkce(): Promise<Pkce> {
  const verifier = random(48);
  const digest = await crypto.subtle.digest(
    "SHA-256",
    new TextEncoder().encode(verifier),
  );
  return {
    verifier,
    challenge: b64url(new Uint8Array(digest)),
    state: random(16),
  };
}

export const redirectUri = (origin: string) => origin + CALLBACK_PATH;

/** The authorization URL the popup opens. */
export function authorizeUrl(origin: string, p: Pkce): string {
  const q = new URLSearchParams({
    response_type: "code",
    client_id: CLIENT_ID,
    redirect_uri: redirectUri(origin),
    scope: SCOPE,
    state: p.state,
    code_challenge: p.challenge,
    code_challenge_method: "S256",
  });
  return `${origin}/oauth/authorize?${q.toString()}`;
}

export function loadTokens(): Tokens | null {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return null;
    const t = JSON.parse(raw) as Partial<Tokens>;
    if (
      typeof t.accessToken === "string" &&
      typeof t.refreshToken === "string" &&
      typeof t.expiresAt === "number"
    ) {
      return t as Tokens;
    }
  } catch {
    // Unreadable or storage blocked: signed out.
  }
  return null;
}

export function saveTokens(t: Tokens | null): void {
  try {
    if (t) localStorage.setItem(STORAGE_KEY, JSON.stringify(t));
    else localStorage.removeItem(STORAGE_KEY);
  } catch {
    // Storage blocked: the sign-in lasts until the iframe closes.
  }
}

interface TokenResponse {
  access_token?: string;
  refresh_token?: string;
  expires_in?: number;
  error?: string;
  error_description?: string;
}

async function tokenRequest(
  origin: string,
  form: Record<string, string>,
  previousRefresh = "",
): Promise<Tokens> {
  const res = await fetch(`${origin}/oauth/token`, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ client_id: CLIENT_ID, ...form }).toString(),
  });
  const body = (await res.json().catch(() => ({}))) as TokenResponse;
  if (!res.ok || !body.access_token) {
    throw new Error(body.error_description ?? body.error ?? "Sign-in failed.");
  }
  return {
    accessToken: body.access_token,
    refreshToken: body.refresh_token ?? previousRefresh,
    expiresAt: Date.now() + (body.expires_in ?? 3600) * 1000,
  };
}

/** Trade the popup's authorization code for tokens. */
export function exchangeCode(
  origin: string,
  code: string,
  verifier: string,
): Promise<Tokens> {
  return tokenRequest(origin, {
    grant_type: "authorization_code",
    code,
    code_verifier: verifier,
    redirect_uri: redirectUri(origin),
  });
}

export function refresh(origin: string, t: Tokens): Promise<Tokens> {
  return tokenRequest(
    origin,
    { grant_type: "refresh_token", refresh_token: t.refreshToken },
    t.refreshToken,
  );
}

/**
 * A valid access token, refreshed when it expires within a minute; null when
 * there is none or the refresh was refused (the stored tokens are then
 * dropped, so the user signs in again).
 */
export async function validToken(origin: string): Promise<string | null> {
  const t = loadTokens();
  if (!t) return null;
  if (t.expiresAt - Date.now() > 60_000) return t.accessToken;
  try {
    const n = await refresh(origin, t);
    saveTokens(n);
    return n.accessToken;
  } catch {
    saveTokens(null);
    return null;
  }
}
