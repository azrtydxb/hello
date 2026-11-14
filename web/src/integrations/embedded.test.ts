// The pages Hello serves inside Salesforce and Dynamics 365 (web/public/
// integrations) are plain scripts with no build step, so they are loaded as
// text and run here with fakes for the CRM's own APIs and for Hello.
import { beforeAll, describe, expect, it, vi } from "vitest";
import embedSrc from "../../public/integrations/embed.js?raw";
import sfSrc from "../../public/integrations/salesforce/softphone.js?raw";
import dynSrc from "../../public/integrations/dynamics/cif.js?raw";

/* eslint-disable @typescript-eslint/no-explicit-any */
const g = globalThis as any;

beforeAll(() => {
  (0, eval)(embedSrc);
  (0, eval)(sfSrc);
  (0, eval)(dynSrc);
});

function b64url(buf: ArrayBuffer): string {
  return btoa(String.fromCharCode(...new Uint8Array(buf)))
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
}

function res(
  status: number,
  body: unknown,
  headers: Record<string, string> = {},
) {
  const text = typeof body === "string" ? body : JSON.stringify(body);
  return new Response(text, { status, headers });
}

describe("shared helpers", () => {
  it("accepts only dialable numbers", () => {
    const d = g.HelloEmbed.dialable;
    expect(d("tel:+32 11 22-33.44;ext=1")).toBe("+3211223344");
    expect(d("(0)11 22")).toBe("011" + "22");
    expect(d("abc")).toBe("");
    expect(d("")).toBe("");
    expect(d("1;rm -rf")).toBe("1");
    expect(d("%E0%A4%A")).toBe("");
    expect(d("1".repeat(65))).toBe("");
  });

  it("allows only the CRM's own https origins", () => {
    const ok = g.HelloEmbed.originAllowed;
    const sf = ["salesforce.com", "force.com"];
    expect(ok("https://acme.lightning.force.com", sf)).toBe(true);
    expect(ok("https://force.com", sf)).toBe(true);
    expect(ok("https://evilforce.com", sf)).toBe(false);
    expect(ok("https://force.com.evil.example", sf)).toBe(false);
    expect(ok("http://acme.force.com", sf)).toBe(false);
    expect(ok("https://acme.force.com/path", sf)).toBe(false);
    expect(ok("javascript:alert(1)", sf)).toBe(false);
    expect(ok("", sf)).toBe(false);
  });

  it("reads server-sent event blocks", () => {
    const p = g.HelloEmbed.parseBlock;
    expect(p(": keepalive")).toBeNull();
    expect(
      p('id: 5-1\nevent: call.ringing\ndata: {"type":"call.ringing"}'),
    ).toEqual({
      id: "5-1",
      event: { type: "call.ringing" },
    });
    expect(p("id: 1\ndata: {not json")).toBeNull();
  });
});

