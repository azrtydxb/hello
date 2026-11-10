# Connect Hello to a SIP trunk

This guide adds a carrier trunk, routes calls through it in both directions,
and runs the manual check that proves a real trunk works. It assumes a running
lab ([phones.md](phones.md)) and a phone registered on an extension.

## 1. Collect the carrier's details

| You need                        | Registration trunk | IP-authenticated trunk |
| ------------------------------- | ------------------ | ---------------------- |
| SIP proxy host and port         | yes                | yes                    |
| Username and password           | yes                | no                     |
| Realm (if the carrier names it) | often              | no                     |
| The IPs the carrier sends from  | optional           | yes                    |
| Your DID(s)                     | yes                | yes                    |

Hello signals over UDP. In this phase, media flows directly between the phone
and the carrier, so the carrier must be able to reach the phone's RTP address.
Phones behind NAT may get one-way audio with some carriers until media
anchoring arrives in Phase 5.

## 2. Add the trunk

In the UI, open **Trunks** and create the trunk:

- **Mode:** `registration` if the carrier gave you a username and password,
  otherwise `ip`.
- **Destinations:** the carrier's proxy. A hostname without a port is resolved
  through DNS SRV.
- **Source CIDRs:** the carrier's signalling IPs. Hello rejects inbound calls
  from any other address with 403.
- **Password:** entered once. Hello encrypts it with `HELLO_SECRET_KEY` and
  never shows it again; the trunk only reports whether one is set.

Within a few seconds the trunk shows `registered` (registration trunks) and its
destinations `up` (OPTIONS health). Exactly one SIP node registers the trunk;
if that node stops, another takes over within about half a minute.

## 3. Route outbound calls

Open **Routes → Outbound** and add a route. For example, a UAE mobile route
matching `^05[0-9]{8}$`, with the number transform regex `^0([0-9]+)$` and
template `+971${1}`, through `carrier-primary` then `carrier-backup`.

Before trying it, open **Routes → Test**, enter an extension and a number, and
read the trace. It shows every step a real call would take: the internal lookup,
the matched route, the rewrite, each trunk and why it would or would not be
used.

Set your extension's **external number** under **Extensions** so callees see
your DID rather than the extension number.

## 4. Route inbound calls

Open **Routes → Inbound** and map your DID (`exact`, for example `+97145551234`)
from the trunk to an extension.

## 5. Let a trunk dial internal numbers

A trunk peer can reach your extensions by number: an inbound call whose
dialled number is an extension rings it, without a DID map for every phone.
What each trunk may dial is its own decision, made deliberately:

- Open the trunk and set **Internal dialing** to `patterns`, then list what
  the trunk may dial — an exact number (`1012`), an `X` pattern where each
  `X` stands for any digit (`1XX` is every 100-block, `10XX` is 1000–1099),
  or an anchored Go regex (`^101[2-9]$`). Patterns are checked top to
  bottom, and the first match wins; the call's trace names the pattern that
  let it through.
- `all` opens every extension to the trunk. Because that is a toll-fraud
  surface, the API and the form require a `confirm: true` beside it: the
  trunk is not opened to every extension by a stray default in a client.
- The default is `off`: an existing trunk gains no reach by an upgrade.

A route with the **Internal** destination serves every extension: it looks
the dialled number up in the extension list and rings what it finds, so one
route (`DID: any`) covers the whole namespace. The policy is checked before
the routes; a DID that matches no pattern skips the internal route (a traced
skip, visible in Call History), and if no other route takes the call the
peer hears **404** — a reject is a reject, never a silent failover.

What the phone shows: the caller ID as the peer sent it, else the trunk's
default caller ID, else the trunk's name in angle brackets (`<carrier-x>`),
so a trunk-to-internal call is always attributable. The route's caller-ID
transform applies to whatever the ladder produced, as with any call.

Loop guards, because a trunk is untrusted routing input: a call that came
from a trunk never leaves through that same trunk (it is removed from the
candidates, and the trace says "call came from it"), and a call is routed
outbound from a trunk at most once — a second attempt is rejected 403
"loop prevented". Feature codes and every destination kind other than
internal stay out of reach for trunk peers; this gate covers extensions
only.

Before going live, prove the reach in **Routes → Test** with `from:
trunk:<id>`: the trace shows the policy verdict and the internal lookup
exactly as a real call would see them.

## 6. Turn unwanted callers away

A trunk can screen its inbound calls before any route or phone: reject
anonymous callers, allow only callers from listed countries (**Allowed country
codes** in the trunk form), and answer a screened call with `603 Decline` or
`486 Busy Here`. Numbers you block or always allow are on **Call flow →
Blocklist**. See [blocklist.md](blocklist.md) for the order of checks, the CSV
import and the limits. Nothing is on by default.

## 7. The manual check

1. Call an external mobile from your extension. It rings, answers, and both
   sides hear audio.
2. Call your DID from that mobile. Your extension rings.
3. Open **Call History** and select each call. The trace shows the route,
   the rewrite, the trunk and the carrier's answer, and the record shows the
   direction and the original and rewritten numbers.

If a call fails, its detail page ends with a one-line explanation, and the
trace shows what each trunk answered.
