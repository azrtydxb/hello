'use strict';

const crypto = require('node:crypto');

/** How far a signature timestamp may be from this machine's clock. */
const TOLERANCE_SECONDS = 5 * 60;

/**
 * Verifies a Hello-Signature header ("t=<unix seconds>,v1=<hex>[,v1=<hex>]")
 * against the exact bytes of the request body. It returns null when the
 * signature is good and a short reason otherwise. The HMAC-SHA256 is over
 * "<t>.<body>"; during a secret rotation the header carries one v1 per valid
 * secret, and any may match. The comparison is constant time.
 */
function verify(header, rawBody, secrets, nowSeconds = Math.floor(Date.now() / 1000)) {
	if (typeof header !== 'string' || header === '') return 'the Hello-Signature header is missing';
	if (!Buffer.isBuffer(rawBody)) return 'the raw request body is not available';
	let t;
	const sigs = [];
	for (const part of header.split(',')) {
		const i = part.indexOf('=');
		if (i < 0) return 'the Hello-Signature header is malformed';
		const k = part.slice(0, i).trim();
		const v = part.slice(i + 1).trim();
		if (k === 't') {
			if (!/^\d{1,12}$/.test(v)) return 'the Hello-Signature header is malformed';
			t = Number(v);
		} else if (k === 'v1') {
			if (!/^[0-9a-f]{64}$/i.test(v)) return 'the Hello-Signature header is malformed';
			sigs.push(Buffer.from(v, 'hex'));
		}
	}
	if (t === undefined || sigs.length === 0) return 'the Hello-Signature header is malformed';
	if (Math.abs(nowSeconds - t) > TOLERANCE_SECONDS) return 'the signature timestamp is too old or too new';
	let ok = false;
	for (const secret of secrets) {
		const want = crypto
			.createHmac('sha256', secret)
			.update(String(t))
			.update('.')
			.update(rawBody)
			.digest();
		for (const got of sigs) {
			if (got.length === want.length && crypto.timingSafeEqual(got, want)) ok = true;
		}
	}
	return ok ? null : 'the signature does not match';
}

module.exports = { verify, TOLERANCE_SECONDS };