describe("signing in to Hello from a frame", () => {
  function setup(extra: Record<string, unknown> = {}) {
    const store: Record<string, string> = {};
    const storage = {
      getItem: (k: string) => store[k] ?? null,
      setItem: (k: string, v: string) => void (store[k] = v),
      removeItem: (k: string) => void delete store[k],
    };
    let onMessage: ((m: unknown) => void) | null = null;
    const opened: string[] = [];
    const fetchFn = vi.fn();
    const c = g.HelloEmbed.client({
      clientId: "hello-salesforce",
      callbackPath: "/integrations/salesforce/callback",
      origin: "https://hello.example",
      storage,
      fetch: fetchFn,
      open: (u: string) => (opened.push(u), {}),
      listen: (fn: (m: unknown) => void) => {
        onMessage = fn;
        return () => (onMessage = null);
      },
      ...extra,
    });
    return { c, store, fetchFn, opened, send: (m: unknown) => onMessage?.(m) };
  }

  it("runs the authorization code flow with PKCE and keeps the token", async () => {
    const { c, store, fetchFn, opened, send } = setup();
    fetchFn.mockResolvedValueOnce(
      res(200, { access_token: "AT", refresh_token: "RT", expires_in: 3600 }),
    );
    const p = c.signIn();
    await vi.waitFor(() => expect(opened).toHaveLength(1));
    const u = new URL(opened[0]!);
    expect(u.origin + u.pathname).toBe("https://hello.example/oauth/authorize");
    expect(u.searchParams.get("client_id")).toBe("hello-salesforce");
    expect(u.searchParams.get("redirect_uri")).toBe(
      "https://hello.example/integrations/salesforce/callback",
    );
    expect(u.searchParams.get("code_challenge_method")).toBe("S256");
    expect(u.searchParams.get("scope")).toBe("calls events");
    const state = u.searchParams.get("state")!;

    // A message from another origin, or for another state, is ignored.
    send({
      origin: "https://evil.example",
      data: { type: "hello-oauth", state, code: "x" },
    });
    send({
      origin: "https://hello.example",
      data: { type: "hello-oauth", state: "other", code: "x" },
    });
    expect(fetchFn).not.toHaveBeenCalled();
    send({
      origin: "https://hello.example",
      data: { type: "hello-oauth", state, code: "CODE" },
    });
    await p;

    const [url, init] = fetchFn.mock.calls[0]!;
    expect(url).toBe("https://hello.example/oauth/token");
    const form = new URLSearchParams(init.body);
    expect(form.get("grant_type")).toBe("authorization_code");
    expect(form.get("code")).toBe("CODE");
    expect(form.get("client_id")).toBe("hello-salesforce");
    const verifier = form.get("code_verifier")!;
    const digest = await crypto.subtle.digest(
      "SHA-256",
      new TextEncoder().encode(verifier),
    );
    expect(b64url(digest)).toBe(u.searchParams.get("code_challenge"));
    expect(c.signedIn()).toBe(true);
    expect(await c.token()).toBe("AT");
    expect(Object.values(store).join()).toContain("RT");
  });

  it("reports a declined sign-in and a blocked popup", async () => {
    const a = setup();
    const p = a.c.signIn();
    const rejected = expect(p).rejects.toThrow("declined");
    await vi.waitFor(() => expect(a.opened).toHaveLength(1));
    const state = new URL(a.opened[0]!).searchParams.get("state");
    a.send({
      origin: "https://hello.example",
      data: { type: "hello-oauth", state, error: "access_denied" },
    });
    await rejected;
    expect(a.c.signedIn()).toBe(false);

    const b = setup({ open: () => null });
    await expect(b.c.signIn()).rejects.toThrow("blocked");
  });

  it("refreshes an expiring token and signs out when the refresh is refused", async () => {
    const t0 = Date.now();
    const { c, store, fetchFn } = setup();
    store["hello-embed-hello-salesforce"] = JSON.stringify({
      access: "OLD",
      refresh: "RT",
      expiresAt: t0 + 5000,
    });
    const c2 = g.HelloEmbed.client({
      clientId: "hello-salesforce",
      callbackPath: "/x",
      origin: "https://hello.example",
      storage: {
        getItem: (k: string) => store[k] ?? null,
        setItem: (k: string, v: string) => void (store[k] = v),
        removeItem: (k: string) => void delete store[k],
      },
      fetch: fetchFn,
    });
    fetchFn.mockResolvedValueOnce(
      res(200, { access_token: "NEW", expires_in: 3600 }),
    );
    expect(await c2.token()).toBe("NEW");
    expect(
      new URLSearchParams(fetchFn.mock.calls[0]![1].body).get("grant_type"),
    ).toBe("refresh_token");
    expect(JSON.parse(store["hello-embed-hello-salesforce"]!).refresh).toBe(
      "RT",
    );

    store["hello-embed-hello-salesforce"] = JSON.stringify({
      access: "OLD",
      refresh: "RT",
      expiresAt: 1,
    });
    const c3 = g.HelloEmbed.client({
      clientId: "hello-salesforce",
      callbackPath: "/x",
      origin: "https://hello.example",
      storage: {
        getItem: (k: string) => store[k] ?? null,
        setItem: () => undefined,
        removeItem: (k: string) => void delete store[k],
      },
      fetch: fetchFn,
    });
    fetchFn.mockResolvedValueOnce(res(400, { error: "invalid_grant" }));
    await expect(c3.token()).rejects.toThrow();
    expect(c3.signedIn()).toBe(false);
    expect(c.signedIn()).toBe(false);
  });

  it("places a call with the user's token and an idempotency key", async () => {
    const store = {
      "hello-embed-hello-salesforce": JSON.stringify({
        access: "AT",
        refresh: "",
        expiresAt: Date.now() + 3600_000,
      }),
    } as Record<string, string>;
    const fetchFn = vi
      .fn()
      .mockResolvedValue(res(202, { originateId: "o1", state: "queued" }));
    const c = g.HelloEmbed.client({
      clientId: "hello-salesforce",
      callbackPath: "/x",
      origin: "https://hello.example",
      storage: {
        getItem: (k: string) => store[k] ?? null,
        setItem: () => undefined,
        removeItem: () => undefined,
      },
      fetch: fetchFn,
    });
    await c.originate("tel:+32 11 22 33 44");
    const [url, init] = fetchFn.mock.calls[0]!;
    expect(url).toBe("https://hello.example/api/v1/calls/originate");
    expect(init.headers.Authorization).toBe("Bearer AT");
    expect(init.headers["Idempotency-Key"]).toMatch(/^[A-Za-z0-9_-]+$/);
    expect(JSON.parse(init.body)).toEqual({ destination: "+3211223344" });
    await expect(c.originate("not a number")).rejects.toThrow("not a number");
    expect(fetchFn).toHaveBeenCalledTimes(1);
    fetchFn.mockResolvedValueOnce(
      res(429, { error: { message: "Too many calls" } }),
    );
    await expect(c.originate("0111")).rejects.toThrow("Too many calls");
  });

  it("the callback page hands the code to the opener and closes", () => {
    const post = vi.fn();
    const close = vi.fn();
    const msg = g.HelloEmbed.finishPopup({
      location: { search: "?code=C&state=S", origin: "https://hello.example" },
      opener: { postMessage: post },
      close,
    });
    expect(post).toHaveBeenCalledWith(
      { type: "hello-oauth", state: "S", code: "C" },
      "https://hello.example",
    );
    expect(close).toHaveBeenCalled();
    expect(msg.error).toBeUndefined();
    const post2 = vi.fn();
    g.HelloEmbed.finishPopup({
      location: {
        search: "?error=access_denied&state=S",
        origin: "https://hello.example",
      },
      opener: { postMessage: post2 },
      close,
    });
    expect(post2.mock.calls[0]![0]).toEqual({
      type: "hello-oauth",
      state: "S",
      error: "access_denied",
    });
  });

  it("streams call events and resumes after a drop", async () => {
    const enc = new TextEncoder();
    const body = (chunks: string[]) =>
      new ReadableStream({
        start(ctl) {
          chunks.forEach((c) => ctl.enqueue(enc.encode(c)));
          ctl.close();
        },
      });
    const fetchFn = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(
          body([
            ': hi\n\nid: 1-1\ndata: {"type":"call.ringing","data":{"callId":"a"}}\n\nid: 1-',
            '2\ndata: {"type":"call.ended","data":{"callId":"a"}}\n\n',
          ]),
          { status: 200 },
        ),
      )
      .mockImplementation(() => new Promise(() => undefined));
    const c = g.HelloEmbed.client({
      clientId: "hello-salesforce",
      callbackPath: "/x",
      origin: "https://hello.example",
      storage: {
        getItem: () =>
          JSON.stringify({
            access: "AT",
            refresh: "",
            expiresAt: Date.now() + 3600_000,
          }),
        setItem: () => undefined,
        removeItem: () => undefined,
      },
      fetch: fetchFn,
    });
    const got: string[] = [];
    const states: string[] = [];
    vi.useFakeTimers({ toFake: ["setTimeout"] });
    const stop = c.stream(
      (e: { type: string }) => got.push(e.type),
      (s: string) => states.push(s),
    );
    await vi.waitFor(() => expect(got).toEqual(["call.ringing", "call.ended"]));
    expect(fetchFn.mock.calls[0]![0]).toContain(
      "/api/v1/events/stream?types=call.*"
        .replace("*", "%2A")
        .replace("%2A", "*"),
    );
    await vi.advanceTimersByTimeAsync(1100);
    await vi.waitFor(() => expect(fetchFn).toHaveBeenCalledTimes(2));
    expect(fetchFn.mock.calls[1]![1].headers["Last-Event-ID"]).toBe("1-2");
    expect(states).toContain("offline");
    stop();
    vi.useRealTimers();
  });
});

