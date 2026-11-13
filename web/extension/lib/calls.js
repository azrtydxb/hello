// Call state kept by the background worker and shown by the popup and the
// page toast. Pure functions, so they are tested without a browser.

export const FINISHED = new Set(["ended", "failed"]);

export const STATE_TEXT = {
  queued: "Getting a line…",
  ringing_agent: "Your phone is ringing. Answer it to place the call.",
  agent_answered: "Dialling…",
  dialing_target: "Dialling…",
  connected: "Connected.",
  ended: "The call has ended.",
  failed: "The call could not be placed.",
};

export const REASON_TEXT = {
  no_registration: "Your phone is not registered, so there is nothing to ring.",
  agent_no_answer: "Your phone was not answered.",
  agent_busy: "Your phone is busy.",
  agent_dnd: "Your phone is on Do Not Disturb.",
  target_busy: "The number is busy.",
  target_no_answer: "The number did not answer.",
  route_denied: "This number cannot be dialled from here.",
  rate_limited: "Too many calls were placed; try again in a moment.",
  node_unavailable: "The phone system did not respond; try again.",
  cancelled: "The call was cancelled.",
};

/** One line for a call status: what is happening, and why it failed. */
export function describe(call) {
  if (!call) return "";
  const base = STATE_TEXT[call.state] ?? call.state;
  return call.reason
    ? `${base} ${REASON_TEXT[call.reason] ?? call.reason}`
    : base;
}

/** Applies a call.originate.progress event to the call in progress; a stale or foreign event changes nothing. */
export function applyProgress(call, ev) {
  if (!call || ev.type !== "call.originate.progress") return call;
  const d = ev.data ?? {};
  if (d.originateId !== call.originateId || !d.state) return call;
  if (call.updatedAt && Date.parse(ev.createdAt) < Date.parse(call.updatedAt))
    return call;
  return { ...call, state: d.state, reason: d.reason, updatedAt: ev.createdAt };
}

/** The pop for a call.ringing event: who is calling, or null when the event is not one. */
export function ringingPop(ev) {
  if (ev.type !== "call.ringing") return null;
  const d = ev.data ?? {};
  const from = d.from ?? {};
  const who = from.name || from.number || "Unknown caller";
  return {
    id: ev.id,
    callId: d.callId,
    title: `Call from ${who}`,
    message: [
      from.name ? from.number : "",
      d.extension ? `Ringing extension ${d.extension}` : "",
    ]
      .filter(Boolean)
      .join("\n"),
  };
}
