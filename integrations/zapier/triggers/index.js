"use strict";

const { hookTrigger } = require("../lib/events.js");

const triggers = [
  hookTrigger({
    key: "call_ringing",
    type: "call.ringing",
    label: "Call Ringing",
    description: "Triggers when an extension starts ringing.",
  }),
  hookTrigger({
    key: "call_answered",
    type: "call.answered",
    label: "Call Answered",
    description: "Triggers when an extension answers a call.",
  }),
  hookTrigger({
    key: "call_missed",
    type: "call.missed",
    label: "Call Missed",
    description:
      "Triggers when a call reached an extension and nobody answered.",
    extra: {
      endedAt: "2026-10-26T09:14:03.500Z",
      durationMs: 30400,
      disposition: "no_answer",
      reason: "no_answer",
    },
  }),
  hookTrigger({
    key: "call_completed",
    type: "call.completed",
    label: "Call Completed",
    description:
      "Triggers when the call detail record of a finished call is stored.",
    extra: {
      endedAt: "2026-10-26T09:15:10.000Z",
      durationMs: 96900,
      disposition: "answered",
      cdrId: 1042,
    },
  }),
  hookTrigger({
    key: "call_recording_ready",
    type: "call.recording.ready",
    label: "Call Recording Ready",
    description: "Triggers when the recording of a call has been stored.",
    extra: { durationMs: 96900, recordingId: 311 },
  }),
  hookTrigger({
    key: "conversation_created",
    type: "conversation.created",
    label: "New Inbox Conversation",
    description:
      "Triggers when a customer starts a conversation or an agent starts one in the messaging inbox.",
    inbox: true,
  }),
  hookTrigger({
    key: "conversation_assigned",
    type: "conversation.assigned",
    label: "Inbox Conversation Assigned",
    description:
      "Triggers when an inbox conversation is assigned to an agent or returned to its queue.",
    inbox: true,
    extra: { assigneeUserId: 7 },
  }),
  hookTrigger({
    key: "conversation_resolved",
    type: "conversation.resolved",
    label: "Inbox Conversation Resolved",
    description: "Triggers when an inbox conversation is resolved.",
    inbox: true,
    extra: { status: "resolved", assigneeUserId: 7 },
  }),
  hookTrigger({
    key: "message_received",
    type: "message.received",
    label: "New Inbox Message",
    description:
      "Triggers when a customer sends a text or chat message to the inbox.",
    inbox: true,
    body: true,
    extra: { messageId: 9120, direction: "in", status: "received" },
  }),
  hookTrigger({
    key: "message_sent",
    type: "message.sent",
    label: "Inbox Message Sent",
    description:
      "Triggers when an agent or an automation sends a message from the inbox.",
    inbox: true,
    body: true,
    extra: {
      messageId: 9121,
      direction: "out",
      status: "sent",
      assigneeUserId: 7,
    },
  }),
  hookTrigger({
    key: "message_status",
    type: "message.status",
    label: "Inbox Message Delivery Status",
    description:
      "Triggers when the carrier reports that a sent message was delivered or failed.",
    inbox: true,
    extra: { messageId: 9121, direction: "out", status: "delivered" },
  }),
  hookTrigger({
    key: "message_optout",
    type: "message.optout",
    label: "Inbox Opt-Out",
    description:
      "Triggers when a customer texts STOP (or another opt-out keyword) to a number.",
    inbox: true,
    extra: { messageId: 9122, direction: "in" },
  }),
];

module.exports = Object.fromEntries(triggers.map((t) => [t.key, t]));
