'use strict';

const { request } = require('../lib/hello.js');

module.exports = {
  key: 'place_call',
  noun: 'Call',
  display: {
    label: 'Place Call',
    description: 'Rings an extension first, then dials the number when it answers.',
  },
  operation: {
    inputFields: [
      {
        key: 'destination',
        label: 'Number to call',
        required: true,
        helpText: 'The number or extension to dial, for example +3211223344. Hello applies its normal routing and any click-to-call limits; emergency numbers are refused.',
      },
      {
        key: 'extension',
        label: 'Ring this extension first',
        helpText: 'Leave empty to use the extension linked to the API key owner. A service account or administrator can name any extension it may place calls for.',
      },
      {
        key: 'idempotencyKey',
        label: 'Idempotency key',
        helpText: 'Optional. Repeating a request with the same key does not place a second call.',
      },
    ],
    perform: async (z, bundle) => {
      const i = bundle.inputData;
      const body = { destination: String(i.destination || '').trim() };
      if (i.extension && String(i.extension).trim()) body.extension = String(i.extension).trim();
      if (i.idempotencyKey && String(i.idempotencyKey).trim()) body.idempotencyKey = String(i.idempotencyKey).trim();
      const res = await request(z, bundle.authData, { method: 'POST', url: '/api/v1/calls/originate', body });
      return res.data;
    },
    sample: {
      originateId: 'org_8d3c1f0a7b6e4a29',
      state: 'queued',
      extension: '201',
      destination: '+3211223344',
      createdAt: '2026-10-26T09:14:03.512Z',
      updatedAt: '2026-10-26T09:14:03.512Z',
    },
    outputFields: [
      { key: 'originateId', label: 'Request ID' },
      { key: 'state', label: 'State' },
      { key: 'extension', label: 'Extension' },
      { key: 'destination', label: 'Destination' },
      { key: 'createdAt', label: 'Created at' },
    ],
  },
};
