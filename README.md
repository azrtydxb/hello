# Hello

A modern, container-native, SIP-only IP PBX: disposable signaling nodes,
explainable routing, API-driven management, and high availability designed
into the call architecture. The product spec is
[hello-pbx-spec.md](hello-pbx-spec.md).

**Status:** Phase 2 (trunks and routing). Phones register over SIP/UDP
through either SIP node and call each other through a B2BUA. Calls reach the
public network through SIP trunks (registration or IP-authenticated), chosen by
ordered inbound and outbound routes that rewrite numbers and caller ID. Trunks
are health-checked with OPTIONS, fail over to backups, respect concurrency
limits, and every call carries a routing trace in its record. A route tester
explains any number before it goes live. Media flows directly between the
endpoints; anchoring arrives in Phase 5. See [docs/phones.md](docs/phones.md)
for phones, [docs/provisioning.md](docs/provisioning.md) for configuring
them automatically, [docs/trunks.md](docs/trunks.md) for trunks, and
[docs/ai-access.md](docs/ai-access.md) for connecting AI agents over MCP and
[docs/ai-agent.md](docs/ai-agent.md) for the in-product AI agent (assistant,
findings and proposals).

## Layout

| Path                     | What                                                                                               |
| ------------------------ | -------------------------------------------------------------------------------------------------- |
| `cmd/hello-control`      | Management/control-plane service: REST API, migrations                                             |
| `cmd/hello-sip`          | SIP node (Phase 0: ops endpoints and Valkey readiness only)                                        |
| `internal/`              | `config`, `telemetry` (logs, redaction, metrics), `ops` (health/readiness/drain), `api`, `migrate` |
| `migrations/`            | PostgreSQL migrations (goose), embedded in hello-control                                           |
| `web/`                   | React + TypeScript UI (Vite, pnpm)                                                                 |
| `deploy/docker-compose/` | The development lab                                                                                |
| `test/integration/`      | Tests that need PostgreSQL or Docker                                                               |

## Run the lab

Requires Docker with Compose v2.

```sh
docker compose -f deploy/docker-compose/compose.yaml up -d --build --wait
curl localhost:8080/api/v1/version
```

| Service                                     | Host port                                                                                  |
| ------------------------------------------- | ------------------------------------------------------------------------------------------ |
| UI (proxies `/api`)                         | <http://localhost:8080>                                                                    |
| hello-control-1                             | <http://localhost:8081> (`/api/v1/version`, `/api/v1/openapi.json`, `/readyz`, `/metrics`) |
| kamailio                                    | SIP/UDP `5080`: the phone entry point                                                      |
| hello-control-1 provisioning                | <https://localhost:8443> (`/p/…`; TLS from the lab CA in the `provcerts` volume)           |
| hello-sip-1, hello-sip-2                    | <http://localhost:8082>, <http://localhost:8083> (`/readyz`, `/metrics`)                   |
| hello-sip-1, hello-sip-2                    | SIP/UDP `5060`, `5062` (direct, bypassing Kamailio; for tests)                             |
| carrier-primary, carrier-backup             | `8091`, `8092` (simulated carriers)                                                        |
| hello-control-2, postgres, Valkey, Sentinel | none                                                                                       |

The lab runs on two networks:

| Network | Members                                                                                                                                                                                    |
| ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `edge`  | `10.89.53.0/24`: kamailio (`10.89.53.10`, also its Hello-facing address), hello-sip-1 (`.11`), hello-sip-2 (`.12`), hello-control, the UI, the carriers. Every host port is published here |
| `state` | PostgreSQL; Valkey (`valkey-1` primary at start, `valkey-2` replica); `sentinel-1`..`sentinel-3` (master set `hello`); and the Hello services that use them                                |

Kamailio's metrics are on port 9090 inside `edge` only. See [docs/ha.md](docs/ha.md)
for the HA topology and operations.

Sign in as `admin` with the lab-only password `hello-lab-admin`.

Stop and remove it with `docker compose -f deploy/docker-compose/compose.yaml down -v`.

