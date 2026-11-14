// The softphone's state machine: a pure reducer over engine events. It holds
// the registration state, every call line and which line is in front, and it
// is the part that has the unit tests (S-18, S-19). Nothing here touches the
// DOM, React or a SIP library.
import type { CallInfo, CallStreams, EngineEvent, RegState } from "./types";

export interface PhoneState {
  reg: RegState;
  regReason: string;
  /** Lines in the order they appeared; ended ones stay until dismissed. */
  calls: CallInfo[];
  streams: Record<string, CallStreams>;
  /** The line the controls act on. "" when there is none. */
  focus: string;
  /** DTMF the far end sent, newest last, per call (for tests and the log). */
  lastDtmf: Record<string, string>;
  /** New voicemail messages (RFC 3842 message-summary). */
  voicemail: number;
}

export const initialPhoneState: PhoneState = {
  reg: "off",
  regReason: "",
  calls: [],
  streams: {},
  focus: "",
  lastDtmf: {},
  voicemail: 0,
};

/** How long an ended line stays on screen before the UI dismisses it. */
export const ENDED_LINGER_MS = 4000;

export type PhoneAction =
  | EngineEvent
  | { type: "dismiss"; id: string }
  | { type: "focus"; id: string }
  | { type: "reset" };

/** The live (not ended) calls. */
export function liveCalls(s: PhoneState): CallInfo[] {
  return s.calls.filter((c) => c.phase !== "ended");
}

/** The call waiting: ringing while another line is live. */
export function callWaiting(s: PhoneState): CallInfo | undefined {
  const live = liveCalls(s);
  if (live.length < 2) return undefined;
  return live.find((c) => c.phase === "ringing");
}

/** Whether the phone is in a call or ringing. */
export function busy(s: PhoneState): boolean {
  return liveCalls(s).length > 0;
}

function pickFocus(s: PhoneState, calls: CallInfo[], prefer: string): string {
  const live = calls.filter((c) => c.phase !== "ended");
  if (prefer && live.some((c) => c.id === prefer)) return prefer;
  if (s.focus && live.some((c) => c.id === s.focus)) return s.focus;
  // The active call before a ringing one, so a call waiting does not steal
  // the controls from the call in progress.
  const active = live.find((c) => c.phase === "active");
  return (active ?? live[0])?.id ?? "";
}

export function phoneReducer(s: PhoneState, a: PhoneAction): PhoneState {
  switch (a.type) {
    case "reset":
      return initialPhoneState;
    case "registration":
      return { ...s, reg: a.state, regReason: a.reason ?? "" };
    case "voicemail":
      return { ...s, voicemail: a.waiting };
    case "call": {
      const i = s.calls.findIndex((c) => c.id === a.call.id);
      const calls =
        i < 0
          ? [...s.calls, a.call]
          : s.calls.map((c, j) => (j === i ? a.call : c));
      // A new outgoing line, or the call that just became active, takes the
      // controls; a ringing call does not unless it is the only line.
      const prefer =
        i < 0 && a.call.direction === "out"
          ? a.call.id
          : a.call.phase === "active"
            ? a.call.id
            : "";
      return { ...s, calls, focus: pickFocus(s, calls, prefer) };
    }
    case "streams":
      return { ...s, streams: { ...s.streams, [a.id]: a.streams } };
    case "dtmf":
      return {
        ...s,
        lastDtmf: { ...s.lastDtmf, [a.id]: (s.lastDtmf[a.id] ?? "") + a.tone },
      };
    case "dismiss": {
      const calls = s.calls.filter((c) => c.id !== a.id);
      const streams = { ...s.streams };
      delete streams[a.id];
      const lastDtmf = { ...s.lastDtmf };
      delete lastDtmf[a.id];
      return { ...s, calls, streams, lastDtmf, focus: pickFocus(s, calls, "") };
    }
    case "focus":
      return s.calls.some((c) => c.id === a.id && c.phase !== "ended")
        ? { ...s, focus: a.id }
        : s;
  }
}

/** The words for a registration state, for the dock chip. */
export function regLabel(s: PhoneState): string {
  switch (s.reg) {
    case "off":
      return "Phone off";
    case "connecting":
      return "Connecting…";
    case "registered":
      return "Registered";
    case "unregistered":
      return "Reconnecting…";
    case "failed":
      return s.regReason ? `Not registered: ${s.regReason}` : "Not registered";
    case "other-tab":
      return "Phone active in another tab";
  }
}

/** The words for why a call ended. */
export function endLabel(c: CallInfo): string {
  if (c.endText) return c.endText;
  switch (c.endReason) {
    case "declined":
      return "Declined";
    case "busy":
      return "Busy";
    case "unavailable":
      return "Not reachable";
    case "no-answer":
      return "No answer";
    case "cancelled":
      return "Missed call";
    case "answered-elsewhere":
      return "Answered elsewhere";
    case "media-failed":
      return "The call lost its media connection";
    case "no-media":
      return "No microphone available";
    case "error":
      return "Call failed";
    default:
      return "Call ended";
  }
}
