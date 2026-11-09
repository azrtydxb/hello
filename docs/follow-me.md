# Follow-me

Follow-me makes a call to an extension ring more than that extension's own
phones: up to five more targets ring too. A target is an external number (a
mobile, a home line), another extension or a ring group. It is set per
extension, under **Follow-me** in the extension's drawer in the console, by the
user of the extension under **My phone, Call forwarding**, through
`GET|PUT /api/v1/extensions/{id}/follow-me` (and
`GET|PUT /api/v1/me/extensions/{id}/follow-me` for a user), and through the
MCP tools `getFollowMe` and `putFollowMe`.

## The profile

| Field               | Meaning                                                                                                              |
| ------------------- | -------------------------------------------------------------------------------------------------------------------- |
| Follow-me (enabled) | Whether it rings. A profile can be turned on only with at least one target.                                          |
| Ring                | `simultaneous` rings the own phones and every target at once; `sequential` starts the targets one after another.     |
| Own phones          | Whether the extension's own devices ring too (default on).                                                           |
| Give up after       | 5 to 120 seconds (default 30). Then the no-answer forward or voicemail of the extension applies.                     |
| Press 1 to accept   | An external target plays "press 1 to accept" and is dropped after 10 seconds without the digit (default on).         |
| DND stops follow-me | When the extension is on do-not-disturb, follow-me does not ring (default on).                                       |
| Targets             | At most 5, in ring order. Each has a kind (external, extension, ring group), a target and, for `sequential`, a time. |

A target cannot be the extension itself. An external target is 2 to 32 digits
with an optional leading `+`; an extension or ring group target must exist, and
cannot be deleted, renumbered or renamed while a profile names it.

## When it rings

Follow-me runs when a call reaches the extension's ring step. The order of
what the extension does with a call is:

1. Forward always (`*72`) wins. A call that is forwarded does not reach
   follow-me.
2. Do-not-disturb. With **DND stops follow-me** on (the default) the call is
   handled as DND always was (voicemail or 603), and follow-me does not ring.
   With it off, DND blocks the extension's own phones only, and the targets
   ring.
3. Follow-me rings.
4. When nobody answers within **Give up after**, the no-answer forward and then
   voicemail apply, as for a plain ring.

The ring is the same for a call dialled directly, routed from an inbound route,
a time condition or a forward, and for an IVR option that dials the extension.
It does not apply to the members of a ring group (the group rings their
phones) or to a ring group or queue failing over to an extension.

### Simultaneous and sequential

- **Simultaneous**: the own phones and every target ring at once. The first
  to answer wins and the others are cancelled with the reason "Call completed
  elsewhere", so phones show an answered-elsewhere call instead of a missed one.
- **Sequential**: the own phones ring from the start and keep ringing. The
  first target rings for its time (seconds), then it stops and the next one
  starts, and so on. The last target rings until **Give up after**. A target with
  time 0 that is not last rings until it fails (busy, declined, unreachable)
  and then the next starts. In an IVR dial-out the earlier targets keep ringing
  when the next starts, because the IVR ring cannot stop a single leg.
- An extension with no registered phone rings its targets only.

### Targets

- **External number**: dialled through outbound routing, as if the extension
  dialled it. The route's trunk and its caller-ID rule apply: the caller ID is
  the original caller's number when the trunk lets it through, else the
  extension's outbound identity. A number that is one of Hello's own extensions
  rings as that extension. At most one trunk carries a target, the first usable
  one in the route's order; its destinations all ring.
- **Extension**: that extension's phones. Its own forwarding and DND do not
  apply (a DND target is skipped); a target that is the extension itself or an
  extension already in the forward chain is skipped as a loop.
- **Ring group**: the phones of the group's eligible members (a member on DND is
  skipped unless the group ignores DND), all at once.

A call that goes out to a carrier and comes back to Hello as a new call is a
new call: the carrier's hop limit is the only protection against that loop, so
do not point an external target at a number that forwards to this extension.

### Press 1 to accept

An external target can be a mobile whose carrier answers for it when the phone
is off: its voicemail would take the call and the caller would leave a message
there. With **Press 1 to accept** on, the target is asked first. Hello answers
the carrier's leg itself, plays "You have a forwarded call. Press 1 to accept
it." in the deployment's prompt language, and waits for the digit (RFC 2833
telephone-event, or SIP INFO). The caller is not connected until the digit
arrives; the leg is dropped after 10 seconds without it and its result in the
call record is `declined`. Only one target is asked at a time; a second one that
answers meanwhile waits its turn. The call needs media anchoring for this, which
every call with an SDP offer has; a call without an offer skips its external
targets. The prompt is pre-generated with the other static prompts when text to
speech is configured; without it a beep is played.

## Feature codes

`*76` turns the profile of the extension of the phone that dials it on and
`*77` turns it off, like `*78` and `*79` do for DND. A code dialled by an
extension that has no profile with a target is refused with 404 and changes
nothing. The defaults are added by migration 00052 to existing installs when
the codes are free; change them under System, Feature codes.

The specification named `*74` and `*75`, which are the default park and park
retrieve codes, so the defaults are `*76` and `*77`. Every code is checked
against the rest of the dial space when it is saved.

## The call record

A call that rang a follow-me profile has one line per leg in its routing trace
("Follow-me: external 0475123456 -> declined") and one row per leg in
`cdr_legs` with the role `follow_me`. The call's detail
(`GET /api/v1/cdrs/{id}`) returns them as `followMeLegs` (kind, extension,
trunk, result), and the call page shows them under **Follow-me**. A result is
`answered`, `declined` (an external target did not press 1), `busy`,
`no_answer`, `failed`, `skipped` or `cancelled` (another leg answered). When an
external leg answers, the call's trunk and direction are that leg's.

`hello_follow_me_legs_total{kind,result}` counts the legs, with `kind` one of
`own`, `extension`, `ring_group` and `external`.

## Two nodes

The profile is part of the configuration snapshot every hello-sip node loads,
so both nodes ring the same legs for a call that reaches either. A change by
the API or by `*76` and `*77` is written to PostgreSQL and reaches both nodes
with the next snapshot. A call is replicated from the moment it is answered,
whoever answered, exactly like any other call: if the node that holds it dies,
the other node takes it over, including a call an external target answered
(the carrier is re-INVITEd to the new node's relay). A call that is still
ringing, or an external target still being asked to press 1, is not replicated;
the caller's phone retries as for any ringing call. Trunk slots taken for
external legs are shared between the nodes like any outbound call's, and a slot
of a leg that did not answer is returned as soon as the ring ends.

## Needs a real carrier

The press-1 check is tested against a scripted carrier. How a real mobile
network and its voicemail behave, and the timing of the digit over a real
carrier's RFC 2833, are checked in the lab (see the lab list of the
specification).
