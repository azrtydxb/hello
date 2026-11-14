# Microsoft Teams Direct Routing with Hello

Hello does not speak to Microsoft Teams itself. Teams Direct Routing is
supported by Microsoft only through a **certified session border controller
(SBC)**, or through a Direct-Routing-as-a-Service provider that runs one for
you, and Hello is not certified. This page pairs Hello with such an SBC: the
SBC faces Microsoft, Hello sees the SBC as one more SIP peer, and the two
systems reach each other's numbers through it. Hello's side is a trunk with
the **SBC peer** profile.

Nothing here is built, bought or certified for you. It needs things only you
can supply: a Microsoft 365 tenant with Teams Phone licences, a verified
domain, a certified SBC (or a DRaaS contract) and a public name and
certificate for it. Cost is not estimated here. The steps that touch Microsoft
or the SBC are written from Microsoft's public Direct Routing documentation;
check each against the current pages before you rely on it, and run the
[checks at the end](#checks) on your tenant.

## What runs where

```
Teams user ── Microsoft Phone System ──SIP/TLS 5061, SRTP── SBC ──SIP/UDP, RTP── Hello ── phones, carriers
              sip.pstnhub.microsoft.com                    (public FQDN,            (SBC peer trunk,
              sip2.  sip3.                                  certificate)              source CIDR = the SBC)
```

| Part  | Does                                                                                                                                                                                |
| ----- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Teams | Holds the users and their phone numbers, the voice routing policy and the SBC's registration (`Set-CsOnlinePSTNGateway`). Sends and takes calls through the SBC only.               |
| SBC   | The only system Microsoft talks to. Presents the public certificate, terminates SIP/TLS and SRTP towards Microsoft, transcodes if Teams offers SILK, and anchors Microsoft's media. |
| Hello | Peers with the SBC on the LAN or a DMZ as an IP-authenticated trunk. Routes calls between its extensions, carriers and the Teams users.                                             |

Microsoft does not support media re-targeting during an active Direct Routing
call. Hello's call takeover after a node failure re-INVITEs only the
Hello-to-SBC leg, and the SBC keeps the Microsoft leg unchanged, so a takeover
does not break a call. Keep it that way: do not put Hello's media directly in
front of Microsoft.

## What you need

- A Microsoft 365 tenant with Teams Phone licences for the users, and a
  **verified domain** for the SBC's name (a subdomain of a domain in the
  tenant, for example `sbc.example.com`).
- A certified SBC or a DRaaS provider. Microsoft lists certified vendors
  (AudioCodes, Ribbon, Oracle, Cisco, anynode, TE-SYSTEMS, Mediatrix and
  others); the list and the firmware versions change.
- DNS and a certificate for the SBC's FQDN, trusted by Microsoft (a public
  CA). Microsoft's proxies are `sip.pstnhub.microsoft.com`,
  `sip2.pstnhub.microsoft.com` and `sip3.pstnhub.microsoft.com`; the SBC must
  accept SIP/TLS on port 5061 from them and be reachable on its public name.
- A LAN or DMZ path between the SBC and Hello's SIP address, UDP 5060 for
  signalling and Hello's RTP range for media (see [media.md](media.md)).

## Hello: the SBC peer trunk

Open **Trunks**, **New trunk**, and choose the profile **SBC peer (Teams
Direct Routing)**, or send `"profile": "sbc-peer"` to `POST /api/v1/trunks`.
`GET /api/v1/trunk-profiles` (and the MCP tool `listTrunkProfiles`) lists the
profile's defaults and limits. A new SBC peer takes these defaults for what
you leave unset, and anything you set wins within the limits:

| Field            | Default           | Why                                                                          |
| ---------------- | ----------------- | ---------------------------------------------------------------------------- |
| Mode             | `ip`              | The SBC is recognised by its address; it does not register.                  |
| Codecs           | G.722, PCMU, PCMA | Wideband first. Teams supports G.722 and G.711, and the SBC transcodes SILK. |
| Identity header  | `pai`             | An anonymous call asserts its identity in P-Asserted-Identity.               |
| SRTP             | `off`             | See [what the leg to the SBC is](#the-leg-between-hello-and-the-sbc).        |
| OPTIONS interval | 30 s              | Hello probes the SBC and shows the destination up or down.                   |

You must fill in:

- **Source CIDRs**: the SBC's signalling addresses on the Hello side (usually
  one `/32`). Hello refuses inbound calls from anywhere else with 403.
- **Destination**: the SBC's LAN or DMZ address and its SIP port.
- **Max calls** (optional): keep it at or below the SBC's licensed sessions
  and at or below `-MaxConcurrentSessions` on the Teams side.

The profile's limits are enforced when the trunk is saved (a 400 names each
field): mode `ip`, at least one source CIDR and one destination, only G.722,
PCMU and PCMA, and no required SRTP. Video is never offered to the trunk:
Hello's media is audio, and Teams Direct Routing calls through it are voice.
Within a few seconds the trunk shows its destination `up` (OPTIONS health).

### The leg between Hello and the SBC

