'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { Hello } = require('../nodes/Hello/Hello.node.js');
const { HelloApi } = require('../credentials/HelloApi.credentials.js');
const { baseUrl } = require('../lib/url');
const { fakeContext } = require('./helpers');

const run = (params, hello, extra = {}) => {
	const ctx = fakeContext({ params, hello, ...extra });
	return new Hello().execute.call(ctx).then((r) => ({ r, ctx }));
};

test('Place a call posts the destination, extension and idempotency key', async () => {
	const { r, ctx } = await run(
		{ resource: 'call', operation: 'place', destination: ' +3250123456 ', extension: '101', idempotencyKey: 'exec-9' },
		() => ({ id: 'req_1', state: 'queued' }),
	);
	assert.deepEqual(ctx.calls[0].body, { destination: '+3250123456', extension: '101', idempotencyKey: 'exec-9' });
	assert.equal(ctx.calls[0].method, 'POST');
	assert.equal(ctx.calls[0].url, 'https://pbx.example.com/api/v1/calls/originate');
	assert.deepEqual(r[0].map((x) => x.json), [{ id: 'req_1', state: 'queued' }]);
});

test('Place a call leaves out an empty extension and key', async () => {
	const { ctx } = await run({ resource: 'call', operation: 'place', destination: '200', extension: '', idempotencyKey: '' }, () => ({}));
	assert.deepEqual(ctx.calls[0].body, { destination: '200' });
});

test('Get a call record reads /cdrs/{id} and refuses a bad id', async () => {
	const { ctx } = await run({ resource: 'call', operation: 'get', cdrId: 42 }, () => ({ id: 42 }));
	assert.equal(ctx.calls[0].url, 'https://pbx.example.com/api/v1/cdrs/42');
	await assert.rejects(run({ resource: 'call', operation: 'get', cdrId: 0 }, () => ({})), /positive whole number/);
	await assert.rejects(run({ resource: 'call', operation: 'get', cdrId: '../x' }, () => ({})), /positive whole number/);
});

test('Get many lists calls as one item each, with the filters', async () => {
	const { r, ctx } = await run({ resource: 'call', operation: 'getMany', limit: 2, direction: 'inbound' }, () => ({
		items: [{ id: 3 }, { id: 2 }],
		next: '2',
	}));
	assert.deepEqual(ctx.calls[0].qs, { limit: 2, direction: 'inbound' });
	assert.deepEqual(r[0].map((x) => x.json.id), [3, 2]);
	const none = await run({ resource: 'call', operation: 'getMany', limit: 5, direction: '' }, () => ({ items: [], next: '' }));
	assert.equal(none.r[0].length, 0);
	assert.deepEqual(none.ctx.calls[0].qs, { limit: 5 });
});

test('Find by number passes the number as a query value, not in the path', async () => {
	const { r, ctx } = await run({ resource: 'contact', operation: 'findByNumber', number: '+32 50/12?x=1' }, () => ({ name: 'Acme' }));
	assert.equal(ctx.calls[0].url, 'https://pbx.example.com/api/v1/lookup/number');
	assert.deepEqual(ctx.calls[0].qs, { number: '+32 50/12?x=1' });
	assert.equal(r[0][0].json.name, 'Acme');
});

test('an API error becomes Hello\'s own message, and continue-on-fail keeps going', async () => {
	const fail = () => {
		const e = new Error('Request failed');
		e.response = { body: { error: { code: 'forbidden_scope', message: 'the token lacks the calls scope' } } };
		throw e;
	};
	await assert.rejects(run({ resource: 'call', operation: 'place', destination: '200' }, fail), /lacks the calls scope/);
	const ctx = fakeContext({ params: { resource: 'call', operation: 'place', destination: '200' }, hello: fail, items: [{ json: {} }, { json: {} }] });
	ctx.continueOnFail = () => true;
	const [out] = await new Hello().execute.call(ctx);
	assert.equal(out.length, 2);
	assert.match(out[0].json.error, /calls scope/);
});

test('the token is never put in a request or an output by the node', async () => {
	const { r, ctx } = await run({ resource: 'call', operation: 'place', destination: '200' }, () => ({ ok: true }));
	assert.ok(!JSON.stringify(ctx.calls).includes('hello_pat_SECRET'));
	assert.ok(!JSON.stringify(r).includes('hello_pat_SECRET'));
});

test('the base URL must be https (http only on localhost) and clean', () => {
	assert.equal(baseUrl('https://pbx.example.com/'), 'https://pbx.example.com');
	assert.equal(baseUrl(' https://pbx.example.com/hello/ '), 'https://pbx.example.com/hello');
	assert.equal(baseUrl('http://localhost:8080'), 'http://localhost:8080');
	for (const bad of ['http://pbx.example.com', 'ftp://x', 'pbx.example.com', '', 'https://u:p@pbx.example.com', 'https://pbx.example.com/?x=1', 'https://pbx.example.com/#f']) {
		assert.throws(() => baseUrl(bad), undefined, bad);
	}
});

test('the credential sends the token as a bearer header and tests with /auth/me', () => {
	const c = new HelloApi();
	assert.equal(c.name, 'helloApi');
	assert.equal(c.authenticate.properties.headers.Authorization, '=Bearer {{$credentials.apiToken}}');
	assert.equal(c.test.request.url, '/api/v1/auth/me');
	assert.ok(c.properties.find((p) => p.name === 'apiToken').typeOptions.password);
	const { documented } = require('./helpers');
	assert.ok(documented('GET', 'https://x.example' + c.test.request.url));
});

test('the package manifest lists the files that exist', () => {
	const pkg = require('../package.json');
	for (const f of [...pkg.n8n.credentials, ...pkg.n8n.nodes]) require('../' + f);
});
