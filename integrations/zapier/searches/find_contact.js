'use strict';

const { request } = require('../lib/hello.js');

module.exports = {
  key: 'find_contact',
  noun: 'Contact',
  display: {
    label: 'Find Contact by Number',
    description: 'Finds the name Hello has for a phone number.',
  },
  operation: {
    inputFields: [
      { key: 'number', label: 'Phone number', required: true, helpText: 'As dialled or stored, for example +32 11 22 33 44 or 0032112233.' },
    ],
    perform: async (z, bundle) => {
      const res = await request(z, bundle.authData, {
        method: 'GET',
        url: '/api/v1/lookup/number',
        params: { number: bundle.inputData.number },
      });
      const r = res.data || {};
      // none, ambiguous and unresolvable name nobody: no result.
      if (r.result !== 'match') return [];
      return [{ ...r, id: (r.source || 'book') + ':' + r.entryId }];
    },
    sample: {
      id: 'company:7',
      number: '+32 11 22 33 44',
      key: '+3211223344',
      result: 'match',
      via: 'exact',
      source: 'company',
      book: 'Company',
      entryId: 7,
      name: 'ACME NV',
    },
    outputFields: [
      { key: 'name', label: 'Name' },
      { key: 'number', label: 'Number as asked' },
      { key: 'key', label: 'Normalised number' },
      { key: 'source', label: 'Address book kind' },
      { key: 'book', label: 'Address book' },
      { key: 'via', label: 'Matched on' },
    ],
  },
};
