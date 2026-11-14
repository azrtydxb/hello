'use strict';
// Tests for the Hello Zapier app. They run with `node --test` and need no
// dependencies: the Zapier runtime is replaced by a small fake `z`.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const app = require('../index.js');

const BASE = 'https://pbx.example.com';
const authData = { baseUrl: BASE + '/', apiKey: 'hello_pat_secret' };

// fakeZ records the requests and answers from a queue of responses.
function fakeZ(...responses) {
  const calls = [];
  const z = {
    request: async (opts) => {
      calls.push(opts);
      const r = responses.shift() || { status: 200, data: {} };
      return { status: r.status, data: r.data, headers: {} };
    },
    errors: {
      Error: class extends Error {
        constructor(message, code, status) {
          super(message);
          this.code = code;
          this.status = status;
        }
      },
    },
  };
  return { z, calls };
}

const openapi = JSON.parse(fs.readFileSync(path.join(__dirname, '../../../internal/api/openapi.json'), 'utf8'));

// assertRoute fails unless the route exists in the API description.
function assertRoute(method, url) {
  const p = new URL(url).pathname.replace(/\/\d+$/, '/{id}');
  const key = method.toUpperCase() + ' ' + p;
  const item = openapi.paths[p];
  assert.ok(item && item[method.toLowerCase()], 'route not in openapi.json: ' + key);
}

const EVENTS = {
  call_ringing: 'call.ringing',
  call_answered: 'call.answered',
  call_missed: 'call.missed',
  call_completed: 'call.completed',
  call_recording_ready: 'call.recording.ready',
};

test('the app declares the triggers, action and searches of spec I-52', () => {
  assert.deepEqual(Object.keys(app.triggers).sort(), Object.keys(EVENTS).sort());
  assert.deepEqual(Object.keys(app.creates), ['place_call']);
  assert.deepEqual(Object.keys(app.searches).sort(), ['find_call', 'find_contact']);
});

test('authentication is a custom API key and the test call is GET /auth/me', async () => {
  assert.equal(app.authentication.type, 'custom');
  const keys = app.authentication.fields.map((f) => f.key);
  assert.deepEqual(keys, ['baseUrl', 'apiKey']);
  assert.equal(app.authentication.fields[1].type, 'password');
  const { z, calls } = fakeZ({ data: { username: 'zap' } });
  await app.authentication.test(z, { authData });
  assert.equal(calls[0].method, 'GET');
  assert.equal(calls[0].url, BASE + '/api/v1/auth/me');
  assertRoute('GET', calls[0].url);
});

test('beforeRequest sends the key as a bearer token and never to another host', () => {
  const [before] = app.beforeRequest;
  const out = before({ url: BASE + '/api/v1/events', headers: {} }, null, { authData });
  assert.equal(out.headers.Authorization, 'Bearer hello_pat_secret');
  assert.equal(out.headers.Accept, 'application/json');
  assert.throws(() => before({ url: 'https://evil.example.org/x', headers: {} }, null, { authData }), /Hello/);
});

test('a Hello URL must be https and is normalised', () => {
  const { baseURL } = require('../lib/hello.js');
  assert.equal(baseURL({ baseUrl: ' https://pbx.example.com/// ' }), BASE);
  assert.throws(() => baseURL({ baseUrl: 'http://pbx.example.com' }), /https/);
  assert.throws(() => baseURL({ baseUrl: 'https://u:p@pbx.example.com' }), /https/);
  assert.throws(() => baseURL({ baseUrl: 'pbx.example.com' }), /https/);
  assert.throws(() => baseURL({}), /https/);
});

for (const [key, type] of Object.entries(EVENTS)) {
  test(`trigger ${key} subscribes, lists samples, unsubscribes and parses the hook`, async () => {
    const t = app.triggers[key];
    assert.equal(t.operation.type, 'hook');
    assert.ok(t.display.label && t.display.description);

    // Subscribe: one webhook for this event type, pointing at Zapier.
    let { z, calls } = fakeZ({ status: 201, data: { id: 42, secret: 'whsec_x' } });
    const sub = await t.operation.performSubscribe(z, { authData, targetUrl: 'https://hooks.zapier.com/hooks/standard/1/abc/' });
    assert.equal(calls[0].method, 'POST');
    assert.equal(calls[0].url, BASE + '/api/v1/webhooks');
    assert.deepEqual(calls[0].body, {
      name: calls[0].body.name,
      url: 'https://hooks.zapier.com/hooks/standard/1/abc/',
      events: [type],
    });
    assert.ok(calls[0].body.name.length <= 64 && calls[0].body.name.startsWith('Zapier'));
    assertRoute('POST', calls[0].url);
    // The signing secret is not kept in the subscribe data Zapier stores.
    assert.deepEqual(sub, { id: 42 });

    // Unsubscribe uses the stored id; 404 (already gone) is success.
    ({ z, calls } = fakeZ({ status: 204 }));
    await t.operation.performUnsubscribe(z, { authData, subscribeData: sub });
    assert.equal(calls[0].method, 'DELETE');
    assert.equal(calls[0].url, BASE + '/api/v1/webhooks/42');
    assert.equal(calls[0].skipThrowForStatus, true);
    assertRoute('DELETE', calls[0].url);
    ({ z } = fakeZ({ status: 404 }));
    await t.operation.performUnsubscribe(z, { authData, subscribeData: sub });
    ({ z } = fakeZ({ status: 500 }));
    await assert.rejects(t.operation.performUnsubscribe(z, { authData, subscribeData: sub }));

    // The hook body is the event; Zapier gets it as one item.
    const ev = { id: 'evt_1', type, data: { callId: 'c1' } };
    assert.deepEqual(await t.operation.perform({}, { cleanedRequest: ev }), [ev]);
    assert.deepEqual(await t.operation.perform({}, { cleanedRequest: [ev, ev] }), [ev, ev]);

    // Samples come from the event history, newest first.
    ({ z, calls } = fakeZ({ data: { items: [{ cursor: '1-0', id: 'evt_2', type, data: {} }], nextCursor: '' } }));
    const list = await t.operation.performList(z, { authData });
    assert.equal(calls[0].method, 'GET');
    assert.equal(calls[0].url, BASE + '/api/v1/events');
    assert.deepEqual(calls[0].params, { types: type, order: 'desc', limit: 3 });
    assertRoute('GET', calls[0].url);
    assert.deepEqual(list, [{ id: 'evt_2', type, data: {} }]);

    assert.equal(t.operation.sample.type, type);
    assert.match(t.operation.sample.id, /^evt_/);
    assert.ok(t.operation.outputFields.length > 3);
  });
}

