'use strict';

/** The call events a trigger may subscribe to (docs/integrations.md). */
const EVENT_OPTIONS = [
	{ name: 'Call Answered', value: 'call.answered' },
	{ name: 'Call Completed (has the CDR id)', value: 'call.completed' },
	{ name: 'Call Ended', value: 'call.ended' },
	{ name: 'Call Missed', value: 'call.missed' },
	{ name: 'Call Recording Ready', value: 'call.recording.ready' },
	{ name: 'Call Ringing', value: 'call.ringing' },
	{ name: 'Voicemail Received', value: 'voicemail.received' },
];

const EVENT_TYPES = EVENT_OPTIONS.map((o) => o.value);

/** A comma separated extension list as an array; throws on a bad number. */
function extensions(raw) {
	const out = [];
	for (const x of String(raw || '').split(/[\s,]+/)) {
		if (x === '') continue;
		if (!/^[0-9]{2,10}$/.test(x)) throw new Error(`"${x}" is not an extension number.`);
		if (!out.includes(x)) out.push(x);
	}
	return out;
}

module.exports = { EVENT_OPTIONS, EVENT_TYPES, extensions };
