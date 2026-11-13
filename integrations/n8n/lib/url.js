'use strict';

/**
 * The Hello base URL as the credentials hold it: https only (a PBX console is
 * never reached over plain http except on localhost for a lab), no
 * credentials, query or fragment in it, and no trailing slash.
 */
function baseUrl(raw) {
	let u;
	try {
		u = new URL(String(raw || '').trim());
	} catch {
		throw new Error('The Hello URL is not a valid address (for example https://pbx.example.com).');
	}
	const local = u.hostname === 'localhost' || u.hostname === '127.0.0.1' || u.hostname === '[::1]';
	if (u.protocol !== 'https:' && !(u.protocol === 'http:' && local)) {
		throw new Error('The Hello URL must start with https://.');
	}
	if (u.username || u.password || u.search || u.hash) {
		throw new Error('The Hello URL must be the address only, without credentials, query or fragment.');
	}
	return (u.origin + u.pathname).replace(/\/+$/, '');
}

module.exports = { baseUrl };
