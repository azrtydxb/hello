---
name: hello-call-handling
description: Configure and run the call-handling features of a Kuvryn Hello PBX through its MCP server - call queues and their agents (log in, pause, live wallboard), conference rooms and their live controls, call park lots and parked calls, pickup groups, paging groups and multicast channels, text messaging, presence, voicemail messages and call recordings. Use when someone asks for a support queue, a meeting room, to park or pick up a call, to page a warehouse, to see who is busy or what is waiting in a queue, or to clean up voicemail and recordings.
license: Apache-2.0
metadata:
  hello-version: "v1"
---

# Hello call handling

These features sit behind a destination: an inbound route, IVR option, ring
group failover or time condition names a queue, conference, park code or
paging code. Create the feature first, then point something at it (see
`hello-routing` and `hello-ivr-and-hours`), then prove the path with
`testRouting` and `getFlowGraph` (see `hello-flow-graph`).

## Connecting

- MCP server: `https://<hello host>/mcp` (on kw: `https://hello.kw.watteel.lab/mcp`).
- Scopes: `read` to inspect, `write` to change. Queues, park lots, pickup
  groups, agents, conference control, messages, voicemail and recordings need
  the `operator` role. Conference rooms, paging groups, multicast channels
  and messaging settings need `admin` (role and scope).
- Tools are named after operations; see [Tools](references/tools.md).

## Queues

1. `listQueues`, `listExtensions` (agents are extensions), `listAnnouncements`
   and `listMOHClasses` for ids.
2. `createQueue`:

```json
{
  "name": "support",
  "number": "600",
  "strategy": "longest_idle",
  "agentTimeout": 20,
  "wrapUp": 10,
  "maxCallers": 20,
  "maxWait": 300,
  "joinEmpty": false,
  "members": [
    { "extensionId": 7, "penalty": 0 },
    { "extensionId": 8, "penalty": 1 },
    { "extensionId": 9, "dynamic": true }
  ],
  "timeout": { "kind": "voicemail", "target": "600" },
  "noAgents": { "kind": "extension", "target": "100" }
}
```

Strategies: ring_all, longest_idle, round_robin, fewest_calls, random,
linear. A `dynamic` member is an agent who logs in and out themselves.
The failovers `timeout`, `full` and `noAgents` take the usual destinations
and must not lead back to the queue. Announcements and music
(`mohClassId`, `periodicPromptId`, `positionPromptId`, `waitPromptId`)
are ids from `listAnnouncements` and `listMOHClasses`. 3. `putQueue` replaces the whole queue including members: `getQueue`, edit,
send all of it. Callers already waiting finish with the old settings. A
queue that something names cannot be renamed; `deleteQueue` is refused
(409) while a route, ring group, IVR menu or queue names it. 4. Watch it live with `listQueueStatus`: waiting callers (position, wait),
the longest wait, and each agent's state (available, on_call, ringing,
dnd, wrapping, paused, logged_out).

### Agents

`listQueueAgents` shows who is logged in or paused. `setQueueAgent` (path
argument `extension`) takes `{ "loggedIn": true }`, `{ "paused": true,
"reason": "lunch" }` or `{ "queues": ["support"] }`; absent fields keep their
value, and logging out clears a pause. Always say whose state you changed.

## Conferences

`createConference` (admin): `{ "name": "weekly", "number": "8100", "userPin":
"1234", "leaderPin": "9876", "maxParticipants": 20, "waitForLeader": true,
"muteOnEntry": true, "entryExit": "tone", "record": false }`. The number is
2 to 10 digits and not an extension or ring group number. PINs are write-only:
`listConferences` and `getConference` only say `hasUserPin` and
`hasLeaderPin`. On `updateConference` a PIN left out is kept and an empty
string clears it; an in-use room cannot be renumbered or deleted (409 names
the routes). A DID reaches a room with an inbound route of `destinationKind`
`conference`.

