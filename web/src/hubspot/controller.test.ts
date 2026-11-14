import { describe, expect, it, vi } from "vitest";
import type { CallEvent } from "../api/events";
import { CallController, endStatus, type CallingSdk } from "./controller";

function setup() {
  const sdk = {
    outgoingCall: vi.fn(),
    incomingCall: vi.fn(),
    callAnswered: vi.fn(),
    callEnded: vi.fn(),
    callCompleted: vi.fn(),
    sendError: vi.fn(),
  } satisfies CallingSdk;
  const originate = vi.fn(() => Promise.resolve("orig_1"));
  const engagement = vi.fn(() => Promise.resolve({ adopted: true }));
  return {
    sdk,
    originate,
    engagement,
    c: new CallController({ sdk, originate, engagement }),
  };
}

const ev = (
  type: CallEvent["type"],
  data: Partial<CallEvent["data"]>,
): CallEvent => ({
  apiVersion: "1",
  id: "e",
  type,
  createdAt: "2026-11-13T10:00:00Z",
  data: { callId: "sip-1", from: {}, to: {}, ...data },
});

describe("HubSpot call controller", () => {
  it("places the call, then tells HubSpot the call's progress and hands over the engagement", async () => {
    const { sdk, originate, engagement, c } = setup();
    await c.dial("+971501234567");
    expect(originate).toHaveBeenCalledWith("+971501234567");
    expect(sdk.outgoingCall).toHaveBeenCalledWith({
      externalCallId: "orig_1",
      toNumber: "+971501234567",
      createEngagement: true,
    });
    // An event of another call is ignored.
    c.event(ev("call.answered", { callId: "other", originateId: "orig_9" }));
    expect(sdk.callAnswered).not.toHaveBeenCalled();
    c.event(ev("call.answered", { originateId: "orig_1" }));
    c.event(ev("call.answered", { originateId: "orig_1" }));
    expect(sdk.callAnswered).toHaveBeenCalledTimes(1);
    // The engagement arrives after the call ended: the end waits for it.
    c.event(
      ev("call.ended", { originateId: "orig_1", disposition: "answered" }),
    );
    expect(sdk.callEnded).not.toHaveBeenCalled();
    c.engagementCreated(555);
    expect(engagement).toHaveBeenCalledWith("sip-1", "555");
    expect(sdk.callEnded).toHaveBeenCalledWith({
      externalCallId: "orig_1",
      engagementId: 555,
      callEndStatus: "COMPLETED",
    });
    expect(sdk.callCompleted).not.toHaveBeenCalled();
    c.event(ev("call.completed", { originateId: "orig_1" }));
    expect(sdk.callCompleted).toHaveBeenCalledWith({
      externalCallId: "orig_1",
      engagementId: 555,
    });
    expect(engagement).toHaveBeenCalledTimes(1);
    expect(c.busy).toBe(false);
  });

  it("refuses a second call while one is under way and reports a refused originate", async () => {
    const { sdk, originate, c } = setup();
    await c.dial("100");
    await c.dial("200");
    expect(sdk.sendError).toHaveBeenCalledWith({
      message: "A call is already in progress.",
    });
    expect(originate).toHaveBeenCalledTimes(1);
    const f = setup();
    f.originate.mockRejectedValueOnce(
      new Error("Your phone is not registered."),
    );
    await f.c.dial("100");
    expect(f.sdk.sendError).toHaveBeenCalledWith({
      message: "Your phone is not registered.",
    });
    expect(f.sdk.outgoingCall).not.toHaveBeenCalled();
  });

  it("ends a failed click-to-call with the matching status", async () => {
    const { sdk, c } = setup();
    await c.dial("100");
    c.engagementCreated(7);
    c.event(
      ev("call.originate.progress", {
        originateId: "orig_1",
        state: "failed",
        reason: "target_busy",
      }),
    );
    expect(sdk.callEnded).toHaveBeenCalledWith({
      externalCallId: "orig_1",
      engagementId: 7,
      callEndStatus: "BUSY",
    });
    expect(sdk.callCompleted).toHaveBeenCalled();
  });

  it("pops an inbound call, ignores the agent leg of a click-to-call and a call already shown", () => {
    const { sdk, engagement, c } = setup();
    c.event(
      ev("call.ringing", { direction: "inbound", originateId: "orig_x" }),
    );
    expect(sdk.incomingCall).not.toHaveBeenCalled();
    c.event(
      ev("call.ringing", {
        direction: "inbound",
        from: { number: "+4915" },
        to: { number: "+9714" },
        extension: "101",
      }),
    );
    expect(sdk.incomingCall).toHaveBeenCalledWith({
      externalCallId: "sip-1",
      fromNumber: "+4915",
      toNumber: "+9714",
      createEngagement: true,
    });
    c.event(ev("call.ringing", { direction: "inbound", callId: "sip-2" }));
    expect(sdk.incomingCall).toHaveBeenCalledTimes(1);
    c.engagementCreated(9);
    expect(engagement).toHaveBeenCalledWith("sip-1", "9");
    c.event(ev("call.missed", {}));
    expect(sdk.callEnded).toHaveBeenCalledWith({
      externalCallId: "sip-1",
      engagementId: 9,
      callEndStatus: "MISSED",
    });
  });

  it("describes how a call ended", () => {
    expect(endStatus(ev("call.ended", { disposition: "busy" }).data)).toBe(
      "BUSY",
    );
    expect(endStatus(ev("call.ended", { answeredAt: "x" }).data)).toBe(
      "COMPLETED",
    );
    expect(endStatus(ev("call.ended", {}).data)).toBe("NO_ANSWER");
  });
});
