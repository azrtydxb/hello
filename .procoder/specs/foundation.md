# foundation

Status: complete

Source: `hello-pbx-spec.md` §28 Phase 0 — Foundation. Decided 2026-10-02: the
whole roadmap (Phases 0–7) will be built in phase order, one spec and one
milestone per phase; this spec is Phase 0.

## Problem

Hello exists only as a product vision (`hello-pbx-spec.md`). There is no code,
no build, no runnable topology, and no CI, so no SIP work (Phase 1+) can start
or be verified. Phase 0 lays the skeleton every later phase builds on: a Go
monorepo with the two service binaries, a React/TypeScript UI shell, database
migrations, configuration, structured logs, metrics, health endpoints,
container images, a one-command Docker Compose lab, and a CI pipeline that
gates every change. Getting these conventions right now is cheaper than
retrofitting them across a SIP stack later.

## Users

- **Hello developers** — need `docker compose up` to bring up the full lab,
  `go test ./...` and the UI build to pass locally, and CI to fail on any
  regression.
- **Operators (later phases)** — need consistent health/readiness endpoints,
  JSON logs and Prometheus metrics on every service from day one, because HA
  and draining (Phase 3) depend on them.

## In scope

- [S-1] Go module `github.com/azrtydxb/hello` laid out per spec §26: `cmd/hello-control`, `cmd/hello-sip`, `internal/{config,telemetry,...}` — only packages that have code in this phase.
- [S-2] Configuration loaded from environment variables (`HELLO_*`) into a typed struct, validated at startup; invalid or missing required config exits non-zero with a message naming the key. Bind address and advertised address are distinct settings for hello-sip (spec §18).
- [S-3] Structured JSON logging via stdlib `log/slog`, with a per-service `service` and `node_id` field; a URL-redaction helper so secrets never reach logs (spec §10, §23; verified by `procoder security`).
- [S-4] HTTP ops endpoints on both services: `/healthz` (liveness), `/readyz` (readiness — fails while dependencies are unreachable or the node is draining), `/metrics` (Prometheus).
- [S-5] Prometheus metrics registry with process/Go collectors plus `hello_node_ready` gauge and `hello_build_info`.
- [S-6] Graceful shutdown: on SIGTERM, readiness flips to failing, in-flight HTTP finishes within a configurable timeout, process exits 0.
- [S-7] PostgreSQL migrations under `migrations/`, applied by `hello-control migrate up`; initial schema contains a `schema_info`/revision table only — domain tables arrive with their phases.
- [S-8] hello-control serves `/api/v1/version` (JSON: version, commit, config revision placeholder) and an OpenAPI document at `/api/v1/openapi.json`.
- [S-9] hello-sip binary: starts, loads config, exposes ops endpoints, connects to Valkey for readiness; no SIP listener yet (Phase 1).
- [S-10] React + TypeScript UI in `web/` (Vite, pnpm), with the primary navigation from spec §21 as empty routes and a Dashboard that shows the control-plane version from `/api/v1/version`.
- [S-11] Multi-stage Dockerfiles for hello-control, hello-sip, hello-ui; images run as non-root.
- [S-12] `deploy/docker-compose/` topology per spec §20: hello-ui, hello-control-1/2, hello-sip-1/2, postgres, valkey; `docker compose up` brings it up with migrations applied and all services healthy.
- [S-13] GitHub Actions CI: Go build, `go vet`, golangci-lint, `go test -race`, UI typecheck/lint/build, Docker image builds; pinned action SHAs, timeouts, concurrency cancel.
- [S-14] README with how to build, test, and run the lab.

## Out of scope

- Any SIP handling (listeners, REGISTER, INVITE) — Phase 1.
- Domain tables (extensions, devices, trunks, CDRs) — their phases.
- Authentication/RBAC on the API — the version endpoint is public; auth lands with the first mutable resource.
- Kubernetes manifests/Helm — Phase 6.
- hello-media, Prometheus/Grafana/OTel collector containers — optional, later.
- OpenTelemetry tracing export — logs and metrics only in Phase 0.
- Publishing images to a registry.

## Constraints

- Go 1.27 (installed toolchain); Node 26 for the UI.
- UI package manager: pnpm (decided 2026-10-02), pinned via `packageManager` in `web/package.json` and enabled with corepack in CI and the image.
- Migrations: goose v3, used as a library with migrations embedded in the hello-control binary (decided 2026-10-02); goose's Postgres session locker serializes concurrent runs.
- Prefer stdlib: `net/http` routing, `log/slog`. New dependencies only where stdlib does not cover it: Prometheus client, pgx (PostgreSQL), a Valkey/Redis client, a migration library.
- No required local persistent state in hello-control or hello-sip (spec §4.2).
- Services start in under 2 seconds once dependencies are reachable.
- Secrets (DB password etc.) never appear in logs or API responses — every config value logged at startup goes through the telemetry redaction helper.