test('Place a call posts to /calls/originate and returns the request state', async () => {
  const c = app.creates.place_call;
  const keys = c.operation.inputFields.map((f) => f.key);
  assert.deepEqual(keys, ['destination', 'extension', 'idempotencyKey']);
  assert.equal(c.operation.inputFields[0].required, true);
  const { z, calls } = fakeZ({ status: 202, data: { originateId: 'o1', state: 'queued' } });
  const out = await c.operation.perform(z, {
    authData,
    inputData: { destination: ' +3211223344 ', extension: '201', idempotencyKey: '' },
  });
  assert.equal(calls[0].method, 'POST');
  assert.equal(calls[0].url, BASE + '/api/v1/calls/originate');
  assert.deepEqual(calls[0].body, { destination: '+3211223344', extension: '201' });
  assertRoute('POST', calls[0].url);
  assert.deepEqual(out, { originateId: 'o1', state: 'queued' });
});

test('Place a call sends the idempotency key and omits an empty extension', async () => {
  const { z, calls } = fakeZ({ status: 202, data: { originateId: 'o2' } });
  await app.creates.place_call.operation.perform(z, {
    authData,
    inputData: { destination: '0032112233', idempotencyKey: 'zap-7' },
  });
  assert.deepEqual(calls[0].body, { destination: '0032112233', idempotencyKey: 'zap-7' });
});

test('Find contact by number calls the lookup route and returns [] unless one entry matches', async () => {
  const s = app.searches.find_contact;
  let { z, calls } = fakeZ({ data: { number: '+32 11 22 33 44', key: '+3211223344', result: 'match', source: 'company', name: 'ACME NV', entryId: 7 } });
  let out = await s.operation.perform(z, { authData, inputData: { number: '+32 11 22 33 44' } });
  assert.equal(calls[0].url, BASE + '/api/v1/lookup/number');
  assert.deepEqual(calls[0].params, { number: '+32 11 22 33 44' });
  assertRoute('GET', calls[0].url);
  assert.equal(out.length, 1);
  assert.equal(out[0].name, 'ACME NV');
  assert.equal(out[0].id, 'company:7');
  for (const result of ['none', 'ambiguous', 'unresolvable']) {
    ({ z } = fakeZ({ data: { number: '999', key: '', result } }));
    assert.deepEqual(await s.operation.perform(z, { authData, inputData: { number: '999' } }), [], result);
  }
  ({ z } = fakeZ({ status: 500, data: {} }));
  await assert.rejects(s.operation.perform(z, { authData, inputData: { number: '999' } }));
});

test('Find call by CDR id fetches that record', async () => {
  const s = app.searches.find_call;
  let { z, calls } = fakeZ({ data: { id: 9, source: '201', destination: '+3211223344' } });
  let out = await s.operation.perform(z, { authData, inputData: { cdrId: '9' } });
  assert.equal(calls[0].url, BASE + '/api/v1/cdrs/9');
  assertRoute('GET', calls[0].url);
  assert.equal(out[0].id, 9);
  ({ z } = fakeZ({ status: 404 }));
  out = await s.operation.perform(z, { authData, inputData: { cdrId: '10' } });
  assert.deepEqual(out, []);
  await assert.rejects(s.operation.perform(fakeZ().z, { authData, inputData: { cdrId: 'abc' } }), /CDR ID/);
});

test('Find call by number scans the latest CDRs, newest first, on either side', async () => {
  const s = app.searches.find_call;
  const rows = [
    { id: 5, source: '+3299', destination: '201' },
    { id: 4, source: '201', destination: '+3211223344' },
    { id: 3, source: '+3211223344', destination: '201', originalDestination: '201' },
  ];
  const { z, calls } = fakeZ({ data: { items: rows } });
  const out = await s.operation.perform(z, { authData, inputData: { number: '+3211223344' } });
  assert.equal(calls[0].url, BASE + '/api/v1/cdrs');
  assert.deepEqual(calls[0].params, { limit: 200 });
  assertRoute('GET', calls[0].url);
  assert.deepEqual(out.map((r) => r.id), [4, 3]);
});

test('Find call needs a CDR id or a number', async () => {
  await assert.rejects(app.searches.find_call.operation.perform(fakeZ().z, { authData, inputData: {} }), /CDR ID or a number/);
});

test('every trigger, action and search has a stable key, noun and sample', () => {
  for (const group of [app.triggers, app.creates, app.searches]) {
    for (const [key, v] of Object.entries(group)) {
      assert.equal(v.key, key);
      assert.ok(v.noun, key);
      assert.ok(v.operation.sample, key);
      assert.match(v.display.description, /\.$/, key);
    }
  }
});

test('package.json names the platform version the code is written for', () => {
  const pkg = require('../package.json');
  assert.equal(app.platformVersion, pkg.dependencies['zapier-platform-core']);
  assert.equal(app.version, pkg.version);
});
