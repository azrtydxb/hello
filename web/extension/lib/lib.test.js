import { describe, expect, it } from "vitest";
import {
  applyProgress,
  describe as describeCall,
  ringingPop,
} from "./calls.js";
import {
  hostMatches,
  normalizeHost,
  originPatterns,
  serverOrigin,
} from "./hosts.js";
import { dialable, findNumbers, isPhoneHref } from "./numbers.js";
import { challengeOf, randomToken } from "./pkce.js";
import { createSseParser } from "./sse.js";
import { cleanSettings } from "./settings.js";
import { backoff } from "./stream.js";
import { authorizeUrl, parseRedirect } from "./oauth.js";

describe("dialable", () => {
  it("reads what a tel: or callto: link carries and refuses what is not a number", () => {
    expect(dialable("tel:+971-50-123 4567")).toBe("+971501234567");
    expect(dialable("callto:+32 11 11 12 22")).toBe("+3211111222");
    expect(dialable("tel:%2B97150;phone-context=x")).toBe("+97150");
    expect(dialable("tel:+1(555)010-9999?x=1")).toBe("+15550109999");
    expect(dialable("*97")).toBe("*97");
    expect(dialable("sip:evil@host")).toBe("");
    expect(dialable("tel:050abc")).toBe("");
    expect(dialable("tel:%zz")).toBe("");
    expect(dialable("1".repeat(65))).toBe("");
    expect(dialable("")).toBe("");
    expect(isPhoneHref(" TEL:123")).toBe(true);
    expect(isPhoneHref("https://x/tel:1")).toBe(false);
  });
});

describe("findNumbers", () => {
  it("finds phone numbers and leaves dates, prices and ids alone", () => {
    const t = "Call +32 11 11 12 22 or 011 22 33 44 today";
    expect(findNumbers(t).map((f) => f.number)).toEqual([
      "+3211111222",
      "011223344",
    ]);
    const [first] = findNumbers(t);
    expect(t.slice(first.start, first.end)).toBe("+32 11 11 12 22");
    expect(findNumbers("Invoice 2026-11-12 total 1,234.50 order 8841")).toEqual(
      [],
    );
  });
});

describe("hosts", () => {
  it("normalises, matches and builds permission patterns", () => {
    expect(normalizeHost(" HTTPS://CRM.Example.com:8443/x?y ")).toBe(
      "crm.example.com",
    );
    expect(normalizeHost("*.example.com")).toBe("*.example.com");
    expect(normalizeHost("*")).toBe("");
    expect(normalizeHost("a*b.com")).toBe("");
    expect(normalizeHost("not a host")).toBe("");
    expect(hostMatches("*.example.com", "crm.example.com")).toBe(true);
    expect(hostMatches("*.example.com", "example.com")).toBe(true);
    expect(hostMatches("*.example.com", "evilexample.com")).toBe(false);
    expect(hostMatches("example.com", "crm.example.com")).toBe(false);
    expect(originPatterns("crm.example.com")).toEqual([
      "https://crm.example.com/*",
      "http://crm.example.com/*",
    ]);
    expect(originPatterns("")).toEqual([]);
  });
  it("accepts https servers, and http only on loopback", () => {
    expect(serverOrigin("https://hello.example.com/some/path")).toBe(
      "https://hello.example.com",
    );
    expect(serverOrigin("http://localhost:8081")).toBe("http://localhost:8081");
    expect(serverOrigin("http://hello.example.com")).toBe("");
    expect(serverOrigin("javascript:alert(1)")).toBe("");
    expect(serverOrigin("nonsense")).toBe("");
  });
});

describe("settings", () => {
  it("cleans what storage holds", () => {
    expect(cleanSettings(undefined)).toEqual({
      hosts: [],
      linkify: false,
      extension: "",
      notify: true,
    });
    const s = cleanSettings({
      hosts: ["A.com", "a.com", "bad host", "*"],
      linkify: "yes",
      extension: "10x",
      notify: false,
    });
    expect(s).toEqual({
      hosts: ["a.com"],
      linkify: false,
      extension: "",
      notify: false,
    });
  });
});