describe("Salesforce softphone", () => {
  function rig(opts: { logMissed?: boolean; search?: unknown } = {}) {
    const logs: any[] = [];
    const cb = (args: any, r: unknown) => args.callback(r);
    const sforce = {
      opencti: {
        CALL_TYPE: { INBOUND: "inbound" },
        setSoftphonePanelVisibility: vi.fn((a) => cb(a, { success: true })),
        searchAndScreenPop: vi.fn((a) =>
          cb(a, opts.search ?? { success: true, returnValue: {} }),
        ),
        saveLog: vi.fn((a) => {
          logs.push(a.value);
          cb(a, {
            success: true,
            returnValue: { recordId: a.value.Id ?? "00T000000000001AAA" },
          });
        }),
        enableClickToDial: vi.fn((a) => cb(a, { success: true })),
        onClickToDial: vi.fn(),
      },
    };
    const hello = {
      api: vi.fn().mockResolvedValue({ adopted: true }),
      originate: vi.fn().mockResolvedValue({}),
    };
    const ui = { setCall: vi.fn(), showError: vi.fn() };
    const ctl = g.HelloSalesforce.controller({
      sforce,
      hello,
      HelloEmbed: g.HelloEmbed,
      logMissed: !!opts.logMissed,
      ui,
    });
    return { ctl, sforce, hello, ui, logs };
  }
  const ev = (type: string, d: Record<string, unknown>) => ({
    type,
    data: {
      callId: "c1",
      direction: "inbound",
      from: { number: "+3211223344" },
      to: { number: "201", extension: "201" },
      ...d,
    },
  });

  it("only loads Open CTI from Salesforce", () => {
    const f = g.HelloSalesforce.sfOrigin;
    expect(
      f(
        "?sfdcIFrameOrigin=https%3A%2F%2Facme.lightning.force.com&x=1",
        g.HelloEmbed,
      ),
    ).toBe("https://acme.lightning.force.com");
    expect(
      f("?sfdcIFrameOrigin=https%3A%2F%2Fevil.example", g.HelloEmbed),
    ).toBe("");
    expect(
      f(
        "?sfdcIFrameOrigin=https%3A%2F%2Fevil.example%2F.force.com",
        g.HelloEmbed,
      ),
    ).toBe("");
    expect(f("", g.HelloEmbed)).toBe("");
  });

  it("pops the caller's record on an incoming call and logs the call as a Task", async () => {
    const r = rig({
      search: {
        success: true,
        returnValue: { "003000000000001AAA": { Name: "Ann" } },
      },
    });
    await r.ctl.handleEvent(ev("call.ringing", {}));
    expect(r.sforce.opencti.searchAndScreenPop).toHaveBeenCalledWith(
      expect.objectContaining({
        searchParams: "+3211223344",
        callType: "inbound",
      }),
    );
    expect(r.sforce.opencti.setSoftphonePanelVisibility).toHaveBeenCalled();

    await r.ctl.handleEvent(
      ev("call.answered", { answeredAt: "2026-11-12T08:00:05Z" }),
    );
    expect(r.logs[0]).toMatchObject({
      entityApiName: "Task",
      Status: "Completed",
      TaskSubtype: "Call",
      CallType: "Inbound",
      CallObject: "c1",
      WhoId: "003000000000001AAA",
    });
    expect(r.logs[0]).not.toHaveProperty("CallDurationInSeconds");
    // The server is told which Task is the call's, so it patches it.
    expect(r.hello.api).toHaveBeenCalledWith(
      "POST",
      "/api/v1/integrations/salesforce/engagements",
      { callId: "c1", recordId: "00T000000000001AAA" },
    );

    await r.ctl.handleEvent(
      ev("call.ended", {
        answeredAt: "2026-11-12T08:00:05Z",
        endedAt: "2026-11-12T08:01:10Z",
        disposition: "answered",
      }),
    );
    expect(r.logs[1]).toMatchObject({
      Id: "00T000000000001AAA",
      CallDisposition: "Connected",
      CallDurationInSeconds: 65,
      WhoId: "003000000000001AAA",
    });
    // A repeated end is not logged twice.
    await r.ctl.handleEvent(
      ev("call.ended", {
        answeredAt: "2026-11-12T08:00:05Z",
        endedAt: "2026-11-12T08:01:10Z",
      }),
    );
    expect(r.logs).toHaveLength(2);
  });

  it("does not guess when several records match", async () => {
    const r = rig({
      search: {
        returnValue: { "003000000000001AAA": {}, "003000000000002AAA": {} },
      },
    });
    await r.ctl.handleEvent(ev("call.ringing", {}));
    await r.ctl.handleEvent(ev("call.answered", {}));
    expect(r.logs[0]).not.toHaveProperty("WhoId");
  });

  it("logs a missed call only when the softphone is the only logger", async () => {
    const off = rig({ logMissed: false });
    await off.ctl.handleEvent(ev("call.ringing", {}));
    await off.ctl.handleEvent(ev("call.missed", { reason: "no_answer" }));
    expect(off.logs).toHaveLength(0);

    const on = rig({ logMissed: true });
    await on.ctl.handleEvent(ev("call.ringing", {}));
    await on.ctl.handleEvent(ev("call.missed", { reason: "no_answer" }));
    expect(on.logs[0]).toMatchObject({
      Subject: "Missed call from +3211223344",
      CallDisposition: "No answer",
      CallDurationInSeconds: 0,
    });
  });

  it("ignores internal calls and the other direction's ringing", async () => {
    const r = rig({ logMissed: true });
    await r.ctl.handleEvent(ev("call.ringing", { direction: "internal" }));
    await r.ctl.handleEvent(ev("call.answered", { direction: "internal" }));
    await r.ctl.handleEvent(ev("call.ringing", { direction: "outbound" }));
    expect(r.sforce.opencti.searchAndScreenPop).not.toHaveBeenCalled();
    expect(r.logs).toHaveLength(0);
  });

  it("places a call from a click on a number and attaches the clicked record", async () => {
    const r = rig();
    r.ctl.enableClickToDial();
    expect(r.sforce.opencti.enableClickToDial).toHaveBeenCalled();
    const listener = r.sforce.opencti.onClickToDial.mock.calls[0]![0].listener;
    listener({
      number: "+32 11 22 33 44",
      recordId: "003000000000009AAA",
      recordName: "Bob",
    });
    await vi.waitFor(() =>
      expect(r.hello.originate).toHaveBeenCalledWith("+3211223344"),
    );
    await r.ctl.handleEvent(
      ev("call.answered", {
        direction: "outbound",
        from: { number: "201" },
        to: { number: "+3211223344" },
      }),
    );
    expect(r.logs[0]).toMatchObject({
      CallType: "Outbound",
      Subject: "Call to Bob",
      WhoId: "003000000000009AAA",
    });
  });

  it("rejects what is not a number and shows Hello's refusal", async () => {
    const r = rig();
    await r.ctl.dial("hello");
    expect(r.hello.originate).not.toHaveBeenCalled();
    expect(r.ui.showError).toHaveBeenCalledWith(
      expect.stringContaining("not a number"),
    );
    r.hello.originate.mockRejectedValueOnce(
      new Error("Your phone is not registered"),
    );
    await r.ctl.dial("0111223344");
    expect(r.ui.showError).toHaveBeenCalledWith("Your phone is not registered");
  });

  it("tells the user when Salesforce does not save the log", async () => {
    const r = rig();
    r.sforce.opencti.saveLog.mockImplementation((a: any) =>
      a.callback({ success: false, errors: [{ message: "no access" }] }),
    );
    await r.ctl.handleEvent(ev("call.answered", {}));
    expect(r.ui.showError).toHaveBeenCalledWith(
      expect.stringContaining("no access"),
    );
    expect(r.hello.api).not.toHaveBeenCalled();
  });
});

