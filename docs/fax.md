# Fax

Hello receives faxes to an extension or a fax number, delivers them as PDF by
email and in the console, and sends a PDF as a fax. Fax travels as G.711 audio
(passthrough, the default) or as T.38 on trunks that allow it.

The T.30 modems run in a separate service, `hello-fax`, built on spandsp
through cgo in its own image. hello-sip and hello-control never link spandsp.
Fax is off until `HELLO_FAX_ENABLED` is true.

## How a fax moves

```
carrier ── RTP / UDPTL ──> hello-sip relay ── RTP / UDPTL ──> hello-fax (T.30)
                                  │                                  │
                          signalling, CDR                     HTTP: start, poll, PDF
                          row, PDF in MinIO, email            (cluster-internal)
```

- hello-sip owns the call. It answers (or places) it, relays the media to a UDP
  port hello-fax opens for the session, and polls the session every half
  second. When T.30 ends it stores the result and ends the call.
- A received fax is written by hello-fax as one-bit rows, encoded to PDF in Go
  (Flate, no external library) and stored in the media bucket at
  `fax/<yyyy>/<id>.pdf`. A fax is limited to 50 pages or 20 MB; pages beyond
  that are dropped and the fax is marked truncated in the engine's log.
- For a fax leg echo cancellation, silence suppression and comfort noise are
  off (the relay forwards the payload untouched), and the receive buffer is a
  fixed 60 ms.
- Fax calls are not replicated for in-call high availability: a node lost
  during a fax drops that call, and the sender retries (a received fax is kept
  as `partial` with the pages that arrived).

## Receiving

1. Create a **fax destination** (`/faxes`, Destinations, or `createFaxDestination`):
   a name, the owner extension, and optionally an email address.
2. Point an **inbound route** at it (destination kind `fax`), or an **IVR key**
   (kind `fax`). A route to a fax destination answers the call and starts the
   fax receiver at once; an IVR key moves an answered call to G.711 with a
   re-INVITE and then starts it.
3. When the fax is complete (or failed after some pages) the PDF is emailed
   through the same SMTP settings as voicemail. The attachment is named
   `fax-<yyyymmdd-hhmm>-<id>.pdf`; the message carries the sender number in
   the subject and body. Recipients are the destination's address, or when
   there is none the enabled users linked to the owner extension who have an
   email address. A fax with no recipient is marked `mail failed`; the PDF is
   still in the console.
4. A user with the owner extension sees the fax under `/faxes` (the My faxes
   view) and can download it. Operators see all faxes.

A fax that fails after some pages keeps them: status `partial`, with the T.30
result code and text. A fax that fails with no pages is `failed` and has no
PDF.

## Sending

`POST /api/v1/faxes` (multipart `file`, `to`, optional `callerId`, or JSON with
`pdfBase64`; at most 10 MB and 50 pages). `to` is E.164. hello hands the PDF to
hello-fax, which rasterises it with `pdftoppm` (fixed argv, no shell, private
temporary directory) at 204 by 196 dpi on a 1728 pixel page. A PDF that cannot
be rasterised is refused with 422 at upload time.

The fax is queued. Any hello-sip node claims it under a lease, places the call
through the **outbound routes** as a call from the pseudo source `fax` (a route
that lists source extensions does not carry faxes; one that applies to every
extension does), and hello-fax transmits. On busy, no answer or no carrier the
fax is retried: three attempts, five minutes apart. Cancelling a fax being sent
(`DELETE`) ends its call at the next lease renewal.

## T.38

A trunk's `faxMode` (trunk form, or `updateTrunk`):

| Mode          | Behaviour                                                                                             |
| ------------- | ----------------------------------------------------------------------------------------------------- |
| `passthrough` | The default. G.711 only. CNG never moves the call to T.38.                                            |
| `t38`         | Accept `m=image udptl t38` offers; move an answered fax to T.38 when CNG is heard or the engine asks. |
| `off`         | Fax calls on the trunk are refused with 488.                                                          |

On a `t38` trunk Hello answers with `T38FaxVersion 0`, `T38MaxBitRate 14400`,
`T38FaxRateManagement transferredTCF` and `T38FaxUdpEC t38UDPRedundancy`. UDPTL
is relayed unmodified by the media relay (only the addresses change) to
hello-fax's T.38 terminal. When Hello sends the re-INVITE and the carrier
answers 488 (or accepts with port 0, or with parameters Hello does not serve),
the fax stays on G.711 passthrough. A carrier that sends the T.38 re-INVITE
itself is answered the same way.

Interoperability with a particular carrier and fax machine is a hardware test;
see "Lab checks" below.

## Configuration

| Variable                     | Where                    | Meaning                                                        |
| ---------------------------- | ------------------------ | -------------------------------------------------------------- |
| `HELLO_FAX_ENABLED`          | hello-control, hello-sip | `true` turns fax on (default false).                           |
| `HELLO_FAX_ADDR`             | hello-control, hello-sip | `host:port` of the hello-fax control API.                      |
| `HELLO_FAX_TOKEN`            | all three                | Optional bearer token between the services.                    |
| `HELLO_FAX_RETENTION_DAYS`   | hello-control            | Days a fax is kept, 1 to 3650 (default 90).                    |
| `HELLO_FAX_LISTEN`           | hello-fax                | Control API listen address (default `:8083`).                  |
| `HELLO_FAX_RTP_PORT_MIN/MAX` | hello-fax                | UDP range for sessions (default 40000-40999).                  |
| `HELLO_FAX_MEDIA_HOST`       | hello-fax                | The address hello-sip sends media to; the pod IP in the chart. |
| `HELLO_FAX_PDFTOPPM`         | hello-fax                | Path of `pdftoppm` (default: from `PATH`).                     |

In the Helm chart, `fax.enabled: true` deploys hello-fax (a ClusterIP Service
for the control API, no Ingress; media goes to pod addresses inside the
cluster) and sets the variables on hello-control and hello-sip. Several
hello-fax replicas can run: the session lives on the replica that started it,
and hello-sip talks to that pod directly for the rest of the call.

## Retention and metrics

Faxes are purged after the retention period by a job that runs on one control
node at a time, in batches of 500, deleting the PDF objects before the rows.
A fax that is still `receiving` two hours after it started is closed as failed.

Metric: `hello_fax_total{direction,result}` (hello-sip). Results: `received`,
`sent`, `partial`, `failed`, `refused`, `retry`, `cancelled`.

## Troubleshooting

- A fax call is refused with 488: the call's trace says why (`fax is off on
trunk`, `the fax engine is unavailable`, `a fax call needs G.711`, or the
  T.38 offer is not one Hello serves).
- `sendFax` answers 503: `HELLO_FAX_ENABLED` is off, or hello-fax cannot be
  reached from hello-control.
- Pages are garbled or the fax drops at 14400: use passthrough with a G.711
  allow-list of PCMU/PCMA only, or move the trunk to `t38`; check packet loss
  in the CDR quality fields.
- `no email address for extension N`: link a user with an email address to the
  owner extension, or set an address on the fax destination.

## Lab checks

Hardware tests that cannot run in CI: a real fax machine receiving and
sending through a real carrier on passthrough; the same on a T.38 trunk; a
carrier that starts the T.38 re-INVITE itself; a carrier that answers the
re-INVITE with 488; ECM on and off; 20-page documents.
