'use strict';

const { helloRequest, describeError } = require('../../lib/request');

/**
 * Hello PBX actions: place a call, get a call record, list recent calls and
 * find the name behind a number. Each is one call to Hello's REST API with the
 * token of the credential; nothing else is sent anywhere.
 */
class Hello {
	constructor() {
		this.description = {
			displayName: 'Hello PBX',
			name: 'hello',
			icon: 'file:hello.svg',
			group: ['transform'],
			version: 1,
			subtitle: '={{$parameter["resource"] + ": " + $parameter["operation"]}}',
			description: 'Place calls and look up calls and contacts in a Hello PBX',
			defaults: { name: 'Hello PBX' },
			inputs: ['main'],
			outputs: ['main'],
			credentials: [{ name: 'helloApi', required: true }],
			properties: [
				{
					displayName: 'Resource',
					name: 'resource',
					type: 'options',
					noDataExpression: true,
					options: [
						{ name: 'Call', value: 'call' },
						{ name: 'Contact', value: 'contact' },
					],
					default: 'call',
				},
				{
					displayName: 'Operation',
					name: 'operation',
					type: 'options',
					noDataExpression: true,
					displayOptions: { show: { resource: ['call'] } },
					options: [
						{
							name: 'Place',
							value: 'place',
							action: 'Place a call',
							description: 'Ring an extension, then dial the destination (click to call)',
						},
						{ name: 'Get', value: 'get', action: 'Get a call record', description: 'One call detail record by id' },
						{ name: 'Get Many', value: 'getMany', action: 'Get many call records', description: 'The latest calls, newest first' },
					],
					default: 'place',
				},
				{
					displayName: 'Operation',
					name: 'operation',
					type: 'options',
					noDataExpression: true,
					displayOptions: { show: { resource: ['contact'] } },
					options: [
						{
							name: 'Find by Number',
							value: 'findByNumber',
							action: 'Find the contact name for a number',
							description: 'The name Hello would show for a phone number',
						},
					],
					default: 'findByNumber',
				},
				// Place
				{
					displayName: 'Destination',
					name: 'destination',
					type: 'string',
					required: true,
					default: '',
					placeholder: '+3250123456',
					description: 'What to dial: digits, +, * and # only',
					displayOptions: { show: { resource: ['call'], operation: ['place'] } },
				},
				{
					displayName: 'Extension',
					name: 'extension',
					type: 'string',
					default: '',
					description: 'The extension whose phone rings first. Empty uses the single extension of the token owner.',
					displayOptions: { show: { resource: ['call'], operation: ['place'] } },
				},
				{
					displayName: 'Idempotency Key',
					name: 'idempotencyKey',
					type: 'string',
					default: '',
					description: 'The same key within 10 minutes returns the same call instead of placing another. Use {{ $execution.id }} to protect against retries.',
					displayOptions: { show: { resource: ['call'], operation: ['place'] } },
				},
				// Get
				{
					displayName: 'CDR ID',
					name: 'cdrId',
					type: 'number',
					required: true,
					default: 0,
					description: 'The id in the cdrId field of a Call Completed event',
					displayOptions: { show: { resource: ['call'], operation: ['get'] } },
				},
				// Get many
				{
					displayName: 'Limit',
					name: 'limit',
					type: 'number',
					typeOptions: { minValue: 1, maxValue: 200 },
					default: 50,
					displayOptions: { show: { resource: ['call'], operation: ['getMany'] } },
				},
				{
					displayName: 'Direction',
					name: 'direction',
					type: 'options',
					options: [
						{ name: 'Any', value: '' },
						{ name: 'Inbound', value: 'inbound' },
						{ name: 'Outbound', value: 'outbound' },
						{ name: 'Internal', value: 'internal' },
					],
					default: '',
					displayOptions: { show: { resource: ['call'], operation: ['getMany'] } },
				},
				// Find by number
				{
					displayName: 'Number',
					name: 'number',
					type: 'string',
					required: true,
					default: '',
					placeholder: '+3250123456',
					displayOptions: { show: { resource: ['contact'], operation: ['findByNumber'] } },
				},
			],
		};
	}

	async execute() {
		const items = this.getInputData();
		const out = [];
		for (let i = 0; i < items.length; i++) {
			try {
				const resource = this.getNodeParameter('resource', i);
				const operation = this.getNodeParameter('operation', i);
				let res;
				if (resource === 'call' && operation === 'place') {
					const body = { destination: String(this.getNodeParameter('destination', i)).trim() };
					const ext = String(this.getNodeParameter('extension', i, '')).trim();
					const key = String(this.getNodeParameter('idempotencyKey', i, '')).trim();
					if (ext) body.extension = ext;
					if (key) body.idempotencyKey = key;
					res = await helloRequest(this, 'POST', '/api/v1/calls/originate', { body });
				} else if (resource === 'call' && operation === 'get') {
					const id = this.getNodeParameter('cdrId', i);
					if (!Number.isInteger(id) || id < 1) throw new Error('The CDR ID must be a positive whole number.');
					res = await helloRequest(this, 'GET', `/api/v1/cdrs/${id}`);
				} else if (resource === 'call' && operation === 'getMany') {
					const qs = { limit: this.getNodeParameter('limit', i, 50) };
					const dir = this.getNodeParameter('direction', i, '');
					if (dir) qs.direction = dir;
					res = await helloRequest(this, 'GET', '/api/v1/cdrs', { qs });
					res = Array.isArray(res) ? res : res.items || [];
				} else if (resource === 'contact' && operation === 'findByNumber') {
					const number = String(this.getNodeParameter('number', i)).trim();
					res = await helloRequest(this, 'GET', '/api/v1/lookup/number', { qs: { number } });
				} else {
					throw new Error(`Unknown operation ${resource}/${operation}.`);
				}
				const list = Array.isArray(res) ? res : [res];
				for (const r of list) out.push({ json: r, pairedItem: { item: i } });
			} catch (err) {
				if (this.continueOnFail()) {
					out.push({ json: { error: describeError(err) }, pairedItem: { item: i } });
					continue;
				}
				throw new Error(describeError(err));
			}
		}
		return [out];
	}
}

module.exports = { Hello };