describe("sse", () => {
  it("parses events split across chunks and drops keepalives", () => {
    const p = createSseParser();
    expect(p.push(': keepalive\n\nid: 1-0\ndata: {"a"')).toEqual([]);
    expect(p.push(":1}\r\n\r\n: ka\n\nid: 2-0\ndata: x\ndata: y\n\n")).toEqual([
      { id: "1-0", data: '{"a":1}' },
      { id: "2-0", data: "x\ny" },
    ]);
  });
  it("backs off to 30 seconds", () => {
    expect([1, 2, 3, 6, 20].map(backoff)).toEqual([1, 2, 4, 30, 30]);
  });
});

describe("calls", () => {
  const call = {
    originateId: "o1",
    state: "queued",
    updatedAt: "2026-11-12T10:00:00Z",
  };
  const ev = (state, at, id = "o1", reason) => ({
    type: "call.originate.progress",
    createdAt: at,
    data: { originateId: id, state, reason },
  });
  it("applies a newer progress event to its own call only", () => {
    expect(
      applyProgress(call, ev("connected", "2026-11-12T10:00:05Z")),
    ).toMatchObject({ state: "connected" });
    expect(applyProgress(call, ev("connected", "2026-11-12T09:59:00Z"))).toBe(
      call,
    );
    expect(
      applyProgress(call, ev("connected", "2026-11-12T10:00:05Z", "other")),
    ).toBe(call);
    expect(
      applyProgress(null, ev("connected", "2026-11-12T10:00:05Z")),
    ).toBeNull();
  });
  it("says what happened and why", () => {
    expect(describeCall({ state: "failed", reason: "agent_dnd" })).toContain(
      "Do Not Disturb",
    );
    expect(describeCall(null)).toBe("");
  });
  it("pops the caller on call.ringing only", () => {
    const pop = ringingPop({
      id: "e1",
      type: "call.ringing",
      data: {
        callId: "c",
        extension: "101",
        from: { name: "Ada", number: "+3211" },
      },
    });
    expect(pop).toMatchObject({ title: "Call from Ada" });
    expect(pop.message).toContain("+3211");
    expect(
      ringingPop({ id: "e2", type: "call.ringing", data: { from: {} } }).title,
    ).toBe("Call from Unknown caller");
    expect(ringingPop({ id: "e3", type: "call.ended", data: {} })).toBeNull();
  });
});

describe("oauth", () => {
  it("makes an S256 challenge as RFC 7636 gives it", async () => {
    // The example of RFC 7636 appendix B.
    expect(
      await challengeOf("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"),
    ).toBe("E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM");
    expect(randomToken(32)).not.toBe(randomToken(32));
  });
  it("asks for the three scopes with PKCE, the client and the API as resource", async () => {
    const u = new URL(
      await authorizeUrl(
        "https://hello.example",
        "https://id.chromiumapp.org/",
        "st",
        "verifier",
      ),
    );
    expect(u.origin + u.pathname).toBe("https://hello.example/oauth/authorize");
    expect(Object.fromEntries(u.searchParams)).toMatchObject({
      response_type: "code",
      client_id: "hello-extension",
      redirect_uri: "https://id.chromiumapp.org/",
      scope: "calls events read",
      state: "st",
      code_challenge_method: "S256",
      resource: "https://hello.example/api/v1",
    });
  });
  it("accepts only the answer to its own request", () => {
    expect(
      parseRedirect(
        "https://id.chromiumapp.org/?code=abc&state=st&iss=x",
        "st",
      ),
    ).toBe("abc");
    expect(() =>
      parseRedirect("https://id.chromiumapp.org/?code=abc&state=other", "st"),
    ).toThrow(/did not match/);
    expect(() =>
      parseRedirect(
        "https://id.chromiumapp.org/?error=access_denied&state=st",
        "st",
      ),
    ).toThrow(/declined/);
    expect(() =>
      parseRedirect("https://id.chromiumapp.org/?state=st", "st"),
    ).toThrow(/no code/);
  });
});
