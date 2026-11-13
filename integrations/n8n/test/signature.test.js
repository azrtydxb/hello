'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { verify } = require('../lib/signature');
const { sign } = require('./helpers');

const body = Buffer.from('{"type":"call.missed","id":"evt_1"}');

test('a signature made with the secret verifies', () => {
	assert.equal(verify(sign('whsec_a', body), body, ['whsec_a']), null);
});

test('a wrong secret, a changed body and a changed timestamp are refused', () => {
	assert.match(verify(sign('whsec_a', body), body, ['whsec_b']), /does not match/);
	assert.match(verify(sign('whsec_a', body), Buffer.from('{"type":"call.missed","id":"evt_2"}'), ['whsec_a']), /does not match/);
	const h = sign('whsec_a', body, 1000);
	assert.match(verify(h.replace('t=1000', 't=1001'), body, ['whsec_a'], 1000), /does not match/);
});

test('a stale or future timestamp is refused', () => {
	const now = 1_800_000_000;
	assert.match(verify(sign('s', body, now - 301), body, ['s'], now), /too old/);
	assert.match(verify(sign('s', body, now + 301), body, ['s'], now), /too old or too new/);
	assert.equal(verify(sign('s', body, now - 299), body, ['s'], now), null);
});

test('during a rotation any of the v1 values may match', () => {
	const t = 1_800_000_000;
	const a = sign('old', body, t).split(',v1=')[1];
	const b = sign('new', body, t).split(',v1=')[1];
	assert.equal(verify(`t=${t},v1=${a},v1=${b}`, body, ['new'], t), null);
});

test('malformed headers are refused, never thrown on', () => {
	for (const h of [undefined, '', 'nonsense', 't=abc,v1=00', 't=1', 'v1=' + '0'.repeat(64), 't=1,v1=zz', 't=1,v1=' + '0'.repeat(62)]) {
		assert.ok(verify(h, body, ['s'], 1), String(h));
	}
	assert.match(verify(sign('s', body), 'not a buffer', ['s']), /raw request body/);
});

test('the Go implementation signs the same bytes to the same value', () => {
	// Produced by internal/webhook.Sign("whsec_vector", 1800000000, body).
	const goSig = '9510db9c3d67ff52e5cc779c26ba83257fc1951127c95754e3a207d5d6048ed6';
	const raw = Buffer.from('{"id":"evt_1","type":"call.missed"}');
	assert.equal(verify(`t=1800000000,v1=${goSig}`, raw, ['whsec_vector'], 1800000000), null);
});
