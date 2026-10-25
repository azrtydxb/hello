# Conference rooms

A conference room is a number that callers dial to talk together. Hello mixes
the audio itself, on one SIP node per room, so phones need nothing but a call.
Rooms are audio only.

## What a room has

| Setting              | Meaning                                                                                                                  |
| -------------------- | ------------------------------------------------------------------------------------------------------------------------ |
| Number               | 2 to 10 digits, dialled from any phone. It cannot be an extension or ring group number.                                  |
| User PIN             | Optional, 4 to 10 digits. Callers key it and press `#`.                                                                  |
| Leader PIN           | Optional, 4 to 10 digits, different from the user PIN. Makes the caller a leader.                                        |
| Seats                | 2 to 50, default 10. A caller entering a PIN or waiting for a leader holds a seat. The next caller gets `486 Busy Here`. |
| Wait for a leader    | Callers who are not leaders hear a soft beep every six seconds until a leader is in the room.                            |
| Mute on entry        | Everyone but a leader joins muted.                                                                                       |
| Join and leave sound | A tone, an uploaded announcement (an empty name falls back to the tone), or nothing.                                     |
| Record               | The room's mix is saved to the recordings list while anyone is in the room.                                              |
| Enabled              | A disabled room answers `480`.                                                                                           |

PINs are sealed under `HELLO_SECRET_KEY` like trunk passwords and are
write-only in the API and the console: only "a PIN is set" comes back. A room
with only a leader PIN lets everyone else in with `#`. Three wrong PINs end the
call, and ten wrong PINs in five minutes make the room refuse every attempt
until the window passes.

## Joining a room

- Dial the number from a phone.
- Point an inbound route at it (`destinationKind` `conference`, destination the
  room number). The flow canvas shows the room as a node, and the route tester
  reports `conference` in its decision.
- Give a phone a conference key: add the room number to the phone's BLF keys
  (Phones page). Pressing the key dials the room, and the lamp is lit while
  the room has people in it, from any node.
- IVR menus and time conditions will list the room as a target once they merge
  (the catalogue already offers it behind their requirements).

## Controls

A control is `*` and a digit, sent as RFC 2833 telephone events or SIP INFO.

| Keys | Who    | Does                                                        |
| ---- | ------ | ----------------------------------------------------------- |
| `*6` | anyone | Mute or unmute yourself. Not while a leader has muted you.  |
| `*5` | leader | Mute or unmute everyone who is not a leader.                |
| `*7` | leader | Lock or unlock the room. A locked room refuses non-leaders. |
| `*8` | leader | Remove the participant who joined last (not a leader).      |

The Conferences page shows each room's participants (leaders, muted and
waiting ones marked) with Mute, Kick, Lock, Mute all and End buttons for
operators. They call `POST /api/v1/conferences/{id}/control`, which queues a
command for the node that mixes the room; it applies within half a second.
Every control (DTMF or console) is in the audit log with its actor, and
`hello_conference_control_total` counts them.

## The mixer

Every 20 ms the owner takes one frame from each admitted participant, sums the
loudest four talkers above a noise gate, and sends each participant that sum
minus their own voice. Participants waiting for a leader are not mixed; they
hear only the hold source. The mixer runs at 8 kHz. Each participant keeps
its own codec: a room answers like every call Hello terminates (G.722 when
offered, else G.711, Opus only when nothing else is on offer), decodes the
participant to the mixing rate (a streaming half-band filter takes G.722 and
Opus down from 16 kHz) and encodes that participant's mix back in its codec, so
a G.711 phone and a G.722 phone share a room. Mixing at 8 kHz means wideband
participants hear narrowband quality; Opus is unavailable in `-race` builds.
Room state is kept per participant and its media sits behind that codec step,
so a video track can be added beside the audio later without redesigning the
room.

Wait-for-leader audio is the `ConferenceHold` dependency of the SIP server:
return the music-on-hold samples at the mixing rate from `HoldSamples(room)`
and waiting callers hear them; nil (the default) plays the beep.

