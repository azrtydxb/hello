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

## 5. The manual check

1. Call an external mobile from your extension. It rings, answers, and both
   sides hear audio.
2. Call your DID from that mobile. Your extension rings.
3. Open **Call History** and select each call. The trace shows the route,
   the rewrite, the trunk and the carrier's answer, and the record shows the
   direction and the original and rewritten numbers.

If a call fails, its detail page ends with a one-line explanation, and the
trace shows what each trunk answered.
