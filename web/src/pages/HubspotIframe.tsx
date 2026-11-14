import { useCallback, useEffect, useRef, useState } from "react";
import { streamEvents } from "../api/events";
import { Alert, Button } from "../design/azrty/components";
import { CallController, type CallingSdk } from "../hubspot/controller";
import {
  authorizeUrl,
  exchangeCode,
  loadTokens,
  MESSAGE_TYPE,
  newPkce,
  saveTokens,
  validToken,
  type CallbackMessage,
  type Pkce,
} from "../hubspot/oauth";
import { newIdempotencyKey } from "../api/originate";

/** What the HubSpot phone widget shows. */
type View =
  | { kind: "starting" }
  | { kind: "signedOut"; error?: string }
  | { kind: "ready"; note: string };

const RETRY_MS = 5000;

/** An authenticated JSON request with the iframe's own token. */
async function call<T>(
  token: string,
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const json = (await res.json().catch(() => ({}))) as {
    error?: { message?: string };
  } & T;
  if (!res.ok) throw new Error(json.error?.message ?? `HTTP ${res.status}`);
  return json;
}

/**
 * The HubSpot calling iframe (/integrations/hubspot/iframe, spec integrations
 * I-50). HubSpot frames this page as its phone widget: its dial request
 * places a Hello click-to-call and the call's progress is told back to
 * HubSpot, which creates the call record. Sign-in is a popup (the console
 * session cookie does not reach a framed page). nginx lets only HubSpot frame
 * this page.
 */
export function HubspotIframe() {
  const origin = window.location.origin;
  const [view, setView] = useState<View>({ kind: "starting" });
  const pkce = useRef<Pkce | null>(null);
  const sdk = useRef<
    | (CallingSdk & {
        initialized(p: { isLoggedIn?: boolean }): void;
        userLoggedIn(): void;
        userLoggedOut(): void;
      })
    | null
  >(null);
  const ctl = useRef<CallController | null>(null);
  const [signedIn, setSignedIn] = useState(() => loadTokens() !== null);

  // The calling extension and the controller.
  useEffect(() => {
    let dead = false;
    void (async () => {
      const mod = await import("@hubspot/calling-extensions-sdk");
      if (dead) return;
      const Ext = mod.default;
      const ext = new Ext({
        debugMode: false,
        eventHandlers: {
          onReady: () => {
            ext.initialized({ isLoggedIn: loadTokens() !== null });
          },
          onDialNumber: (d: { phoneNumber?: string }) => {
            if (d.phoneNumber) void ctl.current?.dial(d.phoneNumber);
          },
          onCreateEngagementSucceeded: (d: { engagementId?: number }) => {
            if (d.engagementId !== undefined)
              ctl.current?.engagementCreated(d.engagementId);
          },
          onEngagementCreated: (d: { engagementId?: number }) => {
            if (d.engagementId !== undefined)
              ctl.current?.engagementCreated(d.engagementId);
          },
        },
      } as unknown as ConstructorParameters<typeof Ext>[0]);
      sdk.current = ext as unknown as NonNullable<typeof sdk.current>;
      ctl.current = new CallController({
        sdk: ext as unknown as CallingSdk,
        originate: async (destination) => {
          const token = await validToken(origin);
          if (!token) {
            setSignedIn(false);
            throw new Error("Sign in to Hello first.");
          }
          const r = await call<{ originateId: string }>(
            token,
            "POST",
            "/api/v1/calls/originate",
            { destination, idempotencyKey: newIdempotencyKey() },
          );
          return r.originateId;
        },
        engagement: async (callId, engagementId) => {
          const token = await validToken(origin);
          if (token) {
            await call(
              token,
              "POST",
              "/api/v1/integrations/hubspot/engagements",
              {
                callId,
                engagementId,
              },
            );
          }
        },
      });
      setView(
        loadTokens()
          ? { kind: "ready", note: "Ready. Dial from HubSpot." }
          : { kind: "signedOut" },
      );
    })();
    return () => {
      dead = true;
    };
  }, [origin]);

  // The call events of the signed-in user's extensions.
  useEffect(() => {
    if (!signedIn) return;
    const ac = new AbortController();
    let last = "";
    void (async () => {
      while (!ac.signal.aborted) {
        const token = await validToken(origin);
        if (!token) {
          setSignedIn(false);
          setView({ kind: "signedOut", error: "Your sign-in has ended." });
          sdk.current?.userLoggedOut();
          return;
        }
        try {
          await streamEvents(
            { bearer: token, lastEventId: last || undefined },
            {
              onEvent: (ev) => {
                last = ev.cursor;
                ctl.current?.event(ev);
              },
            },
            ac.signal,
          );
        } catch {
          // A broken stream resumes from the last event after a pause.
        }
        await new Promise((r) => setTimeout(r, RETRY_MS));
      }
    })();
    return () => ac.abort();
  }, [signedIn, origin]);

  // The popup's answer.
  useEffect(() => {
    const onMessage = (e: MessageEvent) => {
      const m = e.data as Partial<CallbackMessage> | null;
      const p = pkce.current;
      if (e.origin !== origin || !m || m.type !== MESSAGE_TYPE || !p) return;
      if (m.state !== p.state) return;
      pkce.current = null;
      if (!m.code) {
        setView({
          kind: "signedOut",
          error: m.error ?? "Sign-in was refused.",
        });
        return;
      }
      exchangeCode(origin, m.code, p.verifier)
        .then((t) => {
          saveTokens(t);
          setSignedIn(true);
          setView({ kind: "ready", note: "Ready. Dial from HubSpot." });
          sdk.current?.userLoggedIn();
        })
        .catch((err: unknown) =>
          setView({
            kind: "signedOut",
            error: err instanceof Error ? err.message : "Sign-in failed.",
          }),
        );
    };
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [origin]);

  // The PKCE pair is made ahead of the click so the popup opens at once.
  useEffect(() => {
    if (signedIn) return;
    let live = true;
    void newPkce().then((p) => {
      if (live) pkce.current = p;
    });
    return () => {
      live = false;
    };
  }, [signedIn, view.kind]);

  const signIn = useCallback(() => {
    const p = pkce.current;
    if (!p) return;
    const w = window.open(
      authorizeUrl(origin, p),
      "hello-hubspot-signin",
      "popup,width=520,height=720",
    );
    if (!w) {
      setView({
        kind: "signedOut",
        error: "The sign-in window was blocked. Allow pop-ups for Hello.",
      });
    }
  }, [origin]);

  const signOut = () => {
    saveTokens(null);
    setSignedIn(false);
    setView({ kind: "signedOut" });
    sdk.current?.userLoggedOut();
  };

  return (
    <main className="hubspot-frame" aria-label="Hello calling">
      <h1 className="az-h3">Hello</h1>
      {view.kind === "starting" && <p>Starting…</p>}
      {view.kind === "signedOut" && (
        <>
          {view.error && <Alert tone="bad">{view.error}</Alert>}
          <p>Sign in to place calls from HubSpot with your Hello phone.</p>
          <Button onClick={signIn}>Sign in to Hello</Button>
        </>
      )}
      {view.kind === "ready" && (
        <>
          <p>{view.note}</p>
          <p>
            Your own phone rings first; answer it and Hello dials the number.
          </p>
          <Button variant="secondary" onClick={signOut}>
            Sign out
          </Button>
        </>
      )}
    </main>
  );
}
