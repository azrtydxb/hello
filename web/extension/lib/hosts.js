// The sites the user lets the extension act on, and the permission patterns
// the browser asks for them.

/** Normalises one entry ("crm.example.com", "https://crm.example.com/x", "*.example.com") to a host pattern or "". */
export function normalizeHost(entry) {
  let h = String(entry ?? "")
    .trim()
    .toLowerCase();
  if (h === "") return "";
  h = h.replace(/^[a-z]+:\/\//, "").split(/[/?#]/)[0] ?? "";
  h = h.replace(/:\d+$/, "");
  if (
    !/^(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$/.test(
      h,
    )
  )
    return "";
  return h;
}

/** Whether host (a hostname) is covered by pattern ("example.com" or "*.example.com"). */
export function hostMatches(pattern, host) {
  const p = normalizeHost(pattern);
  const h = String(host ?? "").toLowerCase();
  if (p === "" || h === "") return false;
  if (p.startsWith("*.")) return h === p.slice(2) || h.endsWith(p.slice(1));
  return h === p;
}

/** The match patterns the browser's permission prompt takes for a host. */
export function originPatterns(pattern) {
  const p = normalizeHost(pattern);
  if (p === "") return [];
  return [`https://${p}/*`, `http://${p}/*`];
}

/** The origin pattern of a server URL (https only, or http for a loopback host), or "". */
export function serverOrigin(url) {
  let u;
  try {
    u = new URL(String(url).trim());
  } catch {
    return "";
  }
  const loopback =
    u.hostname === "localhost" ||
    u.hostname === "127.0.0.1" ||
    u.hostname === "[::1]";
  if (u.protocol !== "https:" && !(u.protocol === "http:" && loopback))
    return "";
  return u.origin;
}
