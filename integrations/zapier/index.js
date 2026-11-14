'use strict';
// Hello PBX private integration for Zapier (spec integrations I-52).
// Deploy with the Zapier CLI (see README.md); it is shared by invite link and
// needs no Zapier review.

const { version, dependencies } = require('./package.json');
const { baseURL, request } = require('./lib/hello.js');
const triggers = require('./triggers/index.js');
const place_call = require('./creates/place_call.js');
const find_contact = require('./searches/find_contact.js');
const find_call = require('./searches/find_call.js');

// Every request carries the key as a bearer token, and only to the Hello
// host the user entered: a request to any other origin is refused.
const addAuth = (req, z, bundle) => {
  const base = new URL(baseURL(bundle.authData));
  const target = new URL(req.url, base);
  if (target.origin !== base.origin) {
    throw new Error('Refusing to send the Hello API key to ' + target.origin + ', which is not your Hello server.');
  }
  req.headers = { ...(req.headers || {}), Authorization: 'Bearer ' + bundle.authData.apiKey, Accept: 'application/json' };
  return req;
};

module.exports = {
  version,
  platformVersion: dependencies['zapier-platform-core'],
  authentication: {
    type: 'custom',
    fields: [
      {
        key: 'baseUrl',
        label: 'Hello URL',
        type: 'string',
        required: true,
        helpText: 'The https address of your Hello console, for example https://pbx.example.com. Zapier must be able to reach it from the internet.',
      },
      {
        key: 'apiKey',
        label: 'API token',
        type: 'password',
        required: true,
        helpText: 'A personal API token (hello_pat_...) or a service-account token. Triggers need an administrator token with the admin, secrets, events and read scopes; Place Call needs calls.',
      },
    ],
    test: async (z, bundle) => (await request(z, bundle.authData, { method: 'GET', url: '/api/v1/auth/me' })).data,
    connectionLabel: '{{username}}',
  },
  beforeRequest: [addAuth],
  triggers,
  creates: { place_call },
  searches: { find_contact, find_call },
};
