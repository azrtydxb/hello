// The signed-in session: server, tokens and a fetch that carries a fresh
// access token. Refreshes are serialised, because the refresh token rotates and
// a second use of the old one would revoke the grant.
import * as oauth from "./oauth.js";

const KEY = "hello.session";

export function createSession(storage, identity) {
  let refreshing = null;

  async function read() {
    return (await storage.get(KEY))[KEY] ?? null;
  }
  async function write(s) {
    if (s) await storage.set({ [KEY]: s });
    else await storage.remove(KEY);
  }

  async function freshToken(force = false) {
    const s = await read();
    if (!s) throw new NotSignedIn();
    if (!force && s.expiresAt - Date.now() > 30_000) return s;
    refreshing ??= (async () => {
      try {
        const t = await oauth.refresh(s.server, s.refresh);
        const next = { ...s, ...t };
        await write(next);
        return next;
      } catch (e) {
        // The server refused the refresh token: the grant is gone.
        if (e.status === 400 || e.status === 401) await write(null);
        throw e.status === 400 || e.status === 401 ? new NotSignedIn() : e;
      } finally {
        refreshing = null;
      }
    })();
    return refreshing;
  }

  return {
    read,
    /** Signs in to server and stores the tokens. */
    async signIn(server) {
      const t = await oauth.signIn(server, identity);
      await write({ server, ...t });
    },
    /** Revokes the refresh token at the server and forgets the session. */
    async signOut() {
      const s = await read();
      await write(null);
      if (s) await oauth.revoke(s.server, s.refresh).catch(() => undefined);
    },
    /** fetch against the server's API with the bearer token; retries once after a 401. */
    async fetch(path, init = {}) {
      for (let attempt = 0; ; attempt++) {
        const s = await freshToken(attempt > 0);
        const res = await fetch(`${s.server}${path}`, {
          ...init,
          headers: {
            Accept: "application/json",
            ...init.headers,
            Authorization: `Bearer ${s.access}`,
          },
        });
        if (res.status !== 401 || attempt > 0) return res;
      }
    },
    /** Like fetch, but returns parsed JSON and throws an ApiError on a refusal. */
    async json(path, init = {}) {
      const res = await this.fetch(path, {
        ...init,
        headers: init.body
          ? { "Content-Type": "application/json", ...init.headers }
          : init.headers,
      });
      let body = null;
      try {
        body = await res.json();
      } catch {
        // an empty or non-JSON body
      }
      if (!res.ok) throw new ApiError(res.status, body);
      return body;
    },
  };
}

export class NotSignedIn extends Error {
  constructor() {
    super("Not signed in to Hello.");
    this.name = "NotSignedIn";
  }
}

export class ApiError extends Error {
  constructor(status, body) {
    super(body?.error?.message || `Hello answered ${status}`);
    this.name = "ApiError";
    this.status = status;
    this.code = body?.error?.code || "";
    this.body = body;
  }
}
