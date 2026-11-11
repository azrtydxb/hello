# Door stations and the Door group

A door station (a Grandstream GDS37xx, 2N, Akuvox or DoorBird unit) registers with
Hello like a phone, with its own SIP credentials. Hello does not provision the unit;
configure its SIP account and set its call button to dial the Door group's number.

## Door station extension

Create an extension and turn on **Door station** (Extensions page), or send
`"kind": "door"` to `POST /api/v1/extensions`. A door station:

- has devices like any extension;
- has no voicemail box, takes no pickup, page, intercom (`403`) or MESSAGE (`403`, both
  directions), and cannot be a member of a ring group, paging group, pickup group or
  queue;
- is hidden from the extensions phonebook unless you set `directoryHidden` to false;
- can call only its **targets**: the extensions and ring groups ticked on it
  (`doorTargets`: `{"extensions":[id],"ringGroups":[id]}`, at most 50), and every ring
  group marked Door. Any other destination, an outside number, a feature code or a page
  included, is answered `403`;
- can never reach a trunk, even through a callee's forward-always, a group's external
  fallback, an IVR or a transfer, except an emergency route;
- makes calls that ring with the `doorbell` ring tag and have CDR kind `door`.

An extension that is a member of a group or queue cannot become a door station (`400`
on `kind`); remove it from the group first. Turning a user into a station switches
voicemail and intercom off. Turning it back brings its voicemail box back (off until you
enable it).

## Door group

Mark a ring group **Door group** (`"isDoor": true`). Any strategy works. A new Door group
with no ring tag gets `doorbell`. Calls to it, from a station or from anyone, have CDR
kind `door` and ring the members with the doorbell tone (see [ring tags](ring-tags.md)).
Door stations can always call Door groups.

## Door-open code

A door station may hold a door-open code of 1 to 8 characters of `0-9`, `*` and `#`
(Extensions, the station's drawer, administrators; or `PUT
/api/v1/extensions/{id}/door-open` with `{"code": "..."}`; an empty string clears it). The
code is stored sealed, is never returned, logged, traced or written to the audit log, and
the API says only whether one is set (`GET`).

During an answered call with the station, the user on the far side opens the door in
either of two ways:

- dial `*50` on the phone: Hello sends the code to the station as RFC 2833 events;
- press **Open door** under **My phone** (`POST /api/v1/calls/{id}/door-open`). Only the
  user whose extension answered the call, or placed it to the station, may; anyone else
  gets `403`. The id is the call's correlation id from `GET /api/v1/me/door-calls`.

Door calls are always anchored, so Hello can put the events on the station's leg. The
keys `*`, `5`, `0` the user dialled are still relayed to the station as ordinary DTMF
before the code. Keys dialled by the station itself never open its door. On an ordinary
call `*50` does nothing. The call-flow trace shows "Door open pressed" and "Door open code
sent", never the digits. If the station negotiated no `telephone-event`, the trace says so
and nothing is sent.

Turn on **Hang up after opening** and Hello ends both legs `HELLO_DOOR_HANGUP_SECONDS`
(default 3) after the code was sent. The callee can also key the station's own code; that
is relayed like any DTMF.

Provisioning an "Open door" key on the phones (Yealink DSS DTMF key, Grandstream WP
DialDTMF softkey, and so on) is not done yet: the template keys must first be confirmed
on hardware.
## Doorbell webhook

A doorbell that cannot register as a SIP phone (a camera doorbell, a smart button, a
home-automation rule) posts to a webhook instead. Create it under **Door** (admins), or
with `POST /api/v1/door/webhooks`:

- **name**: the caller name the phones show;
- **action** `ring_door`: Hello originates a call from a virtual caller named after the
  webhook to the members of the Door group (every Door group unless one is named), with
  the `doorbell` ring tag. The first member to answer hears the chime and the call ends
  after it, or after 30 s; the other phones stop ringing. Members in do-not-disturb
  (unless the group ignores it) or in a call are skipped;
- **action** `chime_page`: a one-way page to a paging group (auto-answer, as for a
  server page) that plays the chime and ends;
- **chime**: an announcement, or the built-in chime;
- **rate limit**: triggers a minute (default 11).

The secret is returned once, on create and on `POST .../rotate-secret`, and stored sealed.

The doorbell posts `POST /api/v1/door/webhooks/{id}/trigger`, with no session:

```
X-Hello-Timestamp: <Unix seconds, within 300 s of now>
X-Hello-Signature: <hex HMAC-SHA256(secret, timestamp + "." + body)>
```

The body is optional JSON of at most 1 KiB; a `label` in it shows in the call trace.
Sign the exact bytes sent (an empty body signs `timestamp + "."`). Answers:

- `202` `{"status":"queued"}`, or `{"status":"coalesced"}` when the same webhook fired in
  the last 10 s and is already ringing, so a button pressed twice rings once;
- `401` for a missing, wrong or stale signature, an unknown id and a disabled webhook,
  all alike, so an id cannot be probed;
- `400` for a body that is not a JSON object, `413` over 1 KiB;
- `429` with `Retry-After` above the rate limit (counted per webhook, shared by every
  node);
- `503` when Valkey is down.

Triggers are audited as `webhook:<name>` without the body, and each ring or chime
writes a CDR with kind `door`.

## Not here yet

The door camera preview.
