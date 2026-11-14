/* Hello inside another product's frame (Salesforce Open CTI, Dynamics 365 CIF).
 *
 * The page that loads this file is served by Hello, so every request below is
 * same-origin; the CRM only frames it. The CRM's own session is never used and
 * Hello's cookie is not (a third-party frame does not get it). The user signs
 * in once through a popup (OAuth authorization code with PKCE against Hello's
 * authorization server, a pre-registered public client) and the token stays in
 * this frame's session storage.
 *
 * Plain script, no build step, no inline code: the pages forbid inline script.
 */
(function (root) {
  "use strict";

  var SKEW_MS = 60 * 1000;

  function b64url(bytes) {
    var s = "";
    for (var i = 0; i < bytes.length; i++) s += String.fromCharCode(bytes[i]);
    return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }

  function randomString(n) {
    var a = new Uint8Array(n);
    root.crypto.getRandomValues(a);
    return b64url(a);
  }

  async function challengeOf(verifier) {
    var d = await root.crypto.subtle.digest(
      "SHA-256",
      new TextEncoder().encode(verifier),
    );
    return b64url(new Uint8Array(d));
  }

  /** The number a click-to-call carries, or "" when it is not dialable. */
  function dialable(raw) {
    var n = String(raw == null ? "" : raw).trim();
    n = n.replace(/^tel:/i, "").split(";")[0];
    try {
      n = decodeURIComponent(n);
    } catch (e) {
      return "";
    }
    n = n.replace(/[\s().-]/g, "");
    return /^[0-9+*#]{1,64}$/.test(n) ? n : "";
  }

  /**
   * Whether origin is https and one of the host suffixes ("force.com" allows
   * "a.lightning.force.com" and "force.com" itself, never "evilforce.com").
   */
  function originAllowed(origin, suffixes) {
    var u;
    try {
      u = new URL(origin);
    } catch (e) {
      return false;
    }
    if (u.protocol !== "https:" || u.origin !== origin) return false;
    return suffixes.some(function (s) {
      return u.hostname === s || u.hostname.endsWith("." + s);
    });
  }

  /** Whether an event is a call ringing the user from outside. */
  function isInboundRinging(ev) {
    return (
      !!ev &&
      ev.type === "call.ringing" &&
      !!ev.data &&
      ev.data.direction === "inbound"
    );
  }

  /** The other party's number of a call event. */
  function otherNumber(ev) {
    var d = (ev && ev.data) || {};
    var p = d.direction === "outbound" ? d.to : d.from;
    return (p && p.number) || "";
  }

  /** Parses one server-sent event block into {id, data}; null for a comment. */
  function parseBlock(block) {
    var id = "";
    var data = [];
    block.split("\n").forEach(function (line) {
      if (line.charAt(0) === ":") return;
      var c = line.indexOf(":");
      var f = c < 0 ? line : line.slice(0, c);
      var v = c < 0 ? "" : line.slice(c + 1).replace(/^ /, "");
      if (f === "id") id = v;
      else if (f === "data") data.push(v);
    });
    if (!data.length) return null;
    try {
      return { id: id, event: JSON.parse(data.join("\n")) };
    } catch (e) {
      return null;
    }
  }

  /**
   * client({clientId, callbackPath, scope, storageKey}) returns the Hello
   * client of one embedded app. Options for tests: fetch, open, storage,
   * origin, listen.
   */
  function client(opts) {
    var origin = opts.origin || root.location.origin;
    var doFetch = opts.fetch || root.fetch.bind(root);
    var storage = opts.storage || root.sessionStorage;
    var open = opts.open || root.open.bind(root);
    var listen =
      opts.listen ||
      function (fn) {
        root.addEventListener("message", fn);
        return function () {
          root.removeEventListener("message", fn);
        };
      };
    var key = opts.storageKey || "hello-embed-" + opts.clientId;
    var scope = opts.scope || "calls events";
    var tok = null;
    try {
      tok = JSON.parse(storage.getItem(key) || "null");
    } catch (e) {
      tok = null;
    }

    function save(t) {
      tok = t;
      try {
        if (t) storage.setItem(key, JSON.stringify(t));
        else storage.removeItem(key);
      } catch (e) {
        /* storage may be blocked; the token then lives in memory */
      }
    }

    async function tokenRequest(form) {
      var res = await doFetch(origin + "/oauth/token", {
        method: "POST",
        headers: {
          "Content-Type": "application/x-www-form-urlencoded",
          Accept: "application/json",
        },
        body: new URLSearchParams(form).toString(),
      });
      var body = await res.json().catch(function () {
        return {};
      });
      if (!res.ok || !body.access_token) {
        var e = new Error(
          body.error_description ||
            body.error ||
            "sign-in failed (" + res.status + ")",
        );
        e.status = res.status;
        throw e;
      }
      save({
        access: body.access_token,
        refresh: body.refresh_token || (tok && tok.refresh) || "",
        expiresAt: Date.now() + (body.expires_in || 600) * 1000,
      });
    }

    var c = {
      signedIn: function () {
        return !!tok;
      },
      signOut: function () {
        save(null);
      },

      /** Opens the consent popup; call it from a click or the browser blocks it. */
      signIn: async function () {
        var verifier = randomString(48);
        var state = randomString(16);
        var redirect = origin + opts.callbackPath;
        var q = new URLSearchParams({
          response_type: "code",
          client_id: opts.clientId,
          redirect_uri: redirect,
          scope: scope,
          state: state,
          code_challenge: await challengeOf(verifier),
          code_challenge_method: "S256",
        });
        var win = open(
          origin + "/oauth/authorize?" + q.toString(),
          "hello-signin",
          "width=520,height=720",
        );
        if (!win)
          throw new Error(
            "The sign-in window was blocked. Allow pop-ups for this page and try again.",
          );
        var code = await new Promise(function (resolve, reject) {
          var off = listen(function (m) {
            if (
              m.origin !== origin ||
              !m.data ||
              m.data.type !== "hello-oauth" ||
              m.data.state !== state
            )
              return;
            off();
            if (m.data.error)
              reject(
                new Error(
                  m.data.error === "access_denied"
                    ? "Sign-in was declined."
                    : "Sign-in failed: " + m.data.error,
                ),
              );
            else resolve(m.data.code);
          });
        });
        await tokenRequest({
          grant_type: "authorization_code",
          client_id: opts.clientId,
          code: code,
          redirect_uri: redirect,
          code_verifier: verifier,
        });
      },

      /** A valid access token; refreshes one that is about to expire. */
      token: async function () {
        if (!tok) throw new Error("Not signed in to Hello.");
        if (tok.expiresAt - SKEW_MS > Date.now()) return tok.access;
        if (!tok.refresh) {
          save(null);
          throw new Error("Not signed in to Hello.");
        }
        try {
          await tokenRequest({
            grant_type: "refresh_token",
            client_id: opts.clientId,
            refresh_token: tok.refresh,
          });
        } catch (e) {
          if (e.status === 400 || e.status === 401) save(null);
          throw e;
        }
        return tok.access;
      },

      /** A JSON call to Hello's API as the signed-in user. */
      api: async function (method, path, body, extraHeaders) {
        var send = async function () {
          var h = {
            Accept: "application/json",
            Authorization: "Bearer " + (await c.token()),
          };
          if (body !== undefined) h["Content-Type"] = "application/json";
          Object.keys(extraHeaders || {}).forEach(function (k) {
            h[k] = extraHeaders[k];
          });
          return doFetch(origin + path, {
            method: method,
            headers: h,
            body: body === undefined ? undefined : JSON.stringify(body),
          });
        };
        var res = await send();
        if (res.status === 401 && tok && tok.refresh) {
          tok.expiresAt = 0;
          res = await send();
        }
        var text = await res.text();
        var json = null;
        try {
          json = text ? JSON.parse(text) : null;
        } catch (e) {
          json = null;
        }
        if (!res.ok) {
          var err = new Error(
            (json && json.error && json.error.message) ||
              "Hello answered " + res.status,
          );
          err.status = res.status;
          throw err;
        }
        return json;
      },

      /** Rings the user's own phone, then dials number. */
      originate: function (number) {
        var n = dialable(number);
        if (!n)
          return Promise.reject(
            new Error("That is not a number Hello can dial."),
          );
        return c.api(
          "POST",
          "/api/v1/calls/originate",
          { destination: n },
          { "Idempotency-Key": randomString(12) },
        );
      },

      /**
       * Reads the call event stream until stop() is called, reconnecting with
       * the last event id after a drop. onState gets "live" or "offline".
       */
      stream: function (onEvent, onState) {
        var stopped = false;
        var ctl = null;
        var last = "";
        var delay = 1000;
        (async function loop() {
          while (!stopped) {
            ctl = new AbortController();
            try {
              var h = {
                Accept: "text/event-stream",
                Authorization: "Bearer " + (await c.token()),
              };
              if (last) h["Last-Event-ID"] = last;
              var res = await doFetch(
                origin +
                  "/api/v1/events/stream?types=" +
                  encodeURIComponent("call.*"),
                { headers: h, signal: ctl.signal },
              );
              if (!res.ok || !res.body) throw new Error("stream " + res.status);
              if (onState) onState("live");
              delay = 1000;
              var reader = res.body.getReader();
              var dec = new TextDecoder();
              var buf = "";
              for (;;) {
                var r = await reader.read();
                buf += dec
                  .decode(r.value || new Uint8Array(), { stream: !r.done })
                  .replace(/\r\n?/g, "\n");
                for (
                  var i = buf.indexOf("\n\n");
                  i >= 0;
                  i = buf.indexOf("\n\n")
                ) {
                  var p = parseBlock(buf.slice(0, i));
                  buf = buf.slice(i + 2);
                  if (p) {
                    if (p.id) last = p.id;
                    onEvent(p.event);
                  }
                }
                if (r.done) break;
              }
            } catch (e) {
              if (stopped) return;
            }
            if (onState) onState("offline");
            await new Promise(function (r) {
              setTimeout(r, delay);
            });
            delay = Math.min(delay * 2, 30000);
          }
        })();
        return function stop() {
          stopped = true;
          if (ctl) ctl.abort();
        };
      },
    };
    return c;
  }

  /** Completes the popup: hands the authorization code to the opener. */
  function finishPopup(win) {
    var q = new URLSearchParams(win.location.search);
    var msg = { type: "hello-oauth", state: q.get("state") || "" };
    if (q.get("error")) msg.error = q.get("error");
    else msg.code = q.get("code") || "";
    if (win.opener) win.opener.postMessage(msg, win.location.origin);
    win.close();
    return msg;
  }

  root.HelloEmbed = {
    client: client,
    dialable: dialable,
    originAllowed: originAllowed,
    isInboundRinging: isInboundRinging,
    otherNumber: otherNumber,
    parseBlock: parseBlock,
    finishPopup: finishPopup,
  };
})(typeof globalThis !== "undefined" ? globalThis : window);
