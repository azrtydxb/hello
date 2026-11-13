'use strict';

const { request } = require('../lib/hello.js');

// digits reduces a number to what can be compared: separators dropped.
const digits = (n) => String(n || '').replace(/[\s().-]/g, '');

// sameNumber compares two numbers as written, or on their last nine digits
// when both have that many (+32… against 0032… or 0…).
function sameNumber(a, b) {
  const x = digits(a);
  const y = digits(b);
  if (!x || !y) return false;
  if (x === y) return true;
  const dx = x.replace(/\D/g, '');
  const dy = y.replace(/\D/g, '');
  return dx.length >= 9 && dy.length >= 9 && dx.slice(-9) === dy.slice(-9);
}

module.exports = {
  key: 'find_call',
  noun: 'Call',
  display: {
    label: 'Find Call',
    description: 'Finds a call detail record by its CDR ID, or the latest calls to or from a number.',
  },
  operation: {
    inputFields: [
      { key: 'cdrId', label: 'CDR ID', helpText: 'The data__cdrId of a Call Completed trigger. Takes precedence over the number.' },
      { key: 'number', label: 'Phone number', helpText: 'Matches the caller or the called number among the latest 200 calls, newest first.' },
    ],
    perform: async (z, bundle) => {
      const { cdrId, number } = bundle.inputData;
      if (cdrId !== undefined && cdrId !== null && String(cdrId).trim() !== '') {
        if (!/^[1-9][0-9]{0,17}$/.test(String(cdrId).trim())) throw new z.errors.Error('The CDR ID must be a whole number.');
        const res = await request(z, bundle.authData, { method: 'GET', url: '/api/v1/cdrs/' + String(cdrId).trim() }, [404]);
        return res.status === 404 ? [] : [res.data];
      }
      if (!number || !String(number).trim()) throw new z.errors.Error('Give a CDR ID or a number to search for.');
      const res = await request(z, bundle.authData, { method: 'GET', url: '/api/v1/cdrs', params: { limit: 200 } });
      const rows = (res.data && res.data.items) || [];
      return rows
        .filter((c) => [c.source, c.destination, c.originalDestination].some((n) => sameNumber(n, number)))
        .sort((a, b) => b.id - a.id);
    },
    sample: {
      id: 1042,
      correlationId: '5b0f6e9c2a1d4f7e',
      source: '201',
      destination: '+3211223344',
      startTime: '2026-10-26T09:13:33.100Z',
      endTime: '2026-10-26T09:15:10.000Z',
      durationMs: 96900,
      finalStatus: 200,
      direction: 'outbound',
    },
    outputFields: [
      { key: 'id', label: 'CDR ID', type: 'integer' },
      { key: 'correlationId', label: 'Call ID' },
      { key: 'source', label: 'From' },
      { key: 'destination', label: 'To' },
      { key: 'startTime', label: 'Start' },
      { key: 'endTime', label: 'End' },
      { key: 'durationMs', label: 'Duration (ms)', type: 'integer' },
      { key: 'finalStatus', label: 'Final SIP status', type: 'integer' },
      { key: 'direction', label: 'Direction' },
    ],
  },
};
