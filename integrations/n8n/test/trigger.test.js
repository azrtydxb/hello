'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { HelloTrigger } = require('../nodes/Hello/HelloTrigger.node.js');
const { EVENT_TYPES } = require('../lib/events');
const { fakeContext, sign, spec } = require('./helpers');

const methods = new HelloTrigger().webhookMethods.default;

test('activating subscribes the workflow URL and keeps the signing secret', async () => {
	const ctx = fakeContext({
		params: { events: ['call.missed', 'voicemail.received'], extensions: '101, 102 101' },
		hello: () => ({ id: 12, secret: 'whsec_xyz' }),
	});
	assert.equal(await methods.create.call(ctx), true);
	assert.deepEqual(ctx.calls[0].body, {
		name: 'n8n: Missed call alert',
		url: 'https://n8n.example.com/webhook/abc/webhook',
		events: ['call.missed', 'voicemail.received'],
		extensions: ['101', '102'],
	});
	assert.deepEqual(ctx.staticData, { webhookId: 12, secret: 'whsec_xyz' });
	assert.equal(ctx.calls.length, 1);
});

test('an endpoint Hello already had (no secret) is replaced so it can be verified', async () => {
	let n = 0;
	const ctx = fakeContext({
		params: { events: ['call.missed'], extensions: '' },
		hello: (o) => {
			n++;
			if (o.method === 'POST' && n === 1) return { id: 5, secret: null };
			if (o.method === 'DELETE') return undefined;
			return { id: 6, secret: 'whsec_new' };
		},
	});
	await methods.create.call(ctx);
	assert.deepEqual(ctx.calls.map((c) => c.method), ['POST', 'DELETE', 'POST']);
	assert.equal(ctx.calls[1].url, 'https://pbx.example.com/api/v1/webhooks/5');
	assert.deepEqual(ctx.staticData, { webhookId: 6, secret: 'whsec_new' });
	assert.equal(ctx.calls[0].body.extensions, undefined);
});

test('create refuses no events, a bad extension and a missing secret', async () => {
	await assert.rejects(methods.create.call(fakeContext({ params: { events: [], extensions: '' } })), /at least one event/);
	await assert.rejects(methods.create.call(fakeContext({ params: { events: ['call.missed'], extensions: 'abc' } })), /not an extension/);
	await assert.rejects(
		methods.create.call(fakeContext({ params: { events: ['call.missed'], extensions: '' }, hello: () => ({ id: 1, secret: null }) })),
		/signing secret/,
	);
});

test('checkExists is false until subscribed and when Hello lost the endpoint', async () => {
	assert.equal(await methods.checkExists.call(fakeContext()), false);
	const url = 'https://n8n.example.com/webhook/abc/webhook';
	const ok = fakeContext({ staticData: { webhookId: 3, secret: 's' }, hello: () => ({ id: 3, url }) });
	assert.equal(await methods.checkExists.call(ok), true);
	const moved = fakeContext({ staticData: { webhookId: 3, secret: 's' }, hello: () => ({ id: 3, url: 'https://other.example/x' }) });
	assert.equal(await methods.checkExists.call(moved), false);
	const gone = fakeContext({
		staticData: { webhookId: 3, secret: 's' },
		hello: () => {
			const e = new Error('nope');
			e.httpCode = '404';
			throw e;
		},
	});
	assert.equal(await methods.checkExists.call(gone), false);
});

test('deactivating deletes the endpoint, tolerates it being gone and forgets the secret', async () => {
	const ctx = fakeContext({ staticData: { webhookId: 9, secret: 's' }, hello: () => undefined });
	assert.equal(await methods.delete.call(ctx), true);
	assert.equal(ctx.calls[0].method, 'DELETE');
	assert.deepEqual(ctx.staticData, {});
	const gone = fakeContext({
		staticData: { webhookId: 9, secret: 's' },
		hello: () => {
			const e = new Error('x');
			e.statusCode = 404;
			throw e;
		},
	});
	assert.equal(await methods.delete.call(gone), true);
	assert.deepEqual(gone.staticData, {});
	const down = fakeContext({
		staticData: { webhookId: 9, secret: 's' },
		hello: () => {
			throw new Error('connection refused');
		},
	});
	assert.equal(await methods.delete.call(down), false);
	assert.equal(down.staticData.webhookId, 9);
});

const event = { apiVersion: '1', id: 'evt_1', type: 'call.missed', createdAt: '2026-11-12T10:00:00Z', data: { callId: 'c1', from: { number: '+32' } } };

function delivery(secret, over = {}) {
	const rawBody = Buffer.from(JSON.stringify(event));
	return fakeContext({
		staticData: { webhookId: 1, secret: 'whsec_abc' },
		request: { headers: { 'hello-signature': sign(secret, rawBody) }, rawBody, body: event, ...over },
	});
}

test('a signed delivery starts the workflow with the event', async () => {
	const ctx = delivery('whsec_abc');
	const out = await new HelloTrigger().webhook.call(ctx);
	assert.deepEqual(out.workflowData[0].map((x) => x.json), [event]);
	assert.equal(ctx.responded, undefined);
});

test('a delivery with a wrong or missing signature, or a changed body, starts nothing', async () => {
	for (const ctx of [
		delivery('whsec_other'),
		delivery('whsec_abc', { headers: {} }),
		delivery('whsec_abc', { rawBody: Buffer.from('{"type":"call.missed","id":"forged"}') }),
		delivery('whsec_abc', { rawBody: undefined }),
	]) {
		const out = await new HelloTrigger().webhook.call(ctx);
		assert.equal(out.workflowData, undefined);
		assert.equal(out.noWebhookResponse, true);
		assert.equal(ctx.responded.status, 401);
	}
});

test('a workflow without a stored secret refuses every delivery', async () => {
	const ctx = delivery('whsec_abc');
	ctx.staticData.secret = undefined;
	const out = await new HelloTrigger().webhook.call(ctx);
	assert.equal(out.workflowData, undefined);
	assert.equal(ctx.responded.status, 401);
});

test('a signed body that is not an event is refused', async () => {
	const rawBody = Buffer.from('"hello"');
	const ctx = fakeContext({
		staticData: { secret: 'whsec_abc' },
		request: { headers: { 'hello-signature': sign('whsec_abc', rawBody) }, rawBody, body: 'hello' },
	});
	const out = await new HelloTrigger().webhook.call(ctx);
	assert.equal(out.workflowData, undefined);
	assert.equal(ctx.responded.status, 400);
});

test('every event the trigger offers is one Hello can emit', () => {
	const doc = require('node:fs').readFileSync(require('node:path').join(__dirname, '../../../docs/integrations.md'), 'utf8');
	for (const t of EVENT_TYPES) assert.ok(doc.includes('`' + t + '`'), t);
	assert.ok(spec.paths['/api/v1/webhooks'].post);
});
