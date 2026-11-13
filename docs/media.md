# Media anchoring

Hello relays RTP through itself for every call (the always-anchor policy,
spec S-7); the triggers below are kept as trace information on the call.
This page describes what anchoring means and which codecs it carries; the
implementation detail lives in `internal/media`.

## When a call anchors

| Trigger                   | Example                                               |
| ------------------------- | ----------------------------------------------------- |
| An endpoint is behind NAT | contact address differs from the packet source        |
| Recording is active       | `*1` pressed, or the extension's record default is on |
| An announcement plays     | ring-group failure destination, pre-transfer prompt   |
| Voicemail is involved     | unanswered/busy/DND call with a voicemail box         |
| The operator forces it    | `HELLO_MEDIA_FORCE_ANCHOR=true`                       |

## Fax and T.38

A fax call is anchored by a relay of its own whose far leg is the hello-fax
service (see [fax.md](fax.md)). It carries G.711 (PCMU or PCMA) only; there is
no echo cancellation, silence suppression, comfort noise or recording on it,
and the engine's receive buffer is fixed. On a trunk whose fax mode is `t38`
the same relay switches to **UDPTL** after a re-INVITE to `m=image udptl t38`:
the datagrams cross the relay unmodified (no sequence or timestamp rewrite, no
RTP parsing), and a carrier answering 488 leaves the call on G.711.

An anchored call keeps its anchor until it ends — a mid-call re-INVITE can
add the anchor, but Hello does not remove it mid-call.

## Codecs (HD voice)

Phones talk Opus where both ends have it, G.722 wideband where both do, and
the built-in provisioning templates list them in that order (Opus, G.722,
PCMU, PCMA). G.711 (PCMU, PCMA) is the fallback for everything
else.

**Negotiation.** Hello forwards a filtered copy of the caller's codec list
to the callee: G.722, Opus, PCMU, PCMA and telephone-event, in the caller's
order, each with its own rtpmap and fmtp lines and its dynamic payload
type. Everything else the phone offered (G.729, iLBC, ...) is dropped. The
caller's answer is whatever the callee chose, so the relay needs no
transcoding: payload types pass through unchanged, dynamic ones included. A
mid-call re-INVITE (hold, resume) keeps the codec the call runs on.

**Trunks.** Most carriers accept G.711 only, so a trunk is offered PCMU and
PCMA unless its codec list says otherwise (Trunks page, "Codecs", or
`codecs` in the trunk API). If that filter would leave a wideband-only
caller nothing to offer, the caller's list is sent as it is and the carrier
decides.

**G.722 and clock rates.** G.722 audio is sampled at 16 kHz but its RTP
clock is 8000 (RFC 3551), so `a=rtpmap:9 G722/8000` and a timestamp step of
160 per 20 ms frame. Opus is `opus/48000/2` with a step of 960. The
telephone-event codec that goes with a codec is the one with the same clock
rate; a phone that offers only `telephone-event/16000` with G.722 is
answered with that.

