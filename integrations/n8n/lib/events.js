"use strict";

/**
 * The events a trigger may subscribe to (docs/integrations.md): the call
 * events and the messaging inbox events.
 */
const EVENT_OPTIONS = [
  { name: "Call Answered", value: "call.answered" },
  { name: "Call Completed (has the CDR id)", value: "call.completed" },
  { name: "Call Ended", value: "call.ended" },
  { name: "Call Missed", value: "call.missed" },
  { name: "Call Recording Ready", value: "call.recording.ready" },
  { name: "Call Ringing", value: "call.ringing" },
  { name: "Inbox Conversation Assigned", value: "conversation.assigned" },
  { name: "Inbox Conversation Created", value: "conversation.created" },
  { name: "Inbox Conversation Resolved", value: "conversation.resolved" },
  { name: "Inbox Message Delivery Status", value: "message.status" },
  { name: "Inbox Message Received", value: "message.received" },
  { name: "Inbox Message Sent", value: "message.sent" },
  { name: "Inbox Opt-Out", value: "message.optout" },
  { name: "Voicemail Received", value: "voicemail.received" },
];

/** The messaging inbox events, which the inbox options apply to. */
const INBOX_TYPES = EVENT_OPTIONS.map((o) => o.value).filter((v) =>
  /^(conversation|message)\./.test(v),
);

const EVENT_TYPES = EVENT_OPTIONS.map((o) => o.value);

/** A comma separated extension list as an array; throws on a bad number. */
function extensions(raw) {
  const out = [];
  for (const x of String(raw || "").split(/[\s,]+/)) {
    if (x === "") continue;
    if (!/^[0-9]{2,10}$/.test(x))
      throw new Error(`"${x}" is not an extension number.`);
    if (!out.includes(x)) out.push(x);
  }
  return out;
}

module.exports = { EVENT_OPTIONS, EVENT_TYPES, INBOX_TYPES, extensions };
