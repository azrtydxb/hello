# SIP over TLS and sips

Phones can register and call over TLS (SIP on TCP 5061) instead of UDP. TLS is
opt-in: UDP stays the default and nothing changes for a phone until you switch
it on.

## How it works

Kamailio, the SIP edge, terminates TLS. It presents a certificate issued by
the Hello CA (the same CA phones already trust for provisioning over HTTPS),
accepts TLS 1.2 and later (1.3 when the phone offers it) and refuses anything
older. The hello-sip nodes behind it keep speaking UDP inside the edge
network. Kamailio tells them which transport the phone used in an
`X-Hello-Transport` header (`udp`, `tcp` or `tls`), set from the socket the
request arrived on and honoured only from a trusted proxy
(`HELLO_SIP_TRUSTED_PROXIES`), like `X-Hello-Client`. The registration stores
it: `GET /api/v1/registrations` shows `transport` per binding, and the metric
`hello_sip_registrations_by_transport{transport}` counts them per node.

Calls to a phone registered over TLS leave Kamailio over that phone's TLS
connection (Path `received=...;transport=tls`, then the connection Kamailio
keeps open; idle connections are closed after an hour).

### `sips:` requests

A request whose Request-URI is `sips:` is secure end to end, as RFC 3261
26.2.2 says. Hello routes it only over TLS:

- the caller must be on TLS, or the call is refused with `480` (reason
  `Secure Transport Required`);
- only devices registered over TLS ring; with none, the call is refused with
  `480` and does not fall through to forwarding or voicemail;
- a route that leaves for a trunk or an external SIP URI (reached over UDP) is
  refused with `480`.

`hello_sip_sips_refused_total{reason}` counts the refusals (`caller_not_tls`,
`no_tls_binding`, `no_tls_route`). A plain `sip:` call from a TLS phone to a
UDP phone is unaffected.

## Enabling it

### Helm

```yaml
kamailio:
  image: { tag: <a build with kamailio-tls-modules> }
  tls:
    enabled: true
    secretName: hello-sip-tls # kubernetes.io/tls Secret from the Hello CA
```

The Secret's certificate must name the address phones connect to (`publicHost`)
in its SANs. With `enabled`, the chart sets `KAMAILIO_TLS_PORT` and
`KAMAILIO_PUBLIC_TLS_PORT`, mounts the Secret at `/etc/hello-tls`, publishes
TCP 5061 on the phones Service (or the host network) and, when provisioning is
on, points the provisioned phones at `<publicHost>:<tls.publicPort>`
(`control.provisioning.sipTlsServer` overrides).

### Kamailio

`KAMAILIO_TLS_PORT` switches TLS on; unset, the `tls` module is not loaded, so
an image without it still starts. `KAMAILIO_PUBLIC_TLS_PORT` is the port
advertised to phones (default 5061). The certificate and key are read from
`/etc/hello-tls/tls.crt` and `tls.key`; `deploy/kamailio/tls.cfg` sets the
protocol floor. Changing `docker/kamailio` (the image installs
`kamailio-tls-modules`) means the kamailio image tag must be re-pinned on
deploy.

### kw

`deploy/kuvryn-sync/kw/resources.yaml` carries the cert-manager Certificate
`hello-sip-tls` (issuer `cluster-ca`, the node IP `192.168.10.101` as an IP
SAN, 90 days, renewed 30 days early) and mounts its Secret (optional) at
`/etc/hello-tls`. To turn TLS on, in the same change that pins the new
kamailio image, add `KAMAILIO_TLS_PORT` and `KAMAILIO_PUBLIC_TLS_PORT`
(`"30509"`) to the kamailio Deployment, the `phones-tls` container port, and
`HELLO_PROV_SIP_TLS_SERVER=192.168.10.101:30509` to hello-control. An image
without `tls.so` exits on start when `KAMAILIO_TLS_PORT` is set.

### Compose lab

The lab generates its own CA and certificate (`sip-certs`) and publishes TLS on
host TCP 5081. The CA is in the `kamailio` container at
`/etc/hello-tls/ca.crt`.