**Media Hello ends itself** (voicemail, announcements, the recording notice,
IVR, conference rooms) is a session with its own sample rate: the caller is
answered with Opus when it offered Opus and the build has it (16 kHz), else
with G.722 (16 kHz), else G.711 (8 kHz). Paging and multicast stay G.722
(or G.711), as a multicast page cannot carry Opus. Prompts and recordings are WAV files
at any rate from 8 to 48 kHz; the rate a file has is resampled to the session's,
so existing 8 kHz assets keep working and voicemail left over a G.722 call is
stored at 16 kHz. Sessions (voicemail, IVR) run at 16 kHz on Opus; announcements,
the recording notice and music on hold are played to Opus legs at 48 kHz.
Spoken voicemail prompts are cached WAVs at the session's rate
(`tts/hd/<sha256>.wav`, stored at the voice's own rate in the voicemail bucket); see
[voicemail.md](voicemail.md) for the key layout.

**Which codec a call used.** Every call that connects logs one `call codec`
line per leg (`leg`, `media_mode`, `codec`, `clock_rate`, `payload_type`,
`ptime`, `reason`) and the call
detail record keeps the codec each leg ended on, as `name/clock rate`:
`callerCodec` and `calleeCodec` in `GET /api/v1/cdrs` and `/api/v1/cdrs/{id}`
(`caller_codec` and `callee_codec` columns, migration 00055; the CSV export
ends with the same two), shown as the Codec column of Call history and the
two codec rows of a call's detail. A re-INVITE that switches the codec
replaces it, so the record names the last codec of the call. A leg with no
negotiated media is null: a call that was not answered, the callee side of a
call Hello ends itself (voicemail, conference, IVR, queue), and every call
recorded before the migration. G.722 shows as `G722/8000`, as its RTP clock
is 8000 (see above).

**Recording** decodes G.711, G.722 and Opus to PCM and mixes the two
directions into one WAV at the higher of their sample rates; any other
codec is stored as it came with the codec noted.

**Paging** with a G.722 caller sends G.722 to members that answer with it,
a G.711 rendition (decoded and downsampled) to members that only answered
PCMU, and G.711 to a multicast group.

**Opus** is implemented with `github.com/kazzmir/opus-go`, libopus
transpiled to Go: no cgo, so the static `CGO_ENABLED=0` build and the Alpine
image are unchanged. It is excluded from `-race` builds (its pointer
arithmetic trips the race detector's checkptr), where Opus calls still relay
but are not decodable by the recorder or answerable by voicemail.

## Who may send media into a call

The relay's UDP ports are reachable by anyone who can reach the host, so a
leg does not take audio from just anyone. Each leg accepts RTP from:

- the address its remote SDP names, or an address the signalling vouches
  for: the source of the caller's INVITE, the source of the callee's
  answer. A phone behind NAT names a private address in its SDP, while its
  media arrives from the same public address as its signalling;
- any address, for the first packet only, when the SDP names none usable
  (no address, `0.0.0.0` for a hold, or a private/CGNAT one) and the
  signalling gave no address to vouch for. That packet pins the leg.

Once a leg has its first packet it is pinned to that source **address**. The
port may change (a new NAT mapping), but a packet from any other address is
refused: it is not counted, not recorded, not relayed, and cannot move
where the other side's audio goes. Refusals show as `Rejected` in the
relay's per-direction stats. A mid-call re-INVITE or takeover that moves
media is a new negotiation: the next target restarts the policy, and the
signalling addresses stay vouched.

Limits: the policy is by address, not by RTP stream, so a host that can
spoof the pinned address (same network segment) is not stopped, and the
SSRC is not pinned because phones change it legitimately at hold and
resume. A carrier whose media comes from an address that is neither in its
SDP nor its signalling source is refused; the symptom is one-way or no
audio on that trunk and a rising `Rejected` count.

## Music on hold and waiting music

An anchored call plays music to a party the other side puts on hold: a
`sendonly` or `inactive` re-INVITE from either phone starts the music toward
the other leg, and the holder's own stream is muted meanwhile (an `inactive`
hold is offered to the held phone as `sendonly` so it can hear). A resume
(`sendrecv`), the hangup, or a takeover that finds the call no longer held
stops it. Parking (S-6) uses the same player: the parked party hears the
class a hold by the parker would pick, until it is retrieved, answered by
the parker again or moved to voicemail.

Music comes from **MOH classes** (Media, Music on hold): a named, ordered
playlist of announcements, played in a loop or shuffled (a shuffle plays every
track once per pass and never repeats one back to back). The class is picked
in this order, the first that names an existing class winning:

1. the extension that holds the call (its **Music on hold** setting);
2. the ring group the call dialled;
3. the inbound route that delivered it;
4. the one class marked **default**;
5. the built-in tune (a short computed arpeggio), also used when a class has
   no announcements or none of them can be loaded, so a hold is never silent.

Deleting a class clears every pick that named it; those fall through to the
next level.

**Waiting music instead of ringback.** A ring group or inbound route with
_Play music instead of ringback_ answers the caller with `183 Session
Progress` and the anchor's SDP, then plays its class while the destination
rings; the music stops when the destination answers (or the call moves on to
voicemail or an announcement). The group's setting wins over the route's. A
phone that ignores early media hears nothing until the answer, which is why
the setting is off by default.

Tracks are announcements (WAV, uploaded on the Announcements page). They are
decoded once per 30 seconds (and shared by every held call) and encoded in the leg's
negotiated codec at that codec's rate: 8 kHz for G.711, 16 kHz for G.722, 48 kHz
for Opus. A track is stored at the rate it was uploaded at (see below), so a
48 kHz song is not squashed to phone quality first; it is low-pass filtered once
per rate when a call needs a lower one. Hello is the only
source of hold music: no built-in phone template writes a music on hold
setting (Grandstream P2350 stays empty), which `TestPhonesOriginNoMusicOnHold`
enforces.

If the node anchoring a held call fails, the replicated state carries
`moh:<class id>:<leg>`; the node that takes the call over restarts the music
from the start of the playlist toward the same leg (`TestMusicOnHoldTakeover`).

API: `GET/POST /api/v1/moh-classes`, `GET/PUT/DELETE /api/v1/moh-classes/{id}`;
`mohClassId` on extensions, ring groups and inbound routes, and `mohRingback`
on the last two. Migration `00029_music_on_hold`.

The one exception is a [conference room](conference.md): it decodes every
participant to a common 8 kHz mixing rate, mixes, and encodes each
participant's mix in that participant's own codec (G.711, G.722 and Opus; the
wideband ones are converted to and from 8 kHz). Each participant uses one RTP
port from the range below.

## Roaming and dead media

A Wi-Fi handset that moves to another access point can keep its IP (same
subnet) or get a new one (another subnet). Hello copes with both on an
anchored call:

- A re-INVITE whose `c=` address or port differs from the one the leg last
  named is relayed to the other side as usual, and the anchor re-learns the
  leg: the relay's source policy starts over from the new address, vouched by
  the signalling source of that re-INVITE, and the old address is no longer
  accepted. A hold's `0.0.0.0` and a re-INVITE that repeats the address leave
  the leg alone. The other leg is not touched.
- The relay notes when it last accepted a datagram from either peer. A call
  that is bridged between two parties, anchored on this node and not on hold,
  is ended after `HELLO_MEDIA_DEAD_SECONDS` (default 60, `0` turns it off)
  with no RTP in either direction: both legs get BYE, the CDR side is
  `system` and the reason `no media`. Resuming from hold restarts the clock.
  One-way audio keeps a call up. A taken-over call keeps the watch too.

The signalling half of roaming (the re-REGISTER from the new address replacing
the old contact, and session timers) is in [phones.md](phones.md#roaming-keep-alive-and-dead-calls).

## SRTP (SDES)

Anchored calls can be encrypted per leg (phone-feature-parity S-15). The
caller leg and the callee leg are separate SRTP sessions with their own keys:
the anchor decrypts what one leg sends, rewrites the RTP, and encrypts it for
the other leg, so recording, DTMF and announcements work as on plain RTP and a
phone never learns the other phone's key.

- **Key exchange** is SDES (RFC 4568), `a=crypto` lines in the SDP, with
  `AES_CM_128_HMAC_SHA1_80` first and `AES_CM_128_HMAC_SHA1_32` second.
  `AES_256_CM_HMAC_SHA1_80` is accepted when a phone asks for it but never
  offered. Lines with an MKI or session parameters Hello does not implement
  are ignored. Keys are generated per leg with the system random source and
  are never logged, shown or stored; replicated call state carries no key
  (a node that takes a call over negotiates fresh ones).
- **TLS only.** The keys travel in the SDP, so Hello offers or accepts crypto
  only on a leg whose signalling is TLS (a phone registered through the
  Kamailio TLS listener, see [tls.md](tls.md)). On a UDP leg the crypto lines
  are dropped and plain RTP is answered. Trunk legs are UDP (SIP over TLS for
  trunks is a later phase of the trunks-routing spec), so a trunk never carries
  SRTP today.
- **Policy** per device (`PATCH /api/v1/devices/{id}`, **Devices** page) and per
  trunk (**Trunks** page, `off` or `optional`; see below): `off` offers nothing; `optional` (device default)
  offers `RTP/AVP` with crypto lines, which a phone without SRTP ignores, and
  uses SRTP when the answer carries it; `required` offers `RTP/SAVP` only. A
  `required` leg that cannot be encrypted refuses the call with 488 (a caller
  on UDP or without crypto, a callee that answers without crypto), and a
  required callee that is not on TLS is not rung. An `RTP/SAVP` offer on a leg
  Hello cannot encrypt is also refused with 488.
- **Trunks cannot be `required`.** A trunk's legs are UDP, so `required` would
  refuse every call over it. The API (`POST`/`PATCH /api/v1/trunks`, so the
  MCP tools too) answers 422 on the `srtp` field when a trunk is set to
  `required`, and the Trunks page offers `off` and `optional` only, with the
  reason. `optional` on a trunk behaves as `off` until trunks gain a TLS
  transport. A trunk already stored as `required` from before the rule still
  refuses its calls with 488 (never silently carried in clear); the Trunks page
  flags it and saving it with another policy fixes it.
- **Per leg.** One leg can be encrypted and the other plain (a TLS phone calling
  a UDP phone). The CDR's `srtp` says `none`, `caller`, `callee` or `both`, and
  the call's trace names the legs and suite (never the key).
- **Mid-call.** Hold, resume and refreshes keep the leg's keys and its media
  profile; a phone that rekeys in a re-INVITE is followed, and a phone that
  roams to a new media address keeps its keys. A leg handed to another party
  (a blind or attended transfer target, a retrieved parked call, a pickup, the
  parker's device when a parked call returns) drops the previous endpoint's
  keys and negotiates new ones with the new party under the new party's own
  policy and transport: it is offered (or, when it offers, answered with)
  crypto when its policy allows and its contact is on TLS. A party whose policy
  is `required` that cannot be encrypted is refused (488, or not rung, or the
  call ends), never carried in the clear; an `optional` party that does not
  answer with crypto is plain RTP and counted as `plain`. A retrieve or pickup
  that crosses nodes (the call is held on another node) cannot carry keys
  through the shared store, so it is plain RTP for an `optional` phone and is
  refused with 488 for a `required` one.
- **Authentication.** A packet on an encrypted leg that fails authentication or
  replay checks is dropped, counted (`auth_failed` in the call's stats) and
  never latches the leg's address. RTCP multiplexed on the RTP port is checked
  (SRTCP) and consumed.
- **Voicemail, paging and conference rooms** follow the same policy. Voicemail
  and a conference participant answer a caller's `a=crypto` offer with their own
  key (one SRTP session per caller, so the mixer encrypts the mix per
  participant); a call parked then sent to voicemail keeps the encryption it had.
  A page encrypts its caller leg the same way and each member leg separately:
  every member is offered its own keys under its device's policy and transport,
  a member that requires SRTP and is not on TLS is skipped
  (`srtp_required` in the page legs), and one that answers without crypto is
  hung up. A page's multicast group is always plain RTP (the stream is
  one-to-many by design). An HA takeover of voicemail or a conference call
  offers the caller fresh keys and ends the call if it does not answer with
  them.

## DTLS-SRTP and ICE-lite

The same per-leg SRTP state can also be keyed by a DTLS handshake instead of
SDES keys in the SDP (phone-feature-parity S-16, RFC 5763 and 5764), and a
leg can run ICE-lite (RFC 8445). They are the media foundation of the later
WebRTC softphone: a browser leg is one more leg profile (`UDP/TLS/RTP/SAVPF`,
BUNDLE) on top of the code below.

- **What is told apart on the media port.** A leg's UDP socket carries STUN,
  DTLS and RTP/RTCP, told apart by the first byte (RFC 7983). STUN goes to the
  leg's ICE-lite agent, DTLS records to the handshake, the rest is media. A
  DTLS leg whose keys are not in yet carries nothing in either direction
  (never plain RTP). `a=rtcp-mux` is answered on DTLS and ICE legs; RTCP on the
  RTP port is consumed.
- **Answering.** An offer on `UDP/TLS/RTP/SAVP` (or `SAVPF`) with
  `a=fingerprint` and `a=setup` is answered on the same transport with Hello's
  fingerprint and role, and the handshake starts on the leg's socket. Hello
  answers `active` (it is the DTLS client) to `actpass` and `passive`, and
  `passive` to `active`; a client waits until it knows where the peer is (and,
  with ICE, until a check has authenticated that address). A phone that offers
  `RTP/AVP` with `a=crypto` is answered with SDES as before; the answer
  follows the offer.
- **Offering.** When Hello is the offerer (the callee leg of a call, a
  parked party's return, a transfer target) the device's **Key exchange**
  decides: `sdes` (the default) offers `a=crypto`, `dtls` offers
  `UDP/TLS/RTP/SAVP` with Hello's fingerprint, `a=setup:actpass` and
  `a=rtcp-mux`. The policy keeps its meaning: `off` offers nothing, `optional`
  and `required` offer the chosen exchange, `required` refuses a call whose
  answer does not carry it (488). A DTLS transport cannot fall back to plain
  RTP inside the same offer, so a device set to `dtls` must be a phone that
  does DTLS-SRTP.
- **TLS only, as SDES.** DTLS keeps no key in the signalling, but the
  fingerprint is the only thing that authenticates the peer's certificate, so
  it is trusted only when it arrived over a protected signalling leg: DTLS is
  offered or accepted on a leg whose signalling is TLS and whose policy is not
  `off`, exactly as SDES is. A DTLS offer on any other leg is refused with 488,
  as is a DTLS offer with no usable fingerprint, and one on a node that has
  `HELLO_MEDIA_DTLS=false`.
- **Certificate.** Each hello-sip process makes a self-signed ECDSA P-256
  certificate when it first needs one (renewed for new sessions after 30
  days). It is never stored or shared, carries no identity, and is trusted by
  the peer only through its SHA-256 fingerprint in the SDP. Hello accepts the
  peer's certificate only if it matches a fingerprint in the peer's SDP
  (`sha-1`, `sha-256`, `sha-384`, `sha-512`); a mismatch fails the handshake.
  After a restart or a takeover the fingerprint changes, and the taker's
  re-INVITE carries the new one.
- **SRTP profiles.** `SRTP_AEAD_AES_128_GCM`, `SRTP_AES128_CM_HMAC_SHA1_80`,
  `SRTP_AES128_CM_HMAC_SHA1_32` are offered in the handshake, in that order.
  The extended master secret (RFC 7627) is requested but not required, for
  phones on an old TLS stack; the peer's certificate is pinned by fingerprint
  either way.
- **A failed handshake** (timeout after 8 s, a certificate that does not match,
  a protocol error) ends the call: the leg's offer was DTLS only, so there is
  nothing to fall back to. The trace says which leg and why, and
  `hello_srtp_negotiations_total{result="dtls_failed"}` counts it.
- **Re-INVITE.** A re-INVITE that repeats the peer's fingerprint keeps the
  session; one with a new fingerprint (the phone rebooted, a transfer) drops the
  keys and handshakes again. Hello's own answers repeat its fingerprint and
  role.
- **ICE-lite.** An offer with `a=ice-ufrag` and `a=ice-pwd` is answered with
  `a=ice-lite`, Hello's own credentials for that leg, one host candidate (the
  relay address and port, never a server-reflexive or relay one) and
  `a=rtcp-mux`. Hello answers binding requests whose USERNAME is
  `<its ufrag>:<the phone's ufrag>` and whose MESSAGE-INTEGRITY verifies with
  its password, with the sender's address; anything else is dropped silently. A
  valid check makes its source address an address the leg accepts media from
  and, for the first or the nominated pair, the address media is sent to. Hello
  is always the controlled side (a peer that is also controlled gets 487).
  `a=candidate` lines of the phone are parsed and kept for the trace; as the lite
  side Hello never checks them. A device with **ICE** on is offered ICE-lite by
  Hello too. ICE is not tied to TLS (it carries no secret). ICE alone does not
  encrypt anything.
- **Voicemail, conference rooms and the page caller** use the same code on
  their own sockets (a greeting waits for the handshake to finish). A page's
  members are offered SDES, never DTLS. A call retrieved or picked up from
  another node with a DTLS leg is refused with 488, like a required SDES one:
  the command through the shared store does not carry the retriever's policy
  and transport.
- **HA.** The replicated mode of a leg is `dtls`; a taker offers DTLS with its
  own fresh certificate and ends the call if the phone does not complete the
  handshake. The far end's ICE password is not replicated.
- **CDR and metrics.** `cdrs.srtp_keyex` is `none`, `sdes`, `dtls` or `mixed`
  (one of each) and `cdrs.ice` says a leg ran ICE-lite (the call detail card,
  the CSV export). `hello_dtls_handshakes_total{result}` (`ok`, `timeout`,
  `fingerprint`, `failed`), `hello_dtls_handshake_seconds`,
  `hello_ice_checks_total{result}` (`ok`, `bad_credentials`, `role_conflict`);
  a DTLS leg counts as `dtls` in `hello_srtp_negotiations_total` and in
  `hello_srtp_legs`.
- **Ports.** No new port: DTLS and STUN use the leg's RTP port (the existing
  `HELLO_RTP_PORT_MIN`-`MAX` range). Nothing in Kamailio or the chart changes
  for phones. The WebRTC phase adds the one shared port per node, its public IP
  and the TURN and Kamailio WebSocket pieces; they are not part of this item.
- **Settings.** `HELLO_MEDIA_DTLS` and `HELLO_MEDIA_ICE`, both on by default.

## Metrics

`hello_sip_session_teardowns_total{reason}` counts the calls ended for a dead
far end (`refresh_failed`, `refresh_missing`, `media_dead`), and
`hello_sip_session_refreshes_total{method,result}` the refreshes Hello sent.
The anchor's own series:

`hello_rtp_sessions`, `hello_rtp_packets_total{direction}`,
`hello_rtp_octets_total{direction}`, `hello_rtp_loss_total{direction}`,
`hello_rtp_jitter_ms{direction}`, `hello_media_anchored_calls`,
`hello_media_anchor_failures_total`, `hello_recording_storage_bytes`, and for
SRTP `hello_srtp_legs` (legs encrypted now) and
`hello_srtp_negotiations_total{result}` (`srtp`, `dtls`, `plain`, `refused`,
`dtls_failed`); the DTLS and ICE series are listed in the section above.

## RTP port range

Anchor sessions draw RTP ports from `HELLO_RTP_PORT_MIN`–
`HELLO_RTP_PORT_MAX` (default 20000–21000) on the hello-sip node that owns
the call. When the range is exhausted, the call falls back to direct media
with a routing-trace step and a failure metric.

## Anchored media on kw (Kubernetes)

The anchor binds its relay legs on UDP `HELLO_RTP_PORT_MIN`–
`HELLO_RTP_PORT_MAX` (20000–21000). On kw the hello-sip pods run with
`hostNetwork: true`, so the legs bind the node IP, and
`HELLO_MEDIA_ANCHOR_HOST` (the Downward API's `status.hostIP`) is stamped
into every anchored SDP answer: a LAN phone sends its RTP straight to the
node at the advertised port pair. Both deployments carry a required
anti-affinity (one hello-sip per node: the pods bind node UDP 5060 and TCP
8082). The hello-sip Services stay ClusterIP with no RTP ports — no
Service can map a port range — and serve only the in-cluster paths:
Kamailio's dispatcher set 1 and the dialog routes
(`HELLO_SIP_ADVERTISED_ADDR` stays the Service name).

`HELLO_MEDIA_ANCHOR_HOST` is optional: unset, the SDP answers mirror the
offer's own c= address, which carries audio only when the peer can reach
the pod directly (the compose lab). Set it to the node's LAN IPv4 — on kw
the manifest derives it per node, so it stays correct wherever the pod
schedules. Before this wiring existed, a LAN phone's anchored calls
completed and recorded their signaling (CDR, traces, recording rows), but
RTP never reached the anchor, so recordings captured silence and
announcements/voicemail audio did not reach a LAN phone.

Anchoring triggers are unchanged: NAT detection, `*1` or per-extension
`record_default`, an announcement destination or pre-transfer prompt,
voicemail, or `HELLO_MEDIA_FORCE_ANCHOR=true`. On kw the force switch stays
off; see deploy/kuvryn-sync/kw/resources.yaml for the live env.

## Announcement audio

Uploaded announcements (and TTS output) are stored as 16-bit mono PCM WAV at
the sample rate of the upload, up to 48 kHz; faster files (96 kHz) are reduced
to 48 kHz. 48 kHz is Opus's native rate, so a song reaches an Opus call
without a resample, while an 8 kHz prompt stays 8 kHz (upsampling at upload
would only grow the file). Accepted: uncompressed PCM at 8, 16, 24 or 32 bit
and 32/64-bit float, mono or multi-channel (channels are averaged to mono),
including the WAVE_FORMAT_EXTENSIBLE wrapper. Compressed WAVs (ADPCM, A-law,
mu-law, MP3) are refused with a message. The upload limit is 64 MiB, enough for
a five minute 48 kHz stereo song (about 55 MB); the stored copy is mono, about
29 MB. Voicemail greetings keep their 5 MiB limit.

At play time the audio is resampled to the session or leg rate with a
Kaiser-windowed sinc low-pass (about 80 dB stop band, cut-off just below the
target Nyquist frequency), so treble above the target rate is removed rather
than aliased. Music on hold caches the decoded track and each resampled
copy, shared by all held calls, with a 96 MiB bound on the decoded cache;
a class loads its tracks one at a time as they come up. hello-control buffers
an upload in memory (about 1.5x its size at the peak), so its memory limit
should allow for it (the chart default is 512Mi).