Recording is held in memory per ten-minute segment and stored as a WAV in the
recordings bucket, one row per segment (initiated by `default`, correlation id
`conf-<room>-<start>-<n>`), so a node that dies loses at most one segment.

## High availability

A room is mixed on exactly one SIP node, its owner. Two nodes mixing one room
would split it into two rooms, so ownership is a lease in Valkey.

**Ownership.** `hello:conf:owner:<number>` names the owner. The first node to
take a free lease owns the room; it renews every two seconds (the lease lives
six) while the room has participants and releases it when the room empties.
The owner also publishes the room's live state (`hello:conf:state:<number>`)
for the console and reads control commands queued for it
(`hello:conf:cmd:<node>`).

**Joins on the other node.** Kamailio spreads calls over the nodes, so a join
often lands on a node that does not own the room. That node keeps the caller's
dialog and media relay and forwards the call to the owner as an ordinary SIP
call (an INVITE to the owner's advertised address). The owner answers it with a
participant, trusting it because the request carries a MAC under the cluster's
shared nonce secret. DTMF reaches the owner as RFC 2833 events through the
relay, or as SIP INFO forwarded by the node that holds the caller. The caller
sees one call; the owner writes no CDR for the forwarded leg. Hello forwards
rather than redirecting with a 302: phones reach the cluster only through
Kamailio, so a redirect to a node address would not be reachable.

**Losing a node.**

| What dies                                 | What happens                                                                                                                                                                                                                        |
| ----------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| The owner, with participants on it        | The in-call takeover claims each participant's dialog (it is replicated as a conference call). The survivor re-INVITEs the phone onto its own mixer, takes the lease from the dead owner and puts the participant back in the room. |
| The owner, participants forwarded from it | The node holding the caller sees the owner go OFFLINE and moves the call's far end onto a mixer on its own node. The phone is not re-INVITEd.                                                                                       |
| The node holding a forwarded caller       | The in-call takeover moves the caller to a survivor as a conference participant. The owner removes the stale forwarded leg once the node is OFFLINE.                                                                                |
| A draining owner                          | New joins take the lease from it (it is not READY); its participants are handed off like any call and rejoin on the new owner.                                                                                                      |

Audio is lost for about the in-call HA gap (see [High availability](ha.md)).
After a recovery a participant is asked for the PIN again if the room has one,
and the room's lock and mute state are rebuilt empty: participants who had been
muted by a leader are unmuted, and the room is unlocked. A recording in
progress loses the segment in the dead node's memory.

**Limitations, stated plainly.**

- With three or more SIP nodes, a participant taken over by a node that is not
  (and cannot become) the room's owner is dropped and must redial; with two
  nodes the survivor always owns the room.
- If Valkey is unavailable a node keeps mixing the rooms it already hosts, but
  cannot start a room or forward a join (new callers get `503`).
- If the owner is cut off from Valkey while alive, its lease lapses and another
  node may take the room. When the first node reconnects and finds the lease
  taken it ends its copy of the room (participants hear a tone and are hung up)
  rather than keep two mixes; a draining node instead keeps it until its
  participants are handed off.

## Metrics

| Metric                              | Meaning                                                                                           |
| ----------------------------------- | ------------------------------------------------------------------------------------------------- |
| `hello_conference_rooms`            | Rooms this node mixes.                                                                            |
| `hello_conference_participants`     | Participants per room on this node, waiting ones included.                                        |
| `hello_conference_joins_total`      | Join attempts by result: joined, forwarded, bad_pin, locked, full, disabled, unavailable, failed. |
| `hello_conference_control_total`    | Control actions by action and source (dtmf, api).                                                 |
| `hello_conference_owner_total`      | Ownership events: claimed, taken_over, lost, rehomed.                                             |
| `hello_conference_recordings_total` | Recording segments by result: stored, failed, empty.                                              |

## API

`GET|POST /api/v1/conferences`, `GET|PUT|DELETE /api/v1/conferences/{id}` and
`POST /api/v1/conferences/{id}/control` (see the OpenAPI document). Reading is
open to a viewer; creating, changing and deleting a room is admin only;
controlling a live room needs an operator. Deleting or renumbering a room that
an inbound route targets is refused with `409`.