`controlConference` (operator) acts on a live room: `{ "action": "mute" |
"unmute" | "kick" | "lock" | "unlock" | "mute_all" | "unmute_all" | "end",
"participant": "<id from getConference>" }`. The room's owner applies it
within half a second; read `getConference` again to confirm. Ask before
`kick` or `end`.

## Park, parked calls and pickup

- `createParkLot`: `{ "name": "main", "slotStart": 701, "slotEnd": 720,
"returnSecs": 60, "voicemailSecs": 120 }`. The slot range must not overlap
  another lot, an extension number or a ring group name. A parked call rings
  its parker again after `returnSecs`, then goes to voicemail after
  `voicemailSecs`. `putParkLot`, `getParkLot`, `listParkLots`, `deleteParkLot`.
- `listParkedCalls` shows what is parked right now on every node (it needs
  the live state store; a 503 means it is down).
- `createPickupGroup`: `{ "name": "sales", "members": [7, 8, 9] }` (1 to 200
  extension ids); members can answer each other's ringing calls.
  `putPickupGroup`, `getPickupGroup`, `listPickupGroups`, `deletePickupGroup`.

## Paging and multicast

- `listMulticastChannels` is the catalogue. `createMulticastChannel` (admin):
  `{ "label": "warehouse", "address": "239.10.10.1", "port": 5004, "codec":
"G722", "kind": "paging" }`; the address is IPv4 multicast outside
  224.0.0.0/24, address and port are unique. `putMulticastChannel` and
  `deleteMulticastChannel` (refused while a paging group sends to it); a
  channel's kind cannot change while in use. Phones assigned to the channel
  through a phone group are in the `hello-phones` skill
  (`setPhoneGroupMulticast`).
- `createPagingGroup` (admin): `{ "name": "all-floor", "code": "*81",
"members": [7, 8], "callers": [], "priority": "normal" }`; dialling the code
  pages every member at once, one way. Optionally `multicastChannelId`. The
  code must not collide with a feature code (`listFeatureCodes`).
  `putPagingGroup`, `getPagingGroup`, `listPagingGroups`, `deletePagingGroup`
  (409 while an IVR menu names the group). An `emergency` priority group
  overrides do-not-disturb: confirm with the user first.

## Messaging

- `getMessageSettings` and `updateMessageSettings` (admin, whole object:
  `enabled`, `ttlHours`, `retentionDays`, `ratePerMinute`,
  `maxQueuedPerExtension`).
- `sendMessage`: `{ "targetKind": "extension", "target": "101", "body": "..." }`.
  `targetKind` is extension, ring_group, paging_group, broadcast (needs
  `"confirm": true`; ask the user first) or external (through a trunk with
  messaging on). It answers 202 and is queued; an unregistered recipient gets it
  when the phone registers, until the TTL ends. Message contents of other
  people are never readable through MCP.

## Presence, voicemail and recordings

- `listPresence`: each device's live state: idle, ringing, on-call or dnd.
- Voicemail: `listVoicemailBoxes` (unheard and total per box),
  `listVoicemailMessages` (`box` id required, `unheard=true`),
  `markVoicemailMessageHeard` (empty body heard, `{"heard": false}` unheard),
  `deleteVoicemailMessage` (permanent; ask first). PIN and greetings are in
  `hello-setup`.
- Recordings: `listRecordings` (filter by `extension`, page with `before`),
  `deleteRecording` (permanent; ask first). Listening is for people in the
  console.

## Recipe: a support line

1. `createAnnouncement` and `createMOHClass` if the user wants prompts or music.
2. `createQueue` with members and a `noAgents` failover.
3. `createInboundRoute` for the DID with `destinationKind` `queue` and
   `destination` the queue name, or an IVR option with `kind` `queue`.
4. `testRouting` for the DID, then `getFlowGraph` for dangling references.
5. After go-live, `listQueueStatus` shows if callers wait too long.

## References

- [Tools](references/tools.md) - every tool with its scope.
- [Failures](references/failures.md) - what the refusals mean.
