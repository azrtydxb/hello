'use strict';

/**
 * Credentials for a Hello PBX: the address of the console and an API token.
 * Create the token in Hello under Integrations, API access. A personal token
 * (hello_pat_...) or a service account works; the trigger needs an
 * administrator's token with the admin, secrets, events and read scopes (it
 * creates and deletes webhooks), the actions need calls (place a call) and
 * read.
 */
class HelloApi {
	constructor() {
		this.name = 'helloApi';
		this.displayName = 'Hello PBX API';
		this.documentationUrl = 'https://github.com/azrtydxb/hello/blob/main/docs/n8n-make.md';
		this.properties = [
			{
				displayName: 'Hello URL',
				name: 'baseUrl',
				type: 'string',
				default: '',
				placeholder: 'https://pbx.example.com',
				description: 'The address of your Hello console, without a path',
				required: true,
			},
			{
				displayName: 'API Token',
				name: 'apiToken',
				type: 'string',
				typeOptions: { password: true },
				default: '',
				required: true,
			},
		];
		this.authenticate = {
			type: 'generic',
			properties: { headers: { Authorization: '=Bearer {{$credentials.apiToken}}' } },
		};
		this.test = {
			request: { baseURL: '={{$credentials.baseUrl}}', url: '/api/v1/auth/me', method: 'GET' },
		};
	}
}

module.exports = { HelloApi };
