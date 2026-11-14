// What the user chose on the options page.
import { normalizeHost } from "./hosts.js";

const KEY = "hello.settings";

export const DEFAULTS = Object.freeze({
  /** Sites (host or *.host) where phone links and numbers are handled. */
  hosts: [],
  /** Also turn plain-text numbers on those sites into links. */
  linkify: false,
  /** The extension whose phone rings first; empty is the account's own. */
  extension: "",
  /** Show a desktop notification for an incoming call. */
  notify: true,
});

export function cleanSettings(raw) {
  const s = { ...DEFAULTS, ...(raw ?? {}) };
  const hosts = [
    ...new Set(
      (Array.isArray(s.hosts) ? s.hosts : [])
        .map(normalizeHost)
        .filter(Boolean),
    ),
  ];
  return {
    hosts,
    linkify: s.linkify === true,
    extension: /^[0-9]{0,32}$/.test(String(s.extension))
      ? String(s.extension)
      : "",
    notify: s.notify !== false,
  };
}

export async function loadSettings(storage) {
  return cleanSettings((await storage.get(KEY))[KEY]);
}

export async function saveSettings(storage, s) {
  const c = cleanSettings(s);
  await storage.set({ [KEY]: c });
  return c;
}
