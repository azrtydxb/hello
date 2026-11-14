import { describe, expect, it } from "vitest";
import { backoffDelay } from "./backoff";
import {
  busy,
  callWaiting,
  endLabel,
  initialPhoneState,
  liveCalls,
  phoneReducer,
  regLabel,
  type PhoneState,
} from "./phoneState";
import type { CallInfo } from "./types";

const base: CallInfo = {
  id: "a",
  sipCallId: "cid-a",
  direction: "in",
  phase: "ringing",
  remote: "200",
  remoteName: "Ann",
  called: "100",
  ringTag: "",
  heldLocal: false,
  heldRemote: false,
  muted: false,
  video: { local: false, remote: false, offered: false },
  transferring: false,
  answeredAt: 0,
};
const call = (p: Partial<CallInfo>): CallInfo => ({ ...base, ...p });
const feed = (s: PhoneState, ...c: CallInfo[]) =>
  c.reduce((st, x) => phoneReducer(st, { type: "call", call: x }), s);

describe("phone state machine", () => {
  it("tracks registration and its words", () => {
    let s = phoneReducer(initialPhoneState, {
      type: "registration",
      state: "connecting",
    });
    expect(regLabel(s)).toBe("Connecting…");
    s = phoneReducer(s, {
      type: "registration",
      state: "failed",
      reason: "Credentials refused",
    });
    expect(regLabel(s)).toBe("Not registered: Credentials refused");
    s = phoneReducer(s, { type: "registration", state: "other-tab" });
    expect(regLabel(s)).toBe("Phone active in another tab");
  });

  it("a ringing call alone takes the focus; an active call keeps it against a call waiting", () => {
    let s = feed(initialPhoneState, call({ id: "a" }));
    expect(s.focus).toBe("a");
    s = feed(s, call({ id: "a", phase: "active", answeredAt: 1 }));
    s = feed(s, call({ id: "b", phase: "ringing" }));
    expect(s.focus).toBe("a");
    expect(callWaiting(s)?.id).toBe("b");
    expect(busy(s)).toBe(true);
  });

  it("an outgoing call takes the focus at once", () => {
    let s = feed(initialPhoneState, call({ id: "a", phase: "active" }));
    s = feed(s, call({ id: "o", direction: "out", phase: "dialing" }));
    expect(s.focus).toBe("o");
  });

  it("an ended line leaves the live set, loses the focus and is dismissed on request", () => {
    let s = feed(
      initialPhoneState,
      call({ id: "a", phase: "active" }),
      call({ id: "b", phase: "active" }),
    );
    s = feed(s, call({ id: "b", phase: "ended", endReason: "hangup" }));
    expect(liveCalls(s).map((c) => c.id)).toEqual(["a"]);
    expect(s.focus).toBe("a");
    s = phoneReducer(s, { type: "dismiss", id: "b" });
    expect(s.calls.map((c) => c.id)).toEqual(["a"]);
  });

  it("focus moves only to a live call", () => {
    let s = feed(
      initialPhoneState,
      call({ id: "a", phase: "active" }),
      call({ id: "b", phase: "active" }),
    );
    s = phoneReducer(s, { type: "focus", id: "b" });
    expect(s.focus).toBe("b");
    s = feed(s, call({ id: "a", phase: "ended" }));
    expect(phoneReducer(s, { type: "focus", id: "a" }).focus).toBe("b");
  });

  it("keeps hold, mute and video state per call", () => {
    const s = feed(
      initialPhoneState,
      call({
        phase: "active",
        heldLocal: true,
        muted: true,
        video: { local: true, remote: true, offered: true },
      }),
    );
    expect(s.calls[0]).toMatchObject({
      heldLocal: true,
      muted: true,
      video: { local: true, remote: true },
    });
  });

  it("records DTMF and voicemail", () => {
    let s = feed(initialPhoneState, call({}));
    s = phoneReducer(s, { type: "dtmf", id: "a", tone: "1" });
    s = phoneReducer(s, { type: "dtmf", id: "a", tone: "#" });
    expect(s.lastDtmf.a).toBe("1#");
    s = phoneReducer(s, { type: "voicemail", waiting: 3 });
    expect(s.voicemail).toBe(3);
  });

  it("says why a call ended", () => {
    expect(
      endLabel(
        call({
          phase: "ended",
          endReason: "answered-elsewhere",
          endText: "Call completed elsewhere",
        }),
      ),
    ).toBe("Call completed elsewhere");
    expect(endLabel(call({ phase: "ended", endReason: "cancelled" }))).toBe(
      "Missed call",
    );
    expect(
      endLabel(call({ phase: "ended", endReason: "media-failed" })),
    ).toMatch(/media/);
    expect(endLabel(call({ phase: "ended", endReason: "no-media" }))).toMatch(
      /microphone/,
    );
  });
});

describe("reconnect backoff", () => {
  it("grows to a cap and stays within the jitter band", () => {
    expect(backoffDelay(0, {}, () => 0)).toBe(1000);
    expect(backoffDelay(3, {}, () => 0)).toBe(8000);
    expect(backoffDelay(10, {}, () => 0)).toBe(30000);
    expect(backoffDelay(10, {}, () => 1)).toBe(15000);
    for (let i = 0; i < 20; i++) {
      const d = backoffDelay(4, { baseMs: 1000, maxMs: 30000, jitter: 0.5 });
      expect(d).toBeGreaterThanOrEqual(8000);
      expect(d).toBeLessThanOrEqual(16000);
    }
  });
});
