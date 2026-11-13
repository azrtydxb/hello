# Locations, emergency calls and the alarm call

Hello knows where phones are, so an emergency call can say where it comes from
and a phone can ask Hello for its own location. It also answers `*55`, the
alarm call. This page covers the model, the HELD server, the location sent with
an emergency call, the callback and the notice to a ring group, the alarm call,
the vendor keys Hello writes and what only a phone or a carrier can confirm.

## Locations

A location is a **site** (the civic address, and a geo point if you know it), a
**floor** of a site or a **room** of a floor (or of a site). Floors and rooms
only name a place inside their site: the floor's name is the civic floor (`FLR`)
and the room's the civic room (`ROOM`). A floor or room may carry a more exact
geo point than its site; the most specific one wins.

Add them on the **Locations** page (Directory) or with `createLocation`. A site
needs a civic address (country as a two-letter code, state, city, street, house
number, postal code) or a geo point. Names are unique inside their parent.
Deleting a location with places inside it, or one that something is assigned to,
is refused, and the message names what is in the way.

A location is **assigned** to a phone group, an extension or a single phone
(Locations, tab Assignments, or `putLocationAssignment`). A phone gets the most
specific: its own, else its extension's, else its phone group's. The phone
detail API (`getPhoneLocation`) shows what a phone resolves to and which
assignment decided.

## E911 and the HELD server

A WP8x6 (WP816, WP826, WP836) asks Hello for its location with HELD (RFC 5985)
when its phone group has **E911** on (Locations, tab Emergency, or
`putPhoneGroupEmergency`). Hello then writes these keys into the phone's
configuration. Each is from Grandstream's `wp8x6_config_1.0.3.39` template
(config-template-10212026) and the WP8x6 Administration Guide, "External
Service, E911 Service":

| Key      | Value                                   | Meaning (Grandstream)                                       |
| -------- | --------------------------------------- | ----------------------------------------------------------- |
| `P8565`  | `1`                                     | Enable E911                                                 |
| `P8566`  | `1`                                     | HELD protocol: HTTPS                                        |
| `P8567`  | `60`                                    | HELD synchronization interval in minutes (0, or 30 to 1440) |
| `P8568`  | `<host>/p/<token>/held`                 | Location Server (the primary LIS address)                   |
| `P8597`  | the emergency numbers, separated by `;` | E911 Emergency Numbers                                      |
| `P8725`  | `1`                                     | Geolocation Header Format: RFC 6442                         |
| `P26059` | `1`                                     | Use the P-Emergency-Info header                             |

The emergency numbers are those of the emergency-flagged outbound routes, as for
`P8385` (Emergency, written on every WP8x6 whether or not E911 is on).

The server is `POST /p/<token>/held` on the provisioning listener. The token in
the URL is the only identity: it needs HTTPS and an enabled phone, like every
provisioning request, and anything else is an empty 404. The request body never
chooses the location; a phone cannot ask for another phone's. The answer is a
`locationResponse` (`application/held+xml`) with a PIDF-LO of the civic address
and the geo point. A phone with no location, or a request for a type Hello does
not have (`exact="true"` for geodetic with only a civic address), gets a HELD
`error` document (`locationUnavailable`) with status 200, and a malformed request
a `requestError`. Hello answers civic and geodetic locations only, never a
location URI.

Not written, because no document in hand fixes the value: `P8574` (HELD location
types), `P8576` (HELD NAI) and the HELD identities `P8577` to `P8596`. The phone's
defaults stand.

## The location on an emergency call

A trunk has a **geolocation** flag (off by default; Trunks page, or
`updateTrunk`). An emergency call placed over an emergency-flagged outbound route
and sent through a flagged trunk carries:

- a `Geolocation: <cid:...>` header and a PIDF-LO body (`application/pidf+xml`,
  RFC 4119 and RFC 6442) in a `multipart/mixed` body with the SDP;
- the location the phone sent with its own INVITE, when it sent a well-formed
  PIDF-LO (any other part is dropped and a DOCTYPE is refused), else the location
  assigned to the calling phone;
- a location by reference (a URI in the phone's `Geolocation` header) is passed
  on as it came, and the phone's `Geolocation-Routing` and `P-Emergency-Info`
  headers are passed on.

A trunk that is not flagged gets none of this, and neither does any call that is
not an emergency call. When there is no location to send the trace says so and
the call goes ahead.

The request is larger than the 1300 bytes SIP advises for UDP. Hello raises the
datagram limit it sends (to 4000 bytes) so the request leaves; a carrier on UDP
must take such a datagram, which is IP-fragmented. Flag a trunk only for a
carrier that accepts a location by value over its transport.

## What an emergency call does

- An emergency-flagged route ignores the trunk's call limit and a trunk known to
  be down. The caller's DND and forwarding act on calls to a phone, not on a call
  out to a trunk, so they never apply to it.
- The calling extension is marked for **30 minutes**. A call from a trunk to that
  extension in that time rings with the ring tag `emergency-callback` and ignores
  its DND (the mark is shared by all SIP nodes). The call's CDR kind stays `call`;
  its trace and `ringTag` show the callback.
- The **notify group** (Locations, tab Emergency, or `putEmergencySettings`) is a
  ring group whose phones are paged, one way, with a spoken notice, "Emergency
  call from <name>, extension <digits>", in the deployment's prompt language
  (a tone when speech is off). The phones are paged as for a paging group, so a
  phone in DND hears it too; Hello hangs up after the notice. A phone of the
  calling extension is not paged.

A door extension's target list does not exist yet; when it lands it must not
block an emergency route (spec S-28 a).

## The alarm call (`*55`)

A phone group may have an **alarm destination**: an extension or a ring group
(Locations, tab Emergency, or `putPhoneGroupEmergency`). Dialling `*55` from a
phone of that group:

- rings the destination whatever its DND, with the ring tag `alarm`; a ring group
  rings its DND members too;
- is a CDR of kind `alarm` (History shows it; the destination is the rewritten
  destination);
- plays "Panic alarm from <name>" to the answering party only (a tone when speech
  is off), and then connects the two;
- is answered `404` when the group has no destination or the destination is
  gone, and the CDR is still of kind `alarm`.

`*55` is reserved: a feature code or paging code that would collide with it is
refused. On a WP8x6 with a destination Hello also writes the phone's own alarm
keys, from the same template: `P22269 = 1` (Emergency Call), `P22270 = *55`
(Emergency Dial Number), `P22601 = 1` (call way: simultaneous) and `P22602 = 1`
(auto answer calls from the emergency number). Without a destination none of the
four is written. Setting the side key to alarm is a setting override.

Metrics: `hello_emergency_total{kind,result}` counts emergency calls, callbacks,
alarm calls and notices.

## What needs a phone or a carrier

- The WP8x6 HELD client (firmware 1.0.3.20 and later per the release notes): the
  format of **Location Server** (`P8568`). Grandstream's documents say "address"
  and keep the protocol in `P8566`, so Hello writes `host[:port]/p/<token>/held`
  without a scheme, as for `P331`. If a phone wants a scheme or a different
  path, the template changes.
- Whether the phone's `Geolocation` header (with `P8725 = 1`) is a `cid:` with a
  PIDF-LO body, a location URI, or neither, and that its INVITE arrives
  intact.
- That a carrier accepts a multipart INVITE with a PIDF-LO over its transport and
  routes on it, and what it expects in `Geolocation-Routing`.
- That a WP8x6 dials `*55` from its alarm key and auto-answers the callback.
- The spoken notices need a text-to-speech voice (Voice settings); without one
  they play a tone.
