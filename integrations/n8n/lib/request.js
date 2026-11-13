'use strict';

const { baseUrl } = require('./url');

/**
 * Calls the Hello API with the node's credentials. `ctx` is the n8n execution
 * or hook context. The token travels only in the Authorization header that n8n
 * adds from the credential, and only to the address in that credential.
 */
async function helloRequest(ctx, method, path, { qs, body } = {}) {
	const creds = await ctx.getCredentials('helloApi');
	const options = {
		method,
		url: baseUrl(creds.baseUrl) + path,
		json: true,
		headers: { Accept: 'application/json' },
	};
	if (qs && Object.keys(qs).length > 0) options.qs = qs;
	if (body !== undefined) options.body = body;
	return ctx.helpers.httpRequestWithAuthentication.call(ctx, 'helloApi', options);
}

/** The message Hello gave for a failed call, for a readable n8n error. */
function describeError(err) {
	const data = err && err.response && err.response.body;
	const fromApi = data && data.error && (data.error.message || data.error.code);
	return fromApi || (err && err.message) || 'the Hello API call failed';
}

module.exports = { helloRequest, describeError };
