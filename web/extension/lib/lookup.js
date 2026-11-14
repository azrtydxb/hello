// Caller lookup for the incoming-call notification: GET /api/v1/lookup/number
// (spec I-43) with the extension's own token (scope "read", which sign-in
// already asks for). Pure apart from the session it is given, so it is tested
// without a browser.

export const POSITIVE_TTL_MS = 5 * 60 * 1000;
export const NEGATIVE_TTL_MS = 60 * 1000;
// A failure (server down, slow) is remembered only briefly, so a ringing
// phone is not held up by a server that is already struggling.
export const ERROR_TTL_MS = 10 * 1000;
export const TIMEOUT_MS = 2000;
const MAX_ENTRIES = 200;

/** Only an http(s) address is ever offered as a link. */
export function safeLink(u) {
  if (typeof u !== "string") return "";
  try {
    const p = new URL(u);
    return p.protocol === "https:" || p.protocol === "http:" ? p.href : "";
  } catch {
    return "";
  }
}

/**
 * Reduces an API answer to what the notification shows, or null when it names
 * nobody. The CRM contact wins over an address-book match; an ambiguous or
 * unresolvable answer names nobody.
 */
export function contactOf(res) {
  if (!res || typeof res !== "object") return null;
  const crm = res.crm ?? {};
  const book = res.result === "match" ? res : {};
  const name = crm.name || book.name || "";
  const company = crm.company || book.company || "";
  const url = safeLink(crm.url);
  if (!name && !company) return null;
  return {
    name: name || company,
    company: company === name ? "" : company,
    url,
  };
}

/** What a notification needs to name a number: digits only matter for the cache key. */
function keyOf(number) {
  return String(number ?? "")
    .replace(/[^0-9+]/g, "")
    .replace(/^\+/, "");
}

/**
 * Makes the lookup. session is the extension's session (json(path, init));
 * now and timeout are injectable for tests.
 */
export function createLookup(
  session,
  { now = Date.now, timeout = TIMEOUT_MS } = {},
) {
  const cache = new Map();

  function remember(key, contact, ttl) {
    if (cache.size >= MAX_ENTRIES) cache.delete(cache.keys().next().value);
    cache.set(key, { contact, until: now() + ttl });
  }

  return {
    /** The contact for number, or null; never throws. */
    async find(number) {
      const key = keyOf(number);
      if (key === "") return null;
      const hit = cache.get(key);
      if (hit && hit.until > now()) return hit.contact;
      const ctl = new AbortController();
      const timer = setTimeout(() => ctl.abort(), timeout);
      try {
        const res = await session.json(
          `/api/v1/lookup/number?number=${encodeURIComponent(number)}`,
          { signal: ctl.signal },
        );
        const contact = contactOf(res);
        remember(key, contact, contact ? POSITIVE_TTL_MS : NEGATIVE_TTL_MS);
        return contact;
      } catch (e) {
        // 404 (an older server without the route) is a plain no-match; any
        // other failure is also quiet, the event's own name is shown.
        remember(key, null, e?.status === 404 ? NEGATIVE_TTL_MS : ERROR_TTL_MS);
        return null;
      } finally {
        clearTimeout(timer);
      }
    },
    clear: () => cache.clear(),
  };
}
