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

An anchored call keeps its anchor until it ends — a mid-call re-INVITE can
add the anchor, but Hello does not remove it mid-call.

## Codecs (HD voice)

Two phones that both support G.722 talk wideband end to end, and Opus works
where both ends have it. G.711 (PCMU, PCMA) is the fallback for everything
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
paging, IVR) is a session with its own sample rate: 16 kHz when the caller
offered G.722 (it is answered with G.722), 8 kHz G.711 otherwise; an
Opus-only caller is answered with Opus. Prompts and recordings are WAV files
at any rate from 8 to 48 kHz; the rate a file has is resampled to the session's,
so existing 8 kHz assets keep working and voicemail left over a G.722 call is
stored at 16 kHz. Sessions (voicemail, IVR) run at 16 kHz on Opus; announcements,
the recording notice and music on hold are played to Opus legs at 48 kHz.

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
on the last two. Migration `00021_music_on_hold`.

## Metrics

`hello_rtp_sessions`, `hello_rtp_packets_total{direction}`,
`hello_rtp_octets_total{direction}`, `hello_rtp_loss_total{direction}`,
`hello_rtp_jitter_ms{direction}`, `hello_media_anchored_calls`,
`hello_media_anchor_failures_total`, `hello_recording_storage_bytes`.

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
