'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');

/** The route table of Hello's API, to check every call the nodes make. */
const spec = JSON.parse(fs.readFileSync(path.join(__dirname, '../../../internal/api/openapi.json'), 'utf8'));

/** Whether "METHOD /api/v1/x/12" is a documented operation. */
function documented(method, url) {
	const p = new URL(url).pathname;
	for (const [route, ops] of Object.entries(spec.paths)) {
		if (!ops[method.toLowerCase()]) continue;
		const re = new RegExp('^' + route.replace(/\{[^}]+\}/g, '[^/]+') + '$');
		if (re.test(p)) return true;
	}
	return false;
}

/**
 * A fake of the n8n context a node runs in. `hello` answers the API calls:
 * (options) => body or throws. Every request is recorded and checked against
 * the OpenAPI document.
 */
function fakeContext({ params = {}, items = [{ json: {} }], hello = () => ({}), staticData = {}, creds, workflow, webhookUrl, request } = {}) {
	const calls = [];
	const ctx = {
		calls,
		staticData,
		getInputData: () => items,
		getNodeParameter: (name, _i, dflt) => {
			if (name in params) return params[name];
			if (dflt !== undefined) return dflt;
			throw new Error(`parameter ${name} is not set`);
		},
		getCredentials: async () => creds || { baseUrl: 'https://pbx.example.com', apiToken: 'hello_pat_SECRET' },
		helpers: {
			httpRequestWithAuthentication: async function (name, options) {
				if (name !== 'helloApi') throw new Error('wrong credential ' + name);
				calls.push(options);
				if (!options.url.startsWith('https://pbx.example.com/')) throw new Error('request to ' + options.url);
				if (!documented(options.method, options.url) && !(options.url.endsWith('/api/v1/lookup/number') && options.method === 'GET')) {
					throw new Error(`${options.method} ${options.url} is not in internal/api/openapi.json`);
				}
				return hello(options);
			},
			returnJsonArray: (a) => (Array.isArray(a) ? a : [a]).map((json) => ({ json })),
		},
		continueOnFail: () => false,
		getWorkflowStaticData: () => staticData,
		getWorkflow: () => workflow || { id: '7', name: 'Missed call alert' },
		getNodeWebhookUrl: () => webhookUrl || 'https://n8n.example.com/webhook/abc/webhook',
		getRequestObject: () => request,
		getResponseObject: () => ({
			status(s) {
				ctx.responded = { status: s };
				return this;
			},
			json(b) {
				ctx.responded.body = b;
			},
		}),
	};
	return ctx;
}

/** Signs a body the way Hello does. */
function sign(secret, rawBody, t = Math.floor(Date.now() / 1000)) {
	const mac = crypto.createHmac('sha256', secret).update(`${t}.`).update(rawBody).digest('hex');
	return `t=${t},v1=${mac}`;
}

module.exports = { fakeContext, sign, documented, spec };
