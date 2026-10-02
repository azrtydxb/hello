# Hello

A modern, container-native, SIP-only IP PBX: disposable signaling nodes,
explainable routing, API-driven management, and high availability designed
into the call architecture. The product spec is
[hello-pbx-spec.md](hello-pbx-spec.md).

**Status:** Phase 1 (minimum PBX). Phones register over SIP/UDP through
either SIP node with digest authentication, and extensions call each other
through a B2BUA, with the media flowing directly between the phones.
Administrators sign in to manage extensions and devices and to see live
registrations, active calls and call records. Trunks and routing arrive in
Phase 2. To connect real phones, see [docs/phones.md](docs/phones.md).

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

| Service                  | Host port                                                                                  |
| ------------------------ | ------------------------------------------------------------------------------------------ |
| UI (proxies `/api`)      | <http://localhost:8080>                                                                    |
| hello-control-1          | <http://localhost:8081> (`/api/v1/version`, `/api/v1/openapi.json`, `/readyz`, `/metrics`) |
| hello-sip-1, hello-sip-2 | <http://localhost:8082>, <http://localhost:8083> (`/readyz`, `/metrics`)                   |
| hello-sip-1, hello-sip-2 | SIP/UDP `5060`, `5062`                                                                     |

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

| Variable                                          | Service | Default                                                     |
| ------------------------------------------------- | ------- | ----------------------------------------------------------- |
| `HELLO_NODE_ID`                                   | both    | required                                                    |
| `HELLO_HTTP_ADDR`                                 | both    | `:8081` control, `:8082` sip                                |
| `HELLO_LOG_LEVEL`                                 | both    | `info`                                                      |
| `HELLO_SHUTDOWN_TIMEOUT`                          | both    | `30s`                                                       |
| `HELLO_DRAIN_DELAY`                               | both    | `5s`: how long `/readyz` fails before the listener closes   |
| `HELLO_DATABASE_URL`                              | both    | required; hello-sip reads devices and writes call records   |
| `HELLO_VALKEY_ADDR`                               | both    | required; registrations and active calls                    |
| `HELLO_SIP_DOMAIN`                                | both    | required; digest realm and AOR host, identical everywhere   |
| `HELLO_BOOTSTRAP_ADMIN_PASSWORD`                  | control | unset; creates user `admin` when no user exists             |
| `HELLO_SESSION_TTL`                               | control | `12h`                                                       |
| `HELLO_SIP_BIND_ADDR`                             | sip     | `0.0.0.0:5060`                                              |
| `HELLO_SIP_ADVERTISED_ADDR`                       | sip     | the bind address, which must then be a specific IP          |
| `HELLO_SIP_NONCE_SECRET`                          | sip     | required, at least 32 bytes, identical on every SIP node    |
| `HELLO_SIP_REGISTER_MIN_EXPIRES` / `_MAX_EXPIRES` | sip     | `60s` / `1h`                                                |
| `HELLO_SIP_RING_TIMEOUT`                          | sip     | `30s`                                                       |
| `HELLO_SIP_MAX_CALL_DURATION`                     | sip     | `4h`; a call with no BYE (phone gone) is cleared after this |
| `HELLO_SIP_AUTH_FAIL_LIMIT` / `_WINDOW`           | sip     | `10` failures per `5m` per source IP                        |
| `HELLO_SIP_STATE_TIMEOUT`                         | sip     | `200ms`; Valkey calls while handling SIP                    |

Every SIP node must share the same `HELLO_SIP_NONCE_SECRET`. A node with a
different one rejects digest challenges issued by the others.

Commands: `hello-control serve | migrate up | migrate status`, `hello-sip serve`.

## License

Apache-2.0. See [LICENSE](LICENSE).
