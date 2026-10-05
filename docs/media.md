# Media anchoring

Hello relays RTP through itself only when a feature needs it — NAT handling,
call recording, announcements, voicemail. Every other call is direct
endpoint-to-endpoint audio (spec §4, §16). This page describes when a call
anchors and what that means; the implementation detail lives in
`internal/media`.

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

## Codecs

PCMU, PCMA, G.722 and Opus pass through the anchor untouched — Hello never
transcodes. Recordings store the pass-through payload as received (G.711
payloads are recorded as WAV; other codecs are stored with the codec noted
in the recording metadata).

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

The anchor binds its relay legs on the hello-sip pod: UDP
`HELLO_RTP_PORT_MIN`–`HELLO_RTP_PORT_MAX` (20000–21000) on the pod IP, set
on hello-sip-1/2 in `deploy/kuvryn-sync/kw/resources.yaml`. The hello-sip
Services stay ClusterIP with no RTP ports: a Service cannot map a port
range in one entry, a per-port NodePort list (1001 entries per Service)
would still not carry audio, because the anchored SDP answers advertise the
offer's own c= address when no anchor host is wired (see below) — a phone
would send RTP to the address in the answer, never to a node.

Until hello-sip advertises a reachable anchor host:port in its SDP answers
(a code change: wire the media anchor host from the node's routable
address), anchored media carries audio only between endpoints that can
already reach each other and the pod IP (the compose lab's shared host).
On kw, a LAN phone's anchored calls complete and record their signaling
(CDR, traces, recording rows), but RTP does not flow to the anchor, so
recordings capture silence and announcements/voicemail audio do not reach
a LAN phone.

Anchoring triggers are unchanged: NAT detection, `*1` or per-extension
`record_default`, an announcement destination or pre-transfer prompt,
voicemail, or `HELLO_MEDIA_FORCE_ANCHOR=true`. On kw the force switch stays
off; see deploy/kuvryn-sync/kw/resources.yaml for the live env.