## Interfaces

- CLI: `hello-control serve`, `hello-control migrate up|status`, `hello-sip serve`.
- Env: `HELLO_NODE_ID`, `HELLO_HTTP_ADDR`, `HELLO_DATABASE_URL`, `HELLO_VALKEY_ADDR`, `HELLO_SHUTDOWN_TIMEOUT`, `HELLO_DRAIN_DELAY` (time /readyz fails before the listener closes, default 5s), `HELLO_LOG_LEVEL`; hello-sip adds `HELLO_SIP_BIND_ADDR`, `HELLO_SIP_ADVERTISED_ADDR`.
- HTTP (both): `GET /healthz`, `GET /readyz`, `GET /metrics`.
- HTTP (control): `GET /api/v1/version`, `GET /api/v1/openapi.json`.
- UI: served by hello-ui container on port 8080, proxies `/api` to hello-control.

## Data

- PostgreSQL (owned by hello-control): migration bookkeeping table only.
- Valkey: no keys written in Phase 0; connectivity only.
- No local disk state.

## Edge cases

- Two hello-control replicas running `migrate up` concurrently — migrations must take an advisory lock so only one applies.
- Database URL containing a password — must be redacted in startup logs.
- SIGTERM arriving before the HTTP server finished starting.
- Advertised SIP address unset — defaults to bind address only when bind is not `0.0.0.0`/`::`; otherwise startup fails (an unspecified address cannot be advertised).

## Failure modes

- PostgreSQL unreachable at startup: hello-control starts, `/readyz` returns 503 and names the dependency, retries in the background; `/healthz` stays 200.
- Valkey unreachable: same pattern for hello-sip (and hello-control if it uses Valkey).
- Migration failure: `migrate up` exits non-zero, nothing partially applied (each migration in a transaction).
- Invalid config: exit non-zero before binding any port.

## Acceptance criteria

- [ ] [S-1] `procoder test` (which builds `./...`) and `procoder lint` pass and CI produces `hello-control` and `hello-sip` binaries — fails if either `cmd/` main does not compile.
- [ ] [S-2] `TestLoadMissingRequired`, `TestLoadBadDuration` and `TestLoadUnspecifiedAdvertise` in `internal/config` pass — fails if a missing key, a malformed duration, or bind `0.0.0.0` without an advertised address is accepted, or the error omits the key name.
- [ ] [S-3] `TestRedactURL` in `internal/telemetry` passes — fails if a database URL's password survives into the logged string.
- [ ] [S-4] [S-9] `TestReadiness` in `internal/ops` passes — fails if `/healthz` is not 200 while a dependency check fails, or `/readyz` is not 503 naming the failing dependency, or not 200 once all checks pass.
- [ ] [S-5] `TestMetricsEndpoint` in `internal/ops` passes — fails if `/metrics` lacks `hello_node_ready` or `hello_build_info`.
- [ ] [S-6] `TestGracefulShutdown` in `internal/ops` passes — fails if `/readyz` still returns 200 after shutdown begins, or `Serve` does not return nil within the timeout.
- [ ] [S-7] `TestMigrateIdempotentConcurrent` in `test/integration` (PostgreSQL via `HELLO_TEST_DATABASE_URL`) passes — fails if a second or concurrent `migrate up` errors or applies a migration twice.
- [ ] [S-8] `TestVersionAndOpenAPI` in `internal/api` passes — fails if `/api/v1/version` lacks `version`/`commit`, or `/api/v1/openapi.json` is not OpenAPI 3.x JSON describing that path.
- [ ] [S-10] `procoder test` and `procoder lint` pass over `web/` (pnpm typecheck, lint, build, vitest); `Dashboard.test.tsx` fails if the Dashboard does not render the version returned by a mocked `/api/v1/version`.
- [ ] [S-11] `procoder infra` reports no blocking finding for the three Dockerfiles and `TestImagesNonRoot` in `test/integration` (gated by `HELLO_DOCKER=1`) passes — fails if any image builds without a non-root `USER`.
- [ ] [S-12] [S-14] `TestLabSmoke` in `test/integration` (gated by `HELLO_DOCKER=1`) runs `docker compose -f deploy/docker-compose/compose.yaml up -d --wait` and GETs `localhost:8080/api/v1/version` — fails if any service is unhealthy or the curl is not 200; the README documents exactly these steps.
- [ ] [S-13] `.github/workflows/ci.yaml` runs on pull_request and push to main and executes `go test -race ./...` — fails if a red test leaves the job green (verified by `procoder ci`).

## Open questions

<!-- All resolved 2026-10-02; answers in .procoder/ask/answers.md and folded into Constraints and Problem. -->
