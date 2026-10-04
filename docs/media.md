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
