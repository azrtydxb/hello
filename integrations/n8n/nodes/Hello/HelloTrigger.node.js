'use strict';

const { helloRequest, describeError } = require('../../lib/request');
const { verify } = require('../../lib/signature');
const { EVENT_OPTIONS, extensions } = require('../../lib/events');

/**
 * Starts a workflow for Hello call events. Activating the workflow subscribes
 * a webhook in Hello (POST /api/v1/webhooks) to this workflow's own URL;
 * deactivating deletes it. Every delivery is checked against its signature
 * with the signing secret Hello returned at subscription (kept in the
 * workflow's static data), so a request that did not come from Hello starts
 * nothing. Hello retries a failed delivery and sends the same event id again:
 * the id is in the output, dedupe on it when an action must not run twice.
 */
class HelloTrigger {
	constructor() {
		this.description = {
			displayName: 'Hello PBX Trigger',
			name: 'helloTrigger',
			icon: 'file:hello.svg',
			group: ['trigger'],
			version: 1,
			subtitle: '={{$parameter["events"].join(", ")}}',
			description: 'Starts the workflow on call events of a Hello PBX',
			defaults: { name: 'Hello PBX Trigger' },
			inputs: [],
			outputs: ['main'],
			credentials: [{ name: 'helloApi', required: true }],
			webhooks: [{ name: 'default', httpMethod: 'POST', responseMode: 'onReceived', path: 'webhook' }],
			properties: [
				{
					displayName: 'Events',
					name: 'events',
					type: 'multiOptions',
					required: true,
					options: EVENT_OPTIONS,
					default: ['call.missed'],
					description: 'The events that start the workflow',
				},
				{
					displayName: 'Only These Extensions',
					name: 'extensions',
					type: 'string',
					default: '',
					placeholder: '101, 102',
					description: 'Extension numbers separated by commas. Empty is every extension.',
				},
			],
		};
		this.webhookMethods = {
			default: {
				async checkExists() {
					const data = this.getWorkflowStaticData('node');
					if (!data.webhookId || !data.secret) return false;
					try {
						const ep = await helloRequest(this, 'GET', `/api/v1/webhooks/${data.webhookId}`);
						return ep.url === this.getNodeWebhookUrl('default');
					} catch (err) {
						if (err && (err.httpCode === '404' || err.statusCode === 404)) return false;
						throw new Error(describeError(err));
					}
				},
				async create() {
					const events = this.getNodeParameter('events');
					if (!Array.isArray(events) || events.length === 0) throw new Error('Pick at least one event.');
					const exts = extensions(this.getNodeParameter('extensions', ''));
					const url = this.getNodeWebhookUrl('default');
					const body = { name: `n8n: ${this.getWorkflow().name || this.getWorkflow().id || 'workflow'}`.slice(0, 64), url, events };
					if (exts.length > 0) body.extensions = exts;
					const data = this.getWorkflowStaticData('node');
					let ep;
					try {
						ep = await helloRequest(this, 'POST', '/api/v1/webhooks', { body });
						// Hello answers an endpoint that already exists for this URL and
						// these events without its secret, which is shown once. With no
						// secret nothing can be verified: replace it with a new one.
						if (!ep.secret) {
							await helloRequest(this, 'DELETE', `/api/v1/webhooks/${ep.id}`);
							ep = await helloRequest(this, 'POST', '/api/v1/webhooks', { body });
						}
					} catch (err) {
						throw new Error(describeError(err));
					}
					if (!ep.secret) throw new Error('Hello did not return a signing secret for the webhook.');
					data.webhookId = ep.id;
					data.secret = ep.secret;
					return true;
				},
				async delete() {
					const data = this.getWorkflowStaticData('node');
					if (data.webhookId) {
						try {
							await helloRequest(this, 'DELETE', `/api/v1/webhooks/${data.webhookId}`);
						} catch (err) {
							// Already gone is what was wanted.
							if (!(err && (err.httpCode === '404' || err.statusCode === 404))) return false;
						}
					}
					delete data.webhookId;
					delete data.secret;
					return true;
				},
			},
		};
	}

	async webhook() {
		const req = this.getRequestObject();
		const res = this.getResponseObject();
		const data = this.getWorkflowStaticData('node');
		const reject = (status, message) => {
			res.status(status).json({ error: message });
			return { noWebhookResponse: true };
		};
		if (!data.secret) return reject(401, 'this workflow has no webhook secret');
		const why = verify(req.headers['hello-signature'], req.rawBody, [data.secret]);
		if (why) return reject(401, why);
		const body = req.body;
		if (!body || typeof body !== 'object' || typeof body.type !== 'string') return reject(400, 'not a Hello event');
		return { workflowData: [this.helpers.returnJsonArray([body])] };
	}
}

module.exports = { HelloTrigger };
