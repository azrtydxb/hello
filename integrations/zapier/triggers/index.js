'use strict';

const { hookTrigger } = require('../lib/events.js');

const triggers = [
  hookTrigger({
    key: 'call_ringing',
    type: 'call.ringing',
    label: 'Call Ringing',
    description: 'Triggers when an extension starts ringing.',
  }),
  hookTrigger({
    key: 'call_answered',
    type: 'call.answered',
    label: 'Call Answered',
    description: 'Triggers when an extension answers a call.',
  }),
  hookTrigger({
    key: 'call_missed',
    type: 'call.missed',
    label: 'Call Missed',
    description: 'Triggers when a call reached an extension and nobody answered.',
    extra: { endedAt: '2026-10-26T09:14:03.500Z', durationMs: 30400, disposition: 'no_answer', reason: 'no_answer' },
  }),
  hookTrigger({
    key: 'call_completed',
    type: 'call.completed',
    label: 'Call Completed',
    description: 'Triggers when the call detail record of a finished call is stored.',
    extra: { endedAt: '2026-10-26T09:15:10.000Z', durationMs: 96900, disposition: 'answered', cdrId: 1042 },
  }),
  hookTrigger({
    key: 'call_recording_ready',
    type: 'call.recording.ready',
    label: 'Call Recording Ready',
    description: 'Triggers when the recording of a call has been stored.',
    extra: { durationMs: 96900, recordingId: 311 },
  }),
];

module.exports = Object.fromEntries(triggers.map((t) => [t.key, t]));
