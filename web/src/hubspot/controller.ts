// The HubSpot calling iframe's controller (spec integrations I-50). Desk
// phones are not WebRTC, so the iframe only steers: HubSpot's dial request
// becomes a Hello click-to-call, and the call's events from Hello's stream are
// told back to HubSpot with the Calling Extensions SDK. The engagement HubSpot
// creates for the call is handed to the server, which patches that record
// (duration, status, the recording link) instead of creating a second one.
import type { CallEvent } from "../api/events";

/** The part of the SDK the controller uses (CallingExtensions). */
export interface CallingSdk {
  outgoingCall(p: {
    externalCallId: string;
    toNumber?: string;
    fromNumber?: string;
    createEngagement?: boolean;
  }): void;
  incomingCall(p: {
    externalCallId: string;
    fromNumber: string;
    toNumber: string;
    createEngagement?: boolean;
  }): void;
  callAnswered(p: { externalCallId: string }): void;
  callEnded(p: {
    externalCallId: string;
    engagementId: number;
    callEndStatus?: EndStatus;
  }): void;
  callCompleted(p: {
    externalCallId: string;
    engagementId?: number | string;
  }): void;
  sendError(p: { message: string }): void;
}

export type EndStatus =
  | "COMPLETED"
  | "FAILED"
  | "CANCELED"
  | "BUSY"
  | "NO_ANSWER"
  | "REJECTED"
  | "MISSED";

export interface Deps {
  sdk: CallingSdk;
  /** POST /api/v1/calls/originate; resolves with the originate id. */
  originate(destination: string): Promise<string>;
  /** POST /api/v1/integrations/hubspot/engagements. */
  engagement(callId: string, engagementId: string): Promise<unknown>;
}

interface Active {
  /** The id HubSpot knows the call by: the originate id, or the SIP call id. */
  externalId: string;
  inbound: boolean;
  callId?: string;
  engagementId?: number;
  handshakeSent: boolean;
  answered: boolean;
  /** Set when the call is over; the end is told once the engagement is known. */
  end?: EndStatus;
  endSent: boolean;
  completed: boolean;
  completedSent: boolean;
}

/** How a finished call is described to HubSpot. */
export function endStatus(d: CallEvent["data"]): EndStatus {
  switch (d.disposition) {
    case "busy":
      return "BUSY";
    case "no_answer":
    case "no-answer":
      return "NO_ANSWER";
    case "cancelled":
    case "canceled":
      return "CANCELED";
    case "failed":
      return "FAILED";
    case "answered":
      return "COMPLETED";
  }
  return d.answeredAt ? "COMPLETED" : "NO_ANSWER";
}

const failReason: Record<string, EndStatus> = {
  target_busy: "BUSY",
  agent_busy: "BUSY",
  target_no_answer: "NO_ANSWER",
  agent_no_answer: "NO_ANSWER",
  cancelled: "CANCELED",
};

export class CallController {
  private cur: Active | null = null;

  constructor(private readonly d: Deps) {}

  /** Whether a call is under way (not yet ended). */
  get busy(): boolean {
    return this.cur !== null && this.cur.end === undefined;
  }

  /** HubSpot's onDialNumber: place the call from the user's own phone. */
  async dial(phoneNumber: string): Promise<void> {
    if (this.busy) {
      this.d.sdk.sendError({ message: "A call is already in progress." });
      return;
    }
    let id: string;
    try {
      id = await this.d.originate(phoneNumber);
    } catch (err) {
      this.d.sdk.sendError({
        message: err instanceof Error ? err.message : "The call failed.",
      });
      return;
    }
    this.cur = {
      externalId: id,
      inbound: false,
      handshakeSent: false,
      answered: false,
      endSent: false,
      completed: false,
      completedSent: false,
    };
    this.d.sdk.outgoingCall({
      externalCallId: id,
      toNumber: phoneNumber,
      createEngagement: true,
    });
  }

  /** HubSpot's onCreateEngagementSucceeded. */
  engagementCreated(engagementId: number): void {
    const c = this.cur;
    if (!c || c.engagementId !== undefined) return;
    c.engagementId = engagementId;
    this.flush(c);
  }

  /** One event of Hello's stream. */
  event(ev: CallEvent): void {
    const d = ev.data;
    const c = this.cur;
    if (
      (!c || c.end !== undefined) &&
      ev.type === "call.ringing" &&
      d.direction === "inbound" &&
      !d.originateId
    ) {
      this.cur = {
        externalId: d.callId,
        inbound: true,
        callId: d.callId,
        handshakeSent: false,
        answered: false,
        endSent: false,
        completed: false,
        completedSent: false,
      };
      this.d.sdk.incomingCall({
        externalCallId: d.callId,
        fromNumber: d.from.number ?? "",
        toNumber: d.to.number ?? d.extension ?? "",
        createEngagement: true,
      });
      return;
    }
    if (!c || !this.mine(c, d)) return;
    if (!c.callId && d.callId) c.callId = d.callId;
    switch (ev.type) {
      case "call.originate.progress":
        if (d.state === "failed") {
          c.end ??= failReason[d.reason ?? ""] ?? "FAILED";
          c.completed = true;
        }
        break;
      case "call.answered":
        if (!c.answered) {
          c.answered = true;
          this.d.sdk.callAnswered({ externalCallId: c.externalId });
        }
        break;
      case "call.ended":
      case "call.missed":
        c.end ??= ev.type === "call.missed" ? "MISSED" : endStatus(d);
        break;
      case "call.completed":
        c.end ??= endStatus(d);
        c.completed = true;
        break;
    }
    this.flush(c);
  }

  private mine(c: Active, d: CallEvent["data"]): boolean {
    if (d.originateId) return d.originateId === c.externalId;
    return d.callId === c.callId;
  }

  /** Tell what can be told: the handshake, then the end, then the completion. */
  private flush(c: Active): void {
    if (c.engagementId === undefined) return;
    if (c.callId && !c.handshakeSent) {
      c.handshakeSent = true;
      void this.d
        .engagement(c.callId, String(c.engagementId))
        .catch(() => undefined);
    }
    if (c.end !== undefined && !c.endSent) {
      c.endSent = true;
      this.d.sdk.callEnded({
        externalCallId: c.externalId,
        engagementId: c.engagementId,
        callEndStatus: c.end,
      });
    }
    if (c.endSent && c.completed && !c.completedSent) {
      c.completedSent = true;
      this.d.sdk.callCompleted({
        externalCallId: c.externalId,
        engagementId: c.engagementId,
      });
      this.cur = null;
    }
  }
}