Hello's trunk legs are SIP over UDP with RTP today. Hello has no TLS or SRTP
transport towards a trunk, so the SBC peer trunk cannot ask for it, and the
profile refuses `srtp: required`. Microsoft's side is encrypted: the SBC
terminates SIP/TLS and SRTP towards Microsoft, which is what Microsoft
requires. The leg to Hello is therefore plain on your own network, so put
the SBC on the LAN or a DMZ next to Hello, firewall it to the two addresses
and the media range, and treat it like any internal trunk. If your policy
needs that leg encrypted too, that is a separate feature (a TLS trunk
transport) that Hello does not have yet.

## Hello: number plan and routing

Use E.164 numbers (`+` and country code) between Hello and Teams. Hello's
routes normalise numbers; see [trunks.md](trunks.md).

**Teams calls Hello extensions and outside numbers.** Teams sends the dialled
number to the SBC, which sends it to Hello on the trunk.

- An **inbound route** from the SBC trunk maps a DID to an extension, ring
  group or IVR, as for a carrier.
- For Teams users to dial a Hello **extension by its short number**, set the
  trunk's **Internal dialing** to `patterns` and list the extension ranges the
  Teams side may dial (for example `10XX`), or give the extensions DIDs and
  route those. Do not use `all` unless you mean it; it opens every extension
  to the trunk and needs a confirmation.
- A trunk reaches only what its inbound routes and internal dialing allow
  (internal dialing is `off` by default), so Teams users cannot call outside
  numbers through Hello's carriers unless you build that path on purpose; that
  is a toll-fraud decision.

**Hello calls Teams users.** Add an **outbound route** that matches the Teams
users' numbers (their DIDs, or an internal prefix you assign) and use the SBC
trunk as its only trunk. If you give Teams users numbers in an internal range (say
`3XXX`), a route with a prefix match `3` and a number transform to the user's
E.164 number sends the call across. Test it first in **Routes, Test**: the
trace shows the rewrite and the trunk chosen.

Caller ID: set the extension's external number so Teams shows a number the
user recognises; the SBC trunk's default caller ID covers calls without one.
Teams matches an incoming caller to a user by E.164 number.

## Teams: the Microsoft side

These are the usual steps; confirm each against Microsoft's Direct Routing
documentation, since cmdlets and requirements change.

1. Add the SBC's domain to the tenant and verify it, and make sure at least
   one user in the tenant carries that domain (Microsoft requires an
   activated domain name on a user before it accepts the SBC).
2. Register the SBC:
   `New-CsOnlinePSTNGateway -Fqdn sbc.example.com -SipSignalingPort 5061 -MaxConcurrentSessions <n> -Enabled $true`.
3. Create a PSTN usage and a **voice route** that matches the numbers Teams
   should send to Hello (its DIDs and extension ranges) and points at the
   SBC, then a **voice routing policy** with that usage, and grant it to the
   users.
4. Give each user a Direct Routing number:
   `Set-CsPhoneNumberAssignment -Identity user@example.com -PhoneNumber +97145551234 -PhoneNumberType DirectRouting`,
   and enable them for Enterprise Voice.
5. Media bypass is optional and uses ICE on the Teams client and ICE Lite on
   the SBC; leave it off unless the SBC vendor documents it, and never make
   Hello part of it.

## Emergency calls

Responsibility is split, and it is yours to test:

- A call from a Hello extension to an emergency number follows Hello's own
  emergency routes and trunks ([trunks.md](trunks.md), locations), not the
  Teams path.
- A Teams user's emergency call is handled by Teams (emergency policies,
  dynamic emergency calling) and the SBC. Hello does not see it unless your
  voice routes send an emergency number to Hello, which you should not do.
- Test both, from both sides, before go-live. The Hello PBX adds no
  guarantees to Teams' emergency handling.

## What Hello does not do

Presence sync, chat, meeting join, call-queue or auto-attendant interworking
with Teams, and Teams features beyond voice calls are not provided; calls
between the systems are ordinary PSTN-style calls. Hello emits no
Diversion or History-Info for forwarded calls, so the SBC sees the call as a
fresh one from the forwarding extension. Operator Connect, Teams Phone
Mobile and SIP Gateway do not fit a PBX like Hello: Operator Connect replaces
the carrier, Teams Phone Mobile is a mobile-operator service, and SIP Gateway
turns a desk phone into a Teams endpoint, after which it no longer registers
with Hello.

## Support boundary

- Microsoft supports Direct Routing only with certified SBCs and may reject a
  case on a non-certified device. Hello is not certified and no certification
  is attempted.
- Calls through the SBC are supported by the SBC vendor on the Microsoft side;
  the Hello trunk is supported as any Hello trunk. Where a call fails, the
  Hello CDR trace shows whether it reached Hello, and the SBC's own logs show
  the Microsoft leg.

## Checks

Hello's own checks (the profile, its defaults and limits, number
normalisation on the trunk) are tested without a tenant. A conformance run
against Microsoft needs a tenant and an SBC, so it is not part of Hello's test
suite: before go-live, place calls in each direction (Teams to an extension,
Teams to an outside number, an extension to a Teams user, hold, transfer, DTMF
into an IVR) and confirm the trunk shows `up`, the SBC shows the Microsoft
proxies, and the CDRs record the trunk.