## Develop

```sh
go build ./... && go test -race ./...
golangci-lint run ./...

cd web && pnpm install && pnpm typecheck && pnpm lint && pnpm test && pnpm dev
```

`pnpm dev` proxies `/api` to `localhost:8081`, so run the lab, or
`hello-control serve`, alongside it.

Integration tests skip unless they are enabled. The migration test creates
and drops its own scratch database, so the URL needs `CREATEDB` rights:

```sh
HELLO_TEST_DATABASE_URL=postgres://user:pass@localhost:5432/db?sslmode=disable go test ./test/integration/
HELLO_DOCKER=1 go test -timeout 20m ./test/integration/   # images, and SIP flows against the lab
```

## Configuration

Both services read `HELLO_*` environment variables and exit at startup,
naming the key, if one is missing or malformed.

| Variable                                          | Service | Default                                                                                                                       |
| ------------------------------------------------- | ------- | ----------------------------------------------------------------------------------------------------------------------------- |
| `HELLO_NODE_ID`                                   | both    | required                                                                                                                      |
| `HELLO_HTTP_ADDR`                                 | both    | `:8081` control, `:8082` sip                                                                                                  |
| `HELLO_LOG_LEVEL`                                 | both    | `info`                                                                                                                        |
| `HELLO_SHUTDOWN_TIMEOUT`                          | both    | `30s`                                                                                                                         |
| `HELLO_DRAIN_DELAY`                               | both    | `5s`: how long `/readyz` fails before the listener closes                                                                     |
| `HELLO_DATABASE_URL`                              | both    | required; hello-sip reads devices and writes call records                                                                     |
| `HELLO_VALKEY_ADDR`                               | both    | required unless Sentinel is set; single Valkey: registrations, active calls, trunk state; needs Valkey 9+ (hash field expiry) |
| `HELLO_VALKEY_SENTINELS`                          | both    | unset; comma-separated Sentinel `host:port` list, instead of `HELLO_VALKEY_ADDR`                                              |
| `HELLO_VALKEY_MASTER`                             | both    | unset; Sentinel master set name, required with `HELLO_VALKEY_SENTINELS`                                                       |
| `HELLO_SIP_DOMAIN`                                | both    | required; digest realm and AOR host, identical everywhere                                                                     |
| `HELLO_SECRET_KEY`                                | both    | required: 32 bytes, base64; encrypts trunk passwords; identical on every node                                                 |
| `HELLO_BOOTSTRAP_ADMIN_PASSWORD`                  | control | unset; creates user `admin` when no user exists                                                                               |
| `HELLO_SESSION_TTL`                               | control | `12h`                                                                                                                         |
| `HELLO_SIP_BIND_ADDR`                             | sip     | `0.0.0.0:5060`                                                                                                                |
| `HELLO_SIP_ADVERTISED_ADDR`                       | sip     | the bind address, which must then be a specific IP                                                                            |
| `HELLO_SIP_NONCE_SECRET`                          | sip     | required, at least 32 bytes, identical on every SIP node                                                                      |
| `HELLO_SIP_REGISTER_MIN_EXPIRES` / `_MAX_EXPIRES` | sip     | `60s` / `1h`                                                                                                                  |
| `HELLO_SIP_RING_TIMEOUT`                          | sip     | `30s`                                                                                                                         |
| `HELLO_SIP_MAX_CALL_DURATION`                     | sip     | `4h`; a call with no BYE (phone gone) is cleared after this                                                                   |
| `HELLO_SIP_AUTH_FAIL_LIMIT` / `_WINDOW`           | sip     | `10` failures per `5m` per source IP; set the limit on control too (Diagnostics shows which sources it blocks)                |
| `HELLO_SIP_STATE_TIMEOUT`                         | sip     | `200ms`; Valkey calls while handling SIP                                                                                      |
| `HELLO_SIP_TRUSTED_PROXIES`                       | sip     | unset (trust none); CIDRs of the SIP balancers (Kamailio) whose `Path` and client address are believed                        |
| `HELLO_DRAIN_TIMEOUT`                             | sip     | `2h`; a draining node hangs up remaining calls after this                                                                     |
| `HELLO_MEMBER_HEARTBEAT`                          | sip     | `1s`; how often the node refreshes its cluster membership in Valkey (at most 1.33s: a third of the 4s membership TTL)         |
| `HELLO_PROV_PUBLIC_URL`                           | control | unset (off); the `https://` host phones reach; boot and CA URLs use its plain-HTTP form                                       |
| `HELLO_PROV_ADDR`                                 | control | `:8083`; the provisioning listener                                                                                            |
| `HELLO_PROV_SIP_SERVER`                           | control | `HELLO_SIP_ADVERTISED_ADDR`; the `host:port` provisioned phones register with (Kamailio)                                      |
| `HELLO_PROV_TRUSTED_PROXIES`                      | control | unset; CIDRs of the reverse proxies whose `X-Forwarded-Proto`/`-For` are believed                                             |
| `HELLO_PROV_CA_CERT`                              | control | unset; PEM served at `/p/ca.crt` for phones to trust                                                                          |
| `HELLO_PROV_TLS_CERT` / `_TLS_KEY`                | control | unset; TLS on the listener itself, when no proxy terminates it                                                                |
| `HELLO_PROV_BOOT_CIDRS`                           | control | unset (any); sources allowed the DHCP trust-on-first-use hand-off                                                             |
| `HELLO_PROV_TOKEN_GRACE` / `_RESYNC`              | control | `7d` / `24h`; old-token grace after a rotation; the phones' re-check interval                                                 |
| `HELLO_PROV_TIMEZONE` / `_NTP`                    | control | `UTC` / `pool.ntp.org`                                                                                                        |
| `HELLO_PROV_AUDIT_RETENTION`                      | control | `90d`; how long the fetch audit is kept                                                                                       |
| `HELLO_PROV_RATE_*`                               | control | `IP_PER_MIN` `60`, `DENIED_PER_10MIN` `10`, `PHONE_PER_HOUR` `30`                                                             |
| `HELLO_PROV_SNOM_*` etc.                          | control | unset; vendor redirect credentials (`_SNOM_`, `_YEALINK_`, `_YMCS_`, `_GDMS_`), see docs/provisioning.md                      |
| `HELLO_PUBLIC_URL`                                | control | unset; https:// base of Hello for OAuth and `/mcp` (off when unset), see docs/ai-access.md                                    |
| `HELLO_OAUTH_ACCESS_TTL`                          | control | `1h`; OAuth access token lifetime                                                                                             |
| `HELLO_OAUTH_REFRESH_TTL` / `_REFRESH_MAX`        | control | `30` / `90` days; refresh token idle and absolute limits                                                                      |
| `HELLO_OAUTH_DCR`                                 | control | `false`; `true` allows dynamic client registration                                                                            |
| `HELLO_OAUTH_CIMD_ALLOW_PRIVATE`                  | control | `false`; `true` fetches client ID metadata from private addresses                                                             |
| `HELLO_MCP_ALLOWED_ORIGINS`                       | control | unset; browser origins allowed on `/mcp`                                                                                      |

The UI container proxies `/api/`, `/mcp`, `/oauth/` (except the console's `/oauth/consent`) and `/.well-known/oauth-` to `HELLO_CONTROL_UPSTREAM` and re-resolves it through `HELLO_DNS_RESOLVER` (default `127.0.0.11`, Docker's DNS; use your cluster DNS elsewhere).

Every SIP node must share the same `HELLO_SIP_NONCE_SECRET`. A node with a
different one rejects digest challenges issued by the others.

Commands: `hello-control serve | migrate up | migrate status | user add [--role viewer|operator|admin] <username>` (password on stdin; the role defaults to `viewer`, the first user is always `admin`), `hello-sip serve`.

## License

Apache-2.0. See [LICENSE](LICENSE).
