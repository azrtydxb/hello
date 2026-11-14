'use strict';
// REST-hook triggers on call events (spec integrations I-52). One Hello
// webhook is created per trigger type when a Zap is switched on and deleted
// when it is switched off; Hello's POST /api/v1/webhooks is idempotent on
// (url, events), so a repeated subscription returns the same endpoint.

const { request } = require('./hello.js');

// sample builds the example event Zapier shows while the user maps fields.
function sample(type, extra) {
  return {
    apiVersion: '1',
    id: 'evt_3f9c1b7a52d84e0a9b6c0d1e7f2a4c58',
    type,
    createdAt: '2026-10-26T09:14:03.512Z',
    data: {
      callId: '5b0f6e9c2a1d4f7e',
      direction: 'inbound',
      extension: '201',
      userId: 7,
      sequence: 4,
      from: { number: '+3211223344', name: 'ACME NV' },
      to: { number: '201', name: 'Support', extension: '201' },
      trunk: 'carrier-1',
      startedAt: '2026-10-26T09:13:33.100Z',
      ...extra,
    },
  };
}

const outputFields = [
  { key: 'id', label: 'Event ID' },
  { key: 'type', label: 'Event type' },
  { key: 'createdAt', label: 'Created at' },
  { key: 'data__callId', label: 'Call ID' },
  { key: 'data__direction', label: 'Direction' },
  { key: 'data__extension', label: 'Extension' },
  { key: 'data__from__number', label: 'Caller number' },
  { key: 'data__from__name', label: 'Caller name' },
  { key: 'data__to__number', label: 'Called number' },
  { key: 'data__to__name', label: 'Called name' },
  { key: 'data__startedAt', label: 'Started at' },
  { key: 'data__durationMs', label: 'Duration (ms)', type: 'integer' },
  { key: 'data__disposition', label: 'Disposition' },
  { key: 'data__reason', label: 'Reason' },
  { key: 'data__cdrId', label: 'CDR ID', type: 'integer' },
  { key: 'data__recordingId', label: 'Recording ID', type: 'integer' },
];

// hookTrigger builds the definition of one trigger.
function hookTrigger({ key, type, label, description, extra }) {
  return {
    key,
    noun: 'Call event',
    display: { label, description },
    operation: {
      type: 'hook',
      performSubscribe: async (z, bundle) => {
        const res = await request(z, bundle.authData, {
          method: 'POST',
          url: '/api/v1/webhooks',
          body: { name: ('Zapier: ' + label).slice(0, 64), url: bundle.targetUrl, events: [type] },
        });
        // Only the id is kept: the signing secret is shown once and Zapier's
        // hook URL cannot verify it, so it is not stored.
        return { id: res.data.id };
      },
      performUnsubscribe: async (z, bundle) => {
        await request(z, bundle.authData, { method: 'DELETE', url: '/api/v1/webhooks/' + bundle.subscribeData.id }, [404]);
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
          method: 'GET',
          url: '/api/v1/events',
          params: { types: type, order: 'desc', limit: 3 },
        });
        return ((res.data && res.data.items) || []).map(({ cursor, ...event }) => event);
      },
      sample: sample(type, extra),
      outputFields,
    },
  };
}

module.exports = { hookTrigger, sample };
