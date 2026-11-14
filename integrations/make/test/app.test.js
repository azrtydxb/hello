'use strict';

// The Make app is data, not code: these tests check that every file parses,
// that the manifest lists what exists, and that every request the modules make
// is a documented Hello operation with parameters Hello accepts. They do not
// and cannot test Make itself (see docs/n8n-make.md, "Not covered").

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const root = path.join(__dirname, '..');
const spec = JSON.parse(fs.readFileSync(path.join(root, '../../internal/api/openapi.json'), 'utf8'));
const readJSON = (rel) => JSON.parse(fs.readFileSync(path.join(root, rel), 'utf8'));

function files(dir) {
	return fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
		const p = path.join(dir, e.name);
		return e.isDirectory() ? (e.name === 'test' ? [] : files(p)) : [p];
	});
}

const manifest = readJSON('makecomapp.json');
const base = readJSON('general/base.imljson');

/** The OpenAPI operation for a request of the app, with {{...}} read as an id. */
function operation(method, url) {
	const p = '/api/v1' + url.replace(/\{\{[^}]*\}\}/g, '1');
	for (const [route, ops] of Object.entries(spec.paths)) {
		if (new RegExp('^' + route.replace(/\{[^}]+\}/g, '[^/]+') + '$').test(p) && ops[method.toLowerCase()]) return { route, op: ops[method.toLowerCase()] };
	}
	return null;
}

function schemaProps(ref) {
	const s = spec.components.schemas[ref.split('/').pop()];
	return Object.keys(s.properties || {});
}

test('every file of the app parses as JSON', () => {
	const all = files(root).filter((f) => /\.(json|imljson)$/.test(f));
	assert.ok(all.length >= 20, `only ${all.length} files`);
	for (const f of all) assert.doesNotThrow(() => JSON.parse(fs.readFileSync(f, 'utf8')), path.relative(root, f));
});

test('the manifest lists directories that exist and every module is listed', () => {
	const listed = new Set();
	for (const kind of ['connection', 'webhook', 'module']) {
		for (const [name, c] of Object.entries(manifest.components[kind])) {
			assert.ok(fs.statSync(path.join(root, c.dir)).isDirectory(), `${kind} ${name}`);
			listed.add(c.dir);
		}
	}
	for (const d of fs.readdirSync(path.join(root, 'modules'))) assert.ok(listed.has('modules/' + d), `modules/${d} is not in the manifest`);
	assert.ok(fs.existsSync(path.join(root, manifest.generalCodeFiles.base)));
});

test('the token travels only in the Authorization header, which is sanitised from logs', () => {
	assert.equal(base.headers.Authorization, 'Bearer {{connection.token}}');
	assert.ok(base.log.sanitize.includes('request.headers.authorization'));
	const comm = readJSON('connections/hello/communication.imljson');
	assert.ok(comm.log.sanitize.includes('request.headers.authorization'));
	for (const f of files(root)) {
		if (f.endsWith('base.imljson') || f.endsWith('communication.imljson')) continue;
		assert.ok(!/connection\.token|parameters\.token/.test(fs.readFileSync(f, 'utf8')), path.relative(root, f));
	}
	const params = readJSON('connections/hello/parameters.imljson');
	assert.equal(params.find((p) => p.name === 'token').type, 'password');
	assert.equal(params.find((p) => p.name === 'url').type, 'url');
});

test('the connection check calls a documented, harmless operation', () => {
	const c = readJSON('connections/hello/communication.imljson');
	assert.equal(c.method, 'GET');
	assert.ok(c.url.endsWith('/api/v1/auth/me'));
	assert.ok(operation('GET', '/auth/me'));
});

test('every module request is a documented Hello operation', () => {
	for (const dir of fs.readdirSync(path.join(root, 'modules'))) {
		const api = readJSON(`modules/${dir}/api.imljson`);
		if (api.type === 'instant_trigger') continue;
		if (dir === 'make-api-call') continue; // the path and method are the user's
		// GET /lookup/number is added by the Zapier integration (P7, #304) and is
		// not in the document until that merges.
		if (api.url === '/lookup/number') continue;
		assert.ok(operation(api.method, api.url), `${dir}: ${api.method} ${api.url} is not in openapi.json`);
	}
});

test('Place a Call sends only fields the originate request accepts', () => {
	const api = readJSON('modules/place-call/api.imljson');
	const { op } = operation('POST', api.url);
	const props = schemaProps(op.requestBody.content['application/json'].schema.$ref);
	for (const k of Object.keys(api.body)) assert.ok(props.includes(k), k);
	const params = readJSON('modules/place-call/parameters.imljson');
	assert.ok(params.find((p) => p.name === 'destination').required);
	assert.deepEqual(Object.keys(api.body).sort(), params.map((p) => p.name).sort());
});

test('the list and get modules use query parameters and ids Hello documents', () => {
	const list = readJSON('modules/list-calls/api.imljson');
	const { op } = operation('GET', list.url);
	const names = op.parameters.map((p) => p.name);
	for (const k of Object.keys(list.qs)) assert.ok(names.includes(k), k);
	assert.equal(list.response.iterate, '{{body.items}}');
	assert.equal(readJSON('modules/get-call/api.imljson').url, '/cdrs/{{parameters.cdrId}}');
	assert.equal(readJSON('modules/find-contact/api.imljson').qs.number, '{{parameters.number}}');
});

test('the webhook subscribes and unsubscribes through the documented webhook API', () => {
	const attach = readJSON('webhooks/call-event/attach.imljson');
	const { op } = operation('POST', attach.url);
	const props = schemaProps(op.requestBody.content['application/json'].schema.$ref);
	for (const k of Object.keys(attach.body)) assert.ok(props.includes(k), k);
	assert.equal(attach.body.url, '{{webhook.url}}');
	assert.ok(operation('DELETE', '/webhooks/{{webhook.data.id}}'));
	assert.ok(operation('PATCH', '/webhooks/{{webhook.data.id}}'));
	assert.equal(readJSON('webhooks/call-event/detach.imljson').method, 'DELETE');
	// A 404 on delete means it is already gone: not an error.
	assert.match(readJSON('webhooks/call-event/detach.imljson').response.valid, /404/);
});

test('every event the webhook offers is one Hello documents', () => {
	const doc = fs.readFileSync(path.join(root, '../../docs/integrations.md'), 'utf8');
	const events = readJSON('webhooks/call-event/parameters.imljson').find((p) => p.name === 'events');
	for (const o of events.options) assert.ok(doc.includes('`' + o.value + '`'), o.value);
	assert.ok(events.options.length >= 7);
});
