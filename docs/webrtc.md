# Browser softphone: web devices, TURN and ICE servers

Hello can ring and place calls in a browser. This page covers what exists
today: a user's **web devices** (credentials for a browser softphone),
**coturn** (STUN and TURN for the media) and the **ICE servers endpoint** the
softphone calls. The WSS edge is described [below](#sip-over-wss-on-kamailio); the DTLS-SRTP
media path and the softphone itself are separate pieces, and this page says
where they plug in.

## Web devices

A web device is a device of the user's extension with an owner, a label and
credentials that are derived, not stored.

- A user adds one under **My phone, Web devices**, or with
  `POST /api/v1/me/devices` (`label`, optional `extensionId` of one of their
  own extensions, the first by default). The response carries the SIP
  username, the SIP password, the realm and the expiry, once.
- The SIP username is `w<deviceId>.<expiryUnix>` and the password is
  `base64url(HMAC-SHA256(HELLO_CLIENT_AUTH_KEY, "<username>:<realm>"))`.
  hello-sip computes the digest HA1 from the same key when the client
  registers, so nothing secret is in the database and a new browser can
  register the moment the API answers. A name past its expiry is a bad
  credential; the client refreshes with
  `POST /api/v1/me/devices/{id}/credentials`. Credentials live 12 hours.
- The binding is stored under the device's stable name (`w<deviceId>`), so a
  refreshed credential replaces the previous binding.
- `HELLO_CLIENT_AUTH_KEY` (at least 32 bytes, on hello-control and hello-sip)
  turns the feature on; without it the routes answer 503. To replace the key,
  set the new one as `HELLO_CLIENT_AUTH_KEY` and the old one as
  `HELLO_CLIENT_AUTH_KEY_PREVIOUS` on both, roll them, and drop the previous
  one after the longest credential life (12 hours). Registered clients keep
  working throughout.
- A user may have `HELLO_CLIENT_MAX_DEVICES` (5) active web devices; the next
  is `409 device_limit`. One AOR holds `HELLO_SIP_MAX_CONTACTS` (10) bindings.
- **Revoking** (`DELETE /api/v1/me/devices/{id}`) disables the device; the
  configuration revision moves, hello-sip drops the registrations and refuses
  the credentials within its snapshot refresh. A device also ends when its
  owner is disabled, deleted or unlinked from its extension.
- The routes need a **browser session** of a user. A token, an OAuth grant or
  a service account is refused (403), and no route takes a user id: another
  user's device is 404. They are not MCP tools. Credentials and ICE requests
  are limited to 60 a minute per user (429).
- An administrator sees every device with `GET /api/v1/devices?kind=web` and
  can disable or delete one; a web device cannot be moved or have its secret
  rotated (409 `derived_device`).
- **Ringing.** `ringWeb` and `ringMobile` on `PATCH /api/v1/me/profile` (both
  default on) choose whether a kind of client is forked to. Do not disturb,
  forwarding and schedules still apply first. The first branch to answer
  cancels the others with `Reason: SIP;cause=200;text="Call completed
elsewhere"`, so clients show "answered elsewhere".

## coturn

coturn gives a browser behind a NAT or firewall a path to the media. On a
LAN, calls normally connect directly (host candidates) and never touch it.

- Image: `docker/coturn/Dockerfile`, Debian trixie's `coturn` (native arm64),
  published by CI as `coturn:sha-<commit>` and `coturn:ct-<hash of
docker/coturn>`. Its entrypoint writes the configuration from the
  environment, so the shared secret comes from a Secret and is in no image or
  ConfigMap.
- Mode: `use-auth-secret` (the TURN REST API). Credentials are
  `"<expiry>:<name>"` with `base64(HMAC-SHA1(secret, username))`; coturn
  keeps no user database.
- Peers: every private, loopback and link-local range is denied; only
  `coturn.allowedPeers` (the hello-sip media addresses, single IPs or `a-b`
  ranges) can be relayed to, so TURN cannot reach the rest of the LAN. Also
  `no-tcp-relay`, `no-multicast-peers`, `no-cli`, `fingerprint`, per-credential
  and total allocation quotas, and TLS 1.2 minimum when TLS is on.
- Servers: one Deployment per address, one replica each. A TURN allocation
  lives on one server and the relayed media must come back to it, so replicas
  behind one address would break calls. Each is replaced old pod first.
- Ports: UDP and TCP 3478, TLS 5349 (optional), and the relay range
  49152-49999/UDP (800 allocations). With `coturn.hostNetwork: true` (the
  default) they are bound on the node, which is the server's `externalIP`;
  pin each server to its node with `servers[].nodeSelector`. Without
  hostNetwork each server gets a LoadBalancer Service owning its `externalIP`
  and listing every relay port (at most 200).
- Chart values: `coturn.enabled`, `coturn.servers`, `coturn.allowedPeers`,
  `coturn.tls`, `coturn.relayPortMin/Max`, `coturn.userQuota/totalQuota`. The
  credentials Secret gains `turnSecret` (at least 16 bytes; required with
  coturn on) next to `clientAuthKey` and `clientAuthKeyPrevious`.
- Rotating `turnSecret` needs a rolling restart of hello-control and the
  coturn pods together; credentials already handed out stop working at the
  restart (they live an hour).
- Not verified without a container runtime or a lab: the Prometheus exporter
  (`coturn.metrics.enabled`, off by default) depends on the Debian build, and
  a client behind a UDP-blocking NAT reaching TURN over TLS is a lab check.

## ICE servers

`GET /api/v1/me/ice-servers[?deviceId=]` returns what the softphone passes to
`RTCPeerConnection`:

```json
{
  "iceServers": [
    { "urls": ["stun:192.168.10.121:3478"] },
    {
      "urls": ["turn:192.168.10.121:3478?transport=udp", "turns:..."],
      "username": "1893456000:12",
      "credential": "..."
    }
  ],
  "expiresAt": "2030-01-01T00:00:00Z",
  "ttlSeconds": 3600
}
```

The username is the expiry and the device id; the credential expires with it.
Clients refetch before a call when the credential is older than half its
life. `HELLO_TURN_URLS` (comma separated `turn:`/`turns:` URIs; the chart
builds them from `coturn.servers`), `HELLO_STUN_URLS` (default: the plain
`turn:` hosts as `stun:`), `HELLO_TURN_SECRET` and `HELLO_TURN_TTL` (default
1h) configure it. With no server configured it answers 503. Issuing is
counted in `hello_ice_credentials_issued_total`, not logged per credential.

## SIP over WSS on Kamailio

Browsers register and call over SIP on a WebSocket (RFC 7118). Kamailio, the
SIP edge, ends the WebSocket and TLS and talks plain UDP to the hello-sip
nodes, as for any phone (or mutual TLS with the
[TLS hop](#tls-hop-from-kamailio-to-hello-sip) on): hello-sip has no
WebSocket code. A browser is
therefore a phone that registers through the edge, with the credentials of a
[web device](#web-devices). Off by default.

- **Address.** `wss://<console host>/sip`, sub-protocol `sip`. The console's
  Ingress forwards `/sip` to Kamailio's WSS listener, so the browser reuses
  the console's certificate and origin: no second DNS name and no CORS.
  `HELLO_WSS_URL` (what a new device is told) is derived from
  `ui.ingress.host` when `clients.wssUrl` is empty.
- **Image.** `docker/kamailio` installs `kamailio-websocket-modules`; the
  build checks that `websocket.so` and `tls.so` load. An image without them
  must run with `KAMAILIO_WSS_PORT` unset (the config loads nothing for WSS
  then).
- **Config.** `KAMAILIO_WSS_PORT` turns it on: a TLS listener named `wss` on
  the instance's own address (the certificate of SIP over TLS,
  `/etc/hello-tls`, TLS 1.2 minimum). The websocket module pings every
  connection every 10 s and closes one that has not answered in 30 s, so a
  dead tab frees its binding quickly; only the `sip` sub-protocol is
  accepted. HTTP on that socket answers one thing: the upgrade for `GET /sip`;
  everything else is 404, and the metrics socket stays separate.
- **Origins.** `KAMAILIO_WS_ORIGINS` is a comma-separated list of exact
  origins (`https://hello.kw.watteel.lab`). A handshake whose `Origin` is not
  on the list is refused with 403 (cross-site WebSocket hijacking). A client
  that sends no `Origin` is not a browser and may connect; it still has to
  authenticate with digest. Unset, no origin is allowed. The chart defaults the
  list to the origin of `global.publicUrl`.
- **Client address.** After the ingress, every browser shares its address.
  `KAMAILIO_WSS_TRUSTED_PROXIES` is a regular expression the ingress pod's
  address must match for its `X-Forwarded-For` to be believed; Kamailio then
  keeps that address for the connection and tells Hello in `X-Hello-Client`,
  so Hello's failed-authentication throttle counts per browser. Unset, it is
  never believed. Per-IP request limiting (pike) is not applied to WebSocket
  clients for the same reason; Hello's throttle and the per-AOR contact limit
  apply.
- **Contact, Path and Record-Route.** The browser's Contact host is a random
  `.invalid` name; Kamailio rewrites it with `set_contact_alias()` so Hello's
  requests reach the live connection. With the Kamailio pair, Path and
  Record-Route of a WebSocket client name that instance's own address
  (a `hello_ws` socket on UDP 5072), not the virtual address, because a
  WebSocket is one TCP connection pinned to the instance that accepted it. If
  the instance dies the socket closes, the client reconnects through the
  Ingress to the survivor and registers again; until then Hello forks to the
  user's other devices. nathelper's OPTIONS keep-alive is not sent to these
  contacts. With the TLS hop on, `hello_tls` (also this instance's own
  address) takes that role for WebSocket clients instead of `hello_ws`.
- **Transport.** The binding records `wss` (`transport` in
  `/api/v1/registrations`, `hello_sip_registrations_by_transport{transport="wss"}`).
  A `sips:` call may ring a `wss` binding (encrypted to the edge); a plain
  `ws` one does not count. Media encryption (SDES) is not offered to it: a
  browser negotiates DTLS-SRTP, which belongs to the media path.
- **Chart.** `kamailio.wss.enabled`, `port` (8088), `secretName` (default
  `kamailio.tls.secretName`), `origins`, `trustedProxies`, `ingress`,
  `ingressAnnotations`. Enabled, the chart adds the container port, the
  `hello-kamailio-ws` Service and, with `ui.ingress.enabled`, the Ingress for
  `/sip` (`backend-protocol: HTTPS`, one-hour read and send timeouts). The
  Kamailio pods are on the host network, so only the ingress controller
  should reach the port: restrict it with a host firewall or Cilium host
  policy on the nodes; the chart cannot do that for hostNetwork pods.
- **Lab.** The compose lab enables it (host TCP 5083 to 8088, origins
  `http://localhost:8081` and `https://localhost:8443`);
  `TestLabWSSRegister` registers through it, checks the `wss` transport and
  that a foreign origin is refused.
- **Not covered.** A browser call needs the media path (DTLS-SRTP and ICE) and
  the softphone, which are separate pieces. Kamailio-to-hello-sip stays UDP
  inside the edge network unless the TLS hop below is on; a DTLS-SRTP
  fingerprint must not travel over a cleartext hop in production.

## TLS hop from Kamailio to hello-sip

The signalling of a browser carries the fingerprint of its media (DTLS-SRTP),
which is only as trustworthy as the path that carries it. Browsers reach
Kamailio over WSS; the hop from Kamailio to hello-sip must be encrypted too
before a WebRTC call is accepted in production. It is off by default, and
with it off Kamailio and hello-sip behave exactly as before.

- **What uses it.** A request that arrives over an encrypted phone leg (`tls`,
  `ws` or `wss`) goes to the hello-sip node over mutual TLS. Hello's requests
  for that phone come back to a TLS socket of the same Kamailio instance,
  `hello_tls`, on its own address and `kamailio.helloTLS.port` (5073), which
  the Path and Record-Route name. Everything else (UDP and TCP phones, the
  dispatcher's OPTIONS probes) stays UDP on the `hello` socket.
- **Mutual.** hello-sip listens on `sip.tls.port` (5061) and requires a client
  certificate signed by the Hello CA (`HELLO_SIP_TLS_ADDR`, `_CERT`, `_KEY`,
  `_CA`; TLS 1.2 minimum). Kamailio presents its certificate and verifies the
  node's against the same CA (`tls-hop.cfg`, which is `tls.cfg` plus the CA, a
  client certificate and `verify_certificate = yes` for the connections
  Kamailio opens). Hello verifies Kamailio's certificate on its own
  connections. What arrives on `hello_tls` is still vetted by source address,
  like the UDP socket: only a dispatcher node's address is a Hello node.
- **Certificates.** One certificate of the Hello CA with its `ca.crt`, usable as
  a server and a client certificate (`serverAuth` and `clientAuth`), whose
  names include the addresses Hello dials (the Kamailio node addresses) and
  the pod DNS names of the hello-sip nodes. hello-sip re-reads it when the
  file changes, at most once a minute, so cert-manager renewals need no
  restart. Kamailio reads it at start.
- **Chart.** `sip.tls.enabled` and `sip.tls.secretName` (hello-sip), and
  `kamailio.helloTLS.enabled` (with `port`, `sipPort`), which needs
  `kamailio.tls.secretName` for Kamailio's own certificate. Both are off by
  default; the chart refuses one without the other's prerequisites. In
  Kamailio it is `KAMAILIO_HELLO_TLS_PORT` and `KAMAILIO_HELLO_SIP_TLS_PORT`.
  The kw manifests do not set them: turning the hop on there is a manifest
  change together with the certificate. With `kamailio.wss.enabled` too, WSS
  presents the same certificate (`tls-hop.cfg` serves one on every TLS
  socket), so `kamailio.wss.secretName` must be empty or equal to
  `kamailio.tls.secretName`; the chart refuses anything else.
- **Health.** The dispatcher holds a second set: the same nodes by their TLS
  address (`sip:<node>:5061;transport=tls`, `sockname=hello_tls`), probed with
  OPTIONS over TLS from the `hello_tls` socket like set 1 is over UDP. An
  encrypted phone leg is balanced over set 2, so a node whose TLS listener is
  down (or whose certificate no longer verifies) is marked down for those legs
  only, and its UDP entry stays up for everything else. The chart writes set 2
  into the dispatcher list when `kamailio.helloTLS.enabled`. A retry on
  another node and an in-dialog request aimed at a dead node stay in the same
  set (a message flag carries the choice into the failure routes); a request
  that follows a Route to a node (the node that owns the dialog) is converted
  to that node's TLS address without a health check.
- **Metrics.** hello-sip counts requests by the transport they arrived on,
  `hello_sip_requests_by_wire_total{transport="udp|tcp|tls"}`, and Kamailio
  counts those on the hop socket in `kamailio_edge_requests_total{socket="hello_tls"}`.
- **Cleartext refusal.** With the hop off, a WebRTC offer is refused unless
  `HELLO_MEDIA_WEBRTC_ALLOW_CLEARTEXT_HOP=true` (default false), because its
  DTLS fingerprint would travel in the clear between Kamailio and the node; see
  [media.md](media.md). The offer must have arrived on the node's TLS listener
  from a trusted edge. Hello's trunk legs and Hello to SBC connections are not
  part of this hop.
- **Lab.** The compose lab turns it on at both ends (the lab SIP certificate is
  both ends' certificate), so every TLS phone of the lab suite travels it:
  `TestLabSIPTLS` registers over TLS and the node answers on the hop, and
  `TestLabTLSHop` asserts it: the nodes count the REGISTERs as arrived over
  TLS, Kamailio counts Hello's INVITE to the phone on `hello_tls` and the
  phone's connection receives it, and stopping a node marks only its TLS
  destination down through the TLS probes.

## The web softphone

The console has a phone in it. A **dock** (bottom left of every page of a user
who has an extension) shows a status chip and opens into the dialer, the call
lines and their controls. It is off until the user switches **Use this browser
as a phone** on; that is stored for this browser profile only, so a second
browser or computer is switched on separately.

- **Switching on.** The browser asks for the microphone first, with its own
  explanation (a blocked or missing microphone is named, and nothing registers).
  The first time it enrols a [web device](#web-devices) for the browser
  (`POST /api/v1/me/devices`, labelled "Chrome on macOS" and so on); later it
  refreshes that device's credentials. It then fetches the
  [ICE servers](#ice-servers), opens `wss://<console host>/sip` and registers.
  The derived SIP password lives 12 hours; the dock refreshes it five minutes
  before it expires, between calls (the username carries the expiry, so a new
  credential is a new registration), and enrols again if the device was revoked.
- **One phone per browser.** The tab that holds a Web Lock is the phone. The
  other tabs show "Phone active in another tab" and a button to take it over,
  so one browser never registers twice or rings twice.
- **Incoming calls.** A toast shows the caller's number and name, the extension
  that was dialled, the ring tag and a video badge, with **Answer**, **Answer
  with video** and **Decline** (Alt+A answers, Alt+H hangs up, Alt+M mutes).
  The ringtone plays only once the user has interacted with the page (browser
  autoplay policy); with the tab hidden, a notification brings it forward. When
  another device answers, the banner says so ("Call completed elsewhere"); a
  caller who gives up leaves a missed call. A second call rings as call waiting.
- **In a call.** Mute, hold and resume, a keypad (RFC 4733 digits that the relay
  carries and detects, as for any phone), blind transfer to a number and attended
  transfer to another line, video on and off, a call-quality indicator, and a
  picker for the microphone, camera and speaker. A second call puts the first
  on hold; the lines are tabs.
- **Dialing.** A number or name box, with the user's own contacts and recent
  calls as suggestions. **Call** buttons on the Extensions and Phonebooks pages
  place the call from the softphone, or open the dock to ask when it is off.
- **Voicemail.** The dock subscribes to the extension's message-waiting state
  (RFC 3842) and shows the count, linking to My voicemail.
- **Failures are visible.** No or blocked microphone, the WebSocket closed
  (reconnecting with backoff and jitter), registration refused (the credentials
  are refreshed once, then it says so), a call whose media failed (and whether
  a relay server was available).
- **Call quality.** While a call is active the dock samples `getStats()` every
  five seconds; at the end it posts the worst loss, jitter and round trip, the
  codec, the candidate types of the selected pair and the video it received to
  `POST /api/v1/me/calls/{callId}/quality`. The call's detail page shows it
  under "Web client report". It is numbers only, never media.
- **Code.** `web/src/softphone/`. Everything that touches the SIP library is in
  `jssipEngine.ts` (JsSIP); the rest talks to the `PhoneEngine` interface in
  `types.ts`, so the library can change locally and a mobile client can reuse the
  state machine, quality and backoff code unchanged. `phoneState.ts` is a pure
  reducer with the unit tests.

### How a browser call goes through Hello

The call path treats a browser as a phone that registers through the edge. What
differs is the media leg, and Hello builds it per leg:

- **A browser calls.** An INVITE whose audio section is `UDP/TLS/RTP/SAVPF` with
  ICE is a browser's. The node must run WebRTC media
  (`HELLO_MEDIA_WEBRTC_ENABLED`) and the hop from the edge must be TLS unless
  `HELLO_MEDIA_WEBRTC_ALLOW_CLEARTEXT_HOP` is on (the DTLS fingerprint in the SDP
  is only as trustworthy as the path that carries it); otherwise the call is
  refused with 488 and the reason in the trace and the CDR. The caller's leg is
  made on the shared WebRTC port and answered with ICE-lite, the node's
  fingerprint and the codec the callee chose.
- **A browser is called.** A fork to a web or mobile device (or any contact that
  registered over WS or WSS) is offered the **alternate callee leg**: a second
  endpoint on the shared port, parked (its media is dropped) until a fork answers.
  Every browser of the ring is offered the same leg; a ring across a desk phone
  and a browser is one call with two kinds of fork. When a browser answers, its
  leg is promoted and the plain leg is closed; when a desk phone answers first,
  the alternate leg is dropped. The first to answer cancels the others as always.
- **Hold, resume, ICE restart.** A re-INVITE from a browser re-reads its sections
  (ids, ICE credentials) and is answered with the leg's own fingerprint,
  credentials and role, so the browser never re-handshakes. Every SDP written for
  a browser keeps its `m=` lines (a removed video line stays, rejected), which is
  what a browser requires of a dialog.
- **Not replicated.** A call with a browser leg is not replicated for in-call HA:
  its ICE and DTLS state belongs to the node. If the node dies the call ends (the
  browser's ICE failure ends it cleanly on its side) and a new call works at once
  through the survivor. Re-homing a browser call by ICE restart is a lab item.
- **Apps that Hello answers itself.** Voicemail, announcements, queues and IVRs
  play through the browser leg like any other. Conference rooms take browser
  callers too (the participant's media is a mux endpoint). Paging and fax are for
  phones: a browser caller is refused with 488.

## Video

A call between two web clients can carry video. Hello **relays** it: it never
decodes, repacketizes or transcodes it.

- **Codecs.** H.264 Constrained Baseline (packetization mode 1) first, then VP8,
  from what the caller's offer lists. H.265, AV1, high-profile H.264, mode 0, and
  the RTX, RED and FEC payload types are not offered: they need machinery the
  relay does not have. The callee is offered the supported codecs in the
  caller's payload numbering, answers with one, and the caller is answered with
  that one (its fmtp as the callee sent it). No common codec means `m=video 0`
  and the call goes on as audio.
- **Never to a desk phone or a trunk.** Only a web client on both legs gets video.
  A call to a desk phone, a trunk or anything else answers the caller's video
  line with port 0; the trace says why and
  `hello_video_negotiation_failures_total{reason}` counts it. (Video phones and
  door stations are a later piece built on the same relay.)
- **Policy.** `extensions.video_allowed` (Extensions page, "Video allowed", on by
  default) for either extension of the call, `HELLO_VIDEO_ENABLED` (default
  true), and the node's session limit `HELLO_VIDEO_MAX_SESSIONS` (default 20): a
  video line past it is answered with port 0 and counted.
- **Relay.** Audio and video share the browser's one transport (BUNDLE), told
  apart by payload type. Each leg gets its own SSRC and a constant sequence and
  timestamp offset from the other leg's stream, so a retransmission request on one
  leg maps to a sequence number on the other by subtraction (RFC 7667
  translator). RTCP is terminated per leg: Hello sends sender and reception
  reports from the leg's own statistics, forwards picture loss (PLI), full intra
  requests (as a PLI) and NACKs (mapped by the offset), carries the receiver's
  bandwidth estimate (REMB) held at the ceiling, and asks a stream's sender for a
  keyframe when it starts and when a hold ends. Transport-wide congestion control
  and `abs-send-time` are not negotiated (their sequence space is per hop), so a
  sender falls back to REMB and receiver reports.
- **Ceiling.** `HELLO_VIDEO_MAX_KBPS` (default 1500) is written into `b=AS` and
  enforced as a token-bucket rate cap per direction on the relay: packets above
  it are dropped and counted (`hello_video_dropped_packets_total{reason="cap"}`)
  and the senders are told the ceiling in REMB.
- **Mid-call.** A browser adds video by re-INVITE: if the other leg is a browser
  too, the codec the first browser listed is offered to it as the only one. If the
  other side refuses, the video ends again and the trace says so. Removing video
  returns the slot. A hold stops audio and video together; resuming asks both
  senders for a keyframe.
- **In the console.** A video button in the call controls, remote video large
  with the self-view picture-in-picture, the received resolution, frame rate and
  codec, camera selection, and the end-of-call report. The call's detail page says
  the call carried video.
- **Not yet.** Recording of video calls (they record audio only and the trace
  says so), video in conference rooms, and video phones and door stations are
  later pieces; the relay's two-leg pairing and the per-leg video configuration
  (`Relay.SetVideo`, `VideoLeg`) are the seams they plug into. A browser's
  per-codec behaviour (Safari, Firefox, hardware encoders), the REMB fallback's
  quality in each browser, and real-world capacity are lab items.

### Settings and series

| Setting                    | Default | Meaning                                   |
| -------------------------- | ------- | ----------------------------------------- |
| `HELLO_VIDEO_ENABLED`      | true    | Calls between web clients may carry video |
| `HELLO_VIDEO_MAX_KBPS`     | 1500    | Per-direction ceiling of a call's video   |
| `HELLO_VIDEO_MAX_SESSIONS` | 20      | Calls one node carries video for          |

`hello_video_sessions`, `hello_video_keyframe_requests_total`,
`hello_video_dropped_packets_total{reason}` and
`hello_video_negotiation_failures_total{reason}` (`no_common_codec`,
`not_allowed`, `over_limit`, `peer_cannot`, `disabled`).

### Tests

`TestWebRTCCallToDeskPhone`, `TestPhoneCallsBrowser`,
`TestWebRTCRefusedOnCleartextHop`, `TestWebRTCFeaturesOnCall`, `TestWebVideoCall`,
`TestVideoRefusals` and `TestVideoUpgradeDowngrade` (internal/sip) place calls
with a headless pion DTLS-SRTP client as the browser through the whole call path;
`TestVideoCodecChoice`, `TestVideoNegotiation`, `TestVideoRelayForwarding`,
`TestVideoSSRCSwitchContinues`, `TestVideoCapEnforced`, `TestVideoReports` and
`TestAlternateLeg` (internal/media) test the SDP and relay with two browsers on
the shared port. The web suite (`vitest`) covers the softphone state machine, the
Web Lock rule, the failure states and the dock. A real browser (Chrome, Edge,
Firefox, Safari) on the LAN is on the hardware list.
