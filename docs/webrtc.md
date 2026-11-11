# Browser softphone: web devices, TURN and ICE servers

Hello can ring and place calls in a browser. This page covers what exists
today: a user's **web devices** (credentials for a browser softphone),
**coturn** (STUN and TURN for the media) and the **ICE servers endpoint** the
softphone calls. The WSS edge, the DTLS-SRTP media path and the softphone
itself are separate pieces; this page says where they plug in.

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