## Provisioning

With `HELLO_PROV_SIP_TLS_SERVER` set, the phones selected by
`HELLO_PROV_SIP_TLS_MODELS` register with it over TLS. The list holds vendors
or vendor and model prefix, comma separated; the default is `grandstream:WP`
(every Grandstream WP), `none` empties it. A model that fails its TLS run is
switched back to UDP by taking it out of the list; the phone follows at its
next resync.

| Vendor      | Keys written for TLS                                                                                   |
| ----------- | ------------------------------------------------------------------------------------------------------ |
| Grandstream | `P130 = 2`, `P2329 = 1` (sips scheme), `P22293 = 12`, `P22294 = 99` (TLS version floor and no ceiling) |
| Yealink     | `account.1.sip_server.1.transport_type = 2`                                                            |
| Poly        | `reg.1.server.1.transport = "TLS"`                                                                     |
| Fanvil      | `SIP1 Transport = 2`                                                                                   |

Snom has no confirmed transport key, so Snom phones stay on UDP. The phone must
trust the Hello CA; provisioning already installs it (Grandstream P8433, the
other vendors' trusted-certificate keys).

The keys come from the vendor templates and release notes and are not yet
confirmed on a phone (see Hardware checks).

### SRTP

A phone on TLS can also encrypt its media: see [media.md](media.md#srtp-sdes).
Provisioning writes the phone's SRTP mode from the device's policy, for a phone
on TLS only:

| Vendor      | Key written | Values                                                                 |
| ----------- | ----------- | ---------------------------------------------------------------------- |
| Grandstream | `P183`      | `0` off, `1` enabled but not forced (optional), `2` enabled and forced |
| Grandstream | `P60078`    | `0` SDES key exchange (WP8x6 and WP856 only)                           |

`P183` and its `0`, `1` and `2` are the same in Grandstream's own templates for
the WP8x6 (`wp8x6_config_1.0.3.39`), WP810/822/825
(`wp810_822_825_config_1.0.11.83`), WP820 (`wp820_config_1.0.7.90`) and WP856
(`wp856_config_1.0.3.16`). The WP8x6 and WP810/822/825 templates add `3` Optional
and `4` Follow SIP Transport, which Hello does not write; the WP820 has `3`
Follow SIP transport instead, and the WP856 only `0` to `2`. `P60078` (SRTP Key
Exchange, `0` SDES, `1` DTLS) is only in the WP8x6 and WP856 templates, and is
written `0` because Hello's anchor negotiates SDES.

The other vendors write no SRTP key until their key is confirmed from vendor
documentation. A phone on UDP keeps its own SRTP setting; whatever it offers,
Hello answers with plain RTP on that leg.

## Certificate renewal and alerting

cert-manager renews the certificate 30 days before it expires. Kamailio reads
the certificate at start, so a renewed one is served after the pod restarts
(`kubectl -n hello rollout restart deploy/kamailio`); until then the old
certificate is served and UDP keeps working. Alert on cert-manager's own
metric, 14 days ahead:

```
certmanager_certificate_expiration_timestamp_seconds{name="hello-sip-tls"} - time() < 14 * 86400
```

## Hardware checks

Lab item 10 of the parity spec: a Grandstream WP822 and WP836 registering over
TLS with `P130 = 2`, whether `P2329 = 1` and `P22293`/`P22294` take the values
above on the firmware in the lab, whether the phone sends `sips:` and accepts a
`sip:` Contact from Hello in a `sips:` dialog, TLS 1.3 negotiation, and a phone
behind NAT keeping its TLS connection for an hour idle.

SRTP (S-15): whether `P183` takes `0`, `1` and `2` as above on WP822 and WP836,
whether the phone in "enabled but not forced" mode offers `a=crypto` on an
`RTP/AVP` line and answers Hello's `RTP/AVP` offer with its own crypto line,
whether a phone set to forced mode accepts Hello's `RTP/SAVP` offer, that a
hold and resume keep the key, and that an `AES_CM_128_HMAC_SHA1_32` answer is
taken when the phone prefers it.
