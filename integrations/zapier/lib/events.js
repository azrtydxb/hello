"use strict";
// REST-hook triggers on call events (spec integrations I-52) and messaging
// inbox events (spec messaging-channels M-23). One Hello
// webhook is created per trigger type when a Zap is switched on and deleted
// when it is switched off; Hello's POST /api/v1/webhooks is idempotent on
// (url, events), so a repeated subscription returns the same endpoint.

const { request } = require("./hello.js");

// sample builds the example event Zapier shows while the user maps fields.
function sample(type, extra) {
  return {
    apiVersion: "1",
    id: "evt_3f9c1b7a52d84e0a9b6c0d1e7f2a4c58",
    type,
    createdAt: "2026-10-26T09:14:03.512Z",
    data: {
      callId: "5b0f6e9c2a1d4f7e",
      direction: "inbound",
      extension: "201",
      userId: 7,
      sequence: 4,
      from: { number: "+3211223344", name: "ACME NV" },
      to: { number: "201", name: "Support", extension: "201" },
      trunk: "carrier-1",
      startedAt: "2026-10-26T09:13:33.100Z",
      ...extra,
    },
  };
}

const outputFields = [
  { key: "id", label: "Event ID" },
  { key: "type", label: "Event type" },
  { key: "createdAt", label: "Created at" },
  { key: "data__callId", label: "Call ID" },
  { key: "data__direction", label: "Direction" },
  { key: "data__extension", label: "Extension" },
  { key: "data__from__number", label: "Caller number" },
  { key: "data__from__name", label: "Caller name" },
  { key: "data__to__number", label: "Called number" },
  { key: "data__to__name", label: "Called name" },
  { key: "data__startedAt", label: "Started at" },
  { key: "data__durationMs", label: "Duration (ms)", type: "integer" },
  { key: "data__disposition", label: "Disposition" },
  { key: "data__reason", label: "Reason" },
  { key: "data__cdrId", label: "CDR ID", type: "integer" },
  { key: "data__recordingId", label: "Recording ID", type: "integer" },
];

// inboxSample is the example of an inbox event: ids and states, a hashed
// customer key, and the number and text an endpoint may opt into.
function inboxSample(type, extra) {
  return {
    apiVersion: "1",
    id: "evt_8d2e4a1c07b94f3e9a5d6c2b1e0f7a93",
    type,
    createdAt: "2026-11-09T10:21:44.208Z",
    data: {
      conversationId: 381,
      channelId: 3,
      channelKind: "sms",
      provider: "twilio",
      addressId: 5,
      address: "+3225550100",
      status: "open",
      queueId: 2,
      peerKind: "phone",
      peerKey: "pk_0000000000000000000000000000c0de",
      ...extra,
    },
  };
}

const inboxOutputFields = [
  { key: "id", label: "Event ID" },
  { key: "type", label: "Event type" },
  { key: "createdAt", label: "Created at" },
  { key: "data__conversationId", label: "Conversation ID", type: "integer" },
  { key: "data__messageId", label: "Message ID", type: "integer" },
  { key: "data__channelId", label: "Channel ID", type: "integer" },
  { key: "data__channelKind", label: "Channel kind" },
  { key: "data__provider", label: "Provider" },
  { key: "data__address", label: "Our number or chat site" },
  { key: "data__direction", label: "Direction" },
  { key: "data__status", label: "Status" },
  { key: "data__errorCode", label: "Error code" },
  { key: "data__assigneeUserId", label: "Assigned user ID", type: "integer" },
  { key: "data__queueId", label: "Queue ID", type: "integer" },
  { key: "data__peerKind", label: "Customer kind" },
  { key: "data__peerKey", label: "Customer key (hashed)" },
  { key: "data__peer", label: "Customer's number" },
  { key: "data__body", label: "Message text" },
];

// The inbox options of a webhook, asked for in the Zap (both off by default).
const includePeerField = {
  key: "includePeer",
  label: "Include the customer's number",
  type: "boolean",
  default: "false",
  helpText:
    "Off, the event carries a hashed key that is stable per customer instead of the customer's number.",
};
const includeBodyField = {
  key: "includeMessageBody",
  label: "Include the message text",
  type: "boolean",
  default: "false",
  helpText: "Off by default; Hello records the setting in its audit log.",
};

// on reads a Zapier boolean input, which arrives as a boolean or a string.
const on = (v) => v === true || v === "true" || v === "yes";

// hookTrigger builds the definition of one trigger.
function hookTrigger({
  key,
  type,
  label,
  description,
  extra,
  inbox,
  body: withBody,
}) {
  const inputFields = inbox
    ? withBody
      ? [includePeerField, includeBodyField]
      : [includePeerField]
    : undefined;
  return {
    key,
    noun: inbox ? "Inbox event" : "Call event",
    display: { label, description },
    operation: {
      type: "hook",
      ...(inputFields ? { inputFields } : {}),
      performSubscribe: async (z, bundle) => {
        const body = {
          name: ("Zapier: " + label).slice(0, 64),
          url: bundle.targetUrl,
          events: [type],
        };
        const input = bundle.inputData || {};
        if (inbox && on(input.includePeer)) body.includePeer = true;
        if (inbox && withBody && on(input.includeMessageBody))
          body.includeMessageBody = true;
        const res = await request(z, bundle.authData, {
          method: "POST",
          url: "/api/v1/webhooks",
          body,
        });
        // Only the id is kept: the signing secret is shown once and Zapier's
        // hook URL cannot verify it, so it is not stored.
        return { id: res.data.id };
      },
      performUnsubscribe: async (z, bundle) => {
        await request(
          z,
          bundle.authData,
          {
            method: "DELETE",
            url: "/api/v1/webhooks/" + bundle.subscribeData.id,
          },
          [404],
        );
        return {};
      },
      // The body of the delivery is the event itself.
      perform: async (z, bundle) => {
        const b = bundle.cleanedRequest;
        return Array.isArray(b) ? b : [b];
      },
      // Samples for the editor come from the event history.
      performList: async (z, bundle) => {
        const res = await request(z, bundle.authData, {
          method: "GET",
          url: "/api/v1/events",
          params: { types: type, order: "desc", limit: 3 },
        });
        return ((res.data && res.data.items) || []).map(
          ({ cursor, ...event }) => event,
        );
      },
      sample: inbox ? inboxSample(type, extra) : sample(type, extra),
      outputFields: inbox ? inboxOutputFields : outputFields,
    },
  };
}

module.exports = { hookTrigger, sample, inboxSample };