describe("Dynamics 365 widget", () => {
  function rig(found: Record<string, unknown>) {
    const cif = {
      searchAndOpenRecords: vi.fn(async (table: string) =>
        JSON.stringify(found[table] ?? {}),
      ),
      setMode: vi.fn().mockResolvedValue(undefined),
      setClickToAct: vi.fn().mockResolvedValue(true),
      addHandler: vi.fn().mockResolvedValue(undefined),
    };
    const hello = { originate: vi.fn().mockResolvedValue({}) };
    const ui = { setCall: vi.fn(), showError: vi.fn() };
    return {
      cif,
      hello,
      ui,
      ctl: g.HelloDynamics.controller({
        cif,
        hello,
        HelloEmbed: g.HelloEmbed,
        ui,
      }),
    };
  }

  it("only loads the CIF library from a Dynamics environment", () => {
    const f = g.HelloDynamics.orgOrigin;
    expect(
      f("?org=https%3A%2F%2Fcontoso.crm4.dynamics.com%2F", g.HelloEmbed),
    ).toBe("https://contoso.crm4.dynamics.com");
    expect(
      f(
        "?org=https%3A%2F%2Fcontoso.crm4.dynamics.com.evil.example",
        g.HelloEmbed,
      ),
    ).toBe("");
    expect(f("", g.HelloEmbed)).toBe("");
  });

  it("queries the spellings a stored number is likely to have, quoted", () => {
    const q = decodeURIComponent(
      g.HelloDynamics.phoneQuery("contact", "+32 (11) 22 33 44"),
    );
    for (const v of [
      "11223344",
      "11 22 33 44",
      "11-223-344",
      "1122.3344",
      "122/3344",
    ])
      expect(q).toContain(`contains(telephone1,'${v}')`);
    expect(q).toContain("contains(telephone2,'11223344')");
    expect(
      decodeURIComponent(g.HelloDynamics.phoneQuery("lead", "0111223344")),
    ).not.toContain("telephone2");
    expect(g.HelloDynamics.phoneQuery("contact", "1'2")).not.toContain("1'2");
    expect(g.HelloDynamics.phoneVariants("+32 11 22 33 44")).toEqual(
      g.HelloDynamics.phoneVariants("011/22.33.44"),
    );
    expect(g.HelloDynamics.phoneVariants("123456")[0]).toBe("123456");
    expect(g.HelloDynamics.phoneQuery("contact", "1'2").length).toBeLessThan(
      6000,
    );
  });

  it("opens the contact, else the lead, of an incoming call", async () => {
    const a = rig({ contact: { c1: {} } });
    await a.ctl.handleEvent({
      type: "call.ringing",
      data: {
        callId: "c1",
        direction: "inbound",
        from: { number: "+3211223344" },
        to: {},
      },
    });
    expect(a.cif.setMode).toHaveBeenCalledWith(1);
    expect(a.cif.searchAndOpenRecords).toHaveBeenCalledTimes(1);
    expect((a.cif.searchAndOpenRecords.mock.calls[0] as unknown[])[2]).toBe(
      false,
    );

    const b = rig({ lead: { l1: {} } });
    expect(await b.ctl.screenPop("+3211223344")).toBe(true);
    expect(b.cif.searchAndOpenRecords.mock.calls.map((c) => c[0])).toEqual([
      "contact",
      "lead",
    ]);

    const c = rig({});
    expect(await c.ctl.screenPop("+3211223344")).toBe(false);
    expect(await c.ctl.screenPop("201")).toBe(false);
    expect(c.cif.searchAndOpenRecords).toHaveBeenCalledTimes(2);
  });

  it("keeps going when a table cannot be read", async () => {
    const r = rig({ lead: { l1: {} } });
    r.cif.searchAndOpenRecords.mockRejectedValueOnce(new Error("privilege"));
    expect(await r.ctl.screenPop("+3211223344")).toBe(true);
  });

  it("dials the number the user clicked in Dynamics", async () => {
    const r = rig({});
    await r.ctl.enableClickToAct();
    expect(r.cif.setClickToAct).toHaveBeenCalledWith(true);
    expect(r.cif.addHandler.mock.calls[0]![0]).toBe("onclicktoact");
    const handler = r.cif.addHandler.mock.calls[0]![1];
    await handler(
      JSON.stringify({
        value: "+32 11 22 33 44",
        entityLogicalName: "contact",
      }),
    );
    expect(r.hello.originate).toHaveBeenCalledWith("+3211223344");
    await handler("0111223344");
    expect(r.hello.originate).toHaveBeenLastCalledWith("0111223344");
    expect(g.HelloDynamics.clickedNumber(JSON.stringify({ value: "5" }))).toBe(
      "5",
    );
  });
});
