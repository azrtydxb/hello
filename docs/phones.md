# Connect two SIP phones to the lab

This guide takes you from a running lab to two phones calling each other: one
desk phone or softphone per extension, registered with Hello over SIP/UDP. It
takes about ten minutes.

Have more than a couple of phones, or phones that support auto-provisioning
(Yealink, Poly, Grandstream, Snom, Fanvil)? Prefer
[automatic provisioning](provisioning.md): assign each phone's MAC to an
extension and let it configure itself. This guide is the manual path.

You need the lab host's LAN IP address (here `192.168.1.10`) and two SIP
phones or softphones on the same network that can reach it.

## 1. Start the lab with a reachable SIP address

Each SIP node writes its advertised address into every SIP message, and phones
send their replies there. The defaults (`hello-sip-1:5060`) only resolve
inside Docker, so advertise the host's LAN IP instead:

```sh
HELLO_SIP1_ADVERTISED=192.168.1.10:5060 \
HELLO_SIP2_ADVERTISED=192.168.1.10:5062 \
docker compose -f deploy/docker-compose/compose.yaml up -d --build --wait
```

hello-sip-1 answers on UDP port 5060 and hello-sip-2 on UDP port 5062.

## 2. Create two extensions and their devices

Open the UI at `http://192.168.1.10:8080` and sign in as `admin` with the lab
password `hello-lab-admin`. Then:

1. Under **Extensions**, create `101` (Alice) and `102` (Bob).
2. Under **Devices**, create a device for extension 101 with SIP username
   `alice-desk`. Copy the secret from the dialog straight away: it is shown
   once, and only rotating it shows a new one.
3. Do the same for extension 102 with SIP username `bob-desk`.

The same steps work through the API; see `/api/v1/openapi.json`.

## 3. Configure each phone

| Phone setting                | Value                                               |
| ---------------------------- | --------------------------------------------------- |
| SIP server / registrar       | `192.168.1.10`                                      |
| Port                         | `5060` (or `5062` to use the second node)           |
| Transport                    | UDP                                                 |
| SIP domain / realm           | `hello.lab`                                         |
| User ID / username / auth ID | the device's SIP username, e.g. `alice-desk`        |
| Password                     | the device secret                                   |
| Outbound proxy               | none, or the same address and port as the registrar |
| Registration expiry          | 60–3600 seconds                                     |

Point the two phones at different ports to see a call cross both SIP nodes.

## 4. Check and call

The UI's **Registrations** page lists both phones within a few seconds, with
the node each registered through. Dial `102` from Alice's phone: Bob's phone
rings, and **Active Calls** shows the call while it is up. After you hang up,
**Call History** shows the call record.

## When it does not work

- **The phone shows "registration failed" or 403.** Check the username, secret
  and domain `hello.lab`. Ten failed attempts from one IP within five minutes
  lock that IP out for the rest of the window.
- **The phone registers, but calls never ring or drop after about 30 seconds.**
  The advertised address is not reachable from the phone. Restart the lab with
  the `HELLO_SIP*_ADVERTISED` values from step 1.
- **No audio.** Phase 1 does not relay media. The two phones send RTP directly
  to each other, so they must be able to reach each other's IP.

## Roaming, keep-alive and dead calls

A Wi-Fi handset that drops off the network mid-call, goes to sleep behind NAT
or moves to another access point must not leave a call hung or a contact
ringing a dead address.

- **Keep-alive.** A double CRLF ping (RFC 5626) is answered with a single CRLF
  over TCP and TLS, and ignored without an error log over UDP. That is the SIP
  transport library's behaviour, pinned by a test; hello-sip listens on UDP
  today, and the TCP and TLS listeners (phone-feature-parity S-13, S-14) inherit
  it. The Grandstream
  template turns on the phone's own keep-alive: P2397 = 1 (SIP OPTIONS) on
  WP8x6 and P84989 = 0 (RFC 5626 off) on WP822 and WP8xx, whose OPTIONS and
  registration refresh already hold the NAT flow. The OPTIONS interval and
  max-tries keys are left at the phone's defaults until the lab confirms them.
- **Session timers (RFC 4028).** Hello honours a phone's `Session-Expires`
  (with the refresher it names) and `Min-SE`: a request below `Min-SE` gets
  `422` with Min-SE. On a call from a phone that sent none, Hello adds
  `Session-Expires: 1800;refresher=uas` to the 200 and refreshes the call
  itself. On the leg to a phone of a vendor that supports it
  (Grandstream, Yealink, Poly, Snom, Fanvil; anything Hello cannot identify
  gets no request) the INVITE carries `Supported: timer`,
  `Session-Expires: 1800;refresher=uac` and `Min-SE: 90`. Where the phone
  does not take over the refresher role, Hello refreshes at half the interval
  with an UPDATE, or a re-INVITE repeating the leg's last SDP when the phone
  does not list UPDATE. A refresh nobody answers (8 s) or that is refused
  (anything but a retry after `422`, `491`, or `405`/`501` to an UPDATE) ends
  the call: both legs get BYE, the CDR side is `system` with the reason
  `session timer: refresh failed`. When the phone is the refresher and no
  refresh arrives shortly before the interval ends, the call ends the same way
  (`refresh missing`). A refresh from a phone restarts that leg's timer; a
  bodiless UPDATE that only refreshes is answered by Hello, not relayed.
  Tune with `HELLO_SIP_SESSION_EXPIRES_SECONDS` (`0` turns the feature off)
  and `HELLO_SIP_SESSION_MIN_SE_SECONDS`. A parked, picked-up or transferred
  call keeps its timers on the dialogs it moved to: the party left on a
  parked call is refreshed (or must refresh) as before, and the phone that
  retrieves or picks the call up, the parker a returned call rings, and the
  target of a transfer each get a timer of their own. Legs to trunks follow
  the trunk's session-timers setting: `on` requests `Session-Expires` on its
  calls and keeps a timer on calls from it, `off` never does, and `default`
  follows `HELLO_SIP_SESSION_TIMERS_TRUNKS` (false unless set); an inbound
  trunk call is covered by `on` even when the carrier sent no
  `Session-Expires`. `HELLO_SIP_MAX_CALL_DURATION` is still the backstop.
  IVR menus and queues time the caller (Hello refreshes; a caller that must
  refresh gets no timer there) and the destination once connected. After a node
  takeover the timers resume with the negotiated interval and refresher.
- **Dead media.** An anchored call with no RTP either way for
  `HELLO_MEDIA_DEAD_SECONDS` (60) is ended unless it is on hold
  ([media.md](media.md#roaming-and-dead-media)).
- **Roam.** A re-INVITE with a new media address is relayed and the anchor
  re-learns it. A re-REGISTER from a new address replaces the old contact of
  the same phone: the same `+sip.instance`, or else the same Call-ID (a
  contact the request itself registers or removes is left as it is), so a
  phone that got a new IP does not keep a stale contact until it expires.

Lab checks that need hardware (WP822 and WP8x6): a mid-call AP roam on the same
subnet, a roam across subnets, a handset idle for more than an hour behind NAT,
the P2397 and P84989 keys on an exported XML, and that the phones accept
`Session-Expires` on a call and UPDATE as a refresh.

