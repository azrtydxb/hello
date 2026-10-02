# trunks-routing — implementation plan

Status: draft
Spec: .procoder/specs/trunks-routing.md

## Goal

Hello places and receives calls through SIP trunks. It chooses each one with structured inbound and outbound routes, rewrites numbers and caller ID, health-checks trunks and fails over between them, and stores an explainable routing trace on every call.

## Architecture

A pure routing engine (`internal/routing`) compiles trunks and routes into a Table and decides each call, recording a trace. hello-control's route tester and hello-sip's call path both use it, so they produce identical traces.

- **hello-control** owns the schema (`migrations/00003_trunks_routing.sql`), the trunk and route APIs with validation, and trunk-password sealing (`internal/secret`).
- **hello-sip** owns:
  - adding routes and trunks to the revisioned snapshot
  - trunk registration and OPTIONS under a Valkey lease
  - trunk legs on the B2BUA (outbound with carrier digest, inbound with source validation)
  - failover and concurrency slots
  - caller ID and the CDR routing fields
- **Shared Valkey state** for trunks goes through `internal/livestate/trunks.go`.

## Constraints

- No PostgreSQL call on the SIP path. The routing Table is compiled once per snapshot revision.
- Routing decision under 1ms p99 for 100 routes.
- Regexes are RE2 and at most 500 characters.
- Trunk passwords, `HELLO_SECRET_KEY`, nonce secrets, Authorization headers and `hflow` tokens never appear in logs, API responses after creation, metrics, CDRs or traces. A trace says "credentials: configured".
- Determinism: route order is the `position` column, trunk order is `outbound_route_trunks.position`, and of two equally matching inbound routes the earlier wins.
- Each task:
  - leaves `gofmt`, `go vet ./...`, `golangci-lint run ./...` and `go test -race ./...` clean, with the test databases set
  - leaves `procoder check` with 0 blocking findings
  - gives every security-relevant or non-trivial behaviour a test that fails without it, mutation-checked (snapshot immediately before, restore immediately after, `cmp`)
- The REVIEW.md rubric applies, including the 2026-10-02 additions on atomic state changes, replay, readiness, redaction and background writers.

### Shared contracts (fixed; a stream that needs a change asks the lead and never edits another stream's files)

1. **Schema:** `migrations/00003_trunks_routing.sql`, as committed.
2. **Routing types and API:** `internal/routing/types.go`, as committed: `Config`, `Trunk`, `OutboundRoute`, `InboundRoute`, `Transform`, `Schedule`, `Call`, `TrunkUsability`, `Decision`, `Candidate`, `Trace`, `Step`, `FieldError`. The functions in its trailing comment (`Compile`, `Table.Decide`, `Table.Trunk`, `Table.TrunkForSource`, `ApplyTransform`) are implemented by Task 2 with exactly those signatures.
3. **Secret sealing:** `internal/secret`. `Box.Seal(password, "trunk:<id>")` and `Box.Open(sealed, "trunk:<id>")`. hello-control seals on create and update, after the INSERT returns the id (in the same transaction); hello-sip opens when it loads the snapshot.
4. **Trunk live state:** `internal/livestate/trunks.go`, as committed:
   - `AcquireLease` / `ReleaseLease` with `TrunkLeaseKey(id)`
   - `PutTrunkRegistration`, `PutDestinationHealth`
   - `AcquireTrunkCall` / `RefreshTrunkCall` / `ReleaseTrunkCall`
   - `TrunkStatus`
5. **Configuration revision and NOTIFY:** as in Phase 1. Every trunk, destination, route and order change, plus every extension `externalNumber` change, writes an audit row and bumps `config_revision` with `pg_notify('hello_config', rev)`.
6. **Trace text:** each Step is one English sentence, written by `routing` and, for attempts, by hello-sip with `Trace.Add`. Examples:
   - `Internal extension lookup "0501234567" -> no match`
   - `Route "UAE Mobile" matched (regex ^05[0-9]{8}$)`
   - `Rewrite 0501234567 -> +971501234567`
   - `Trunk carrier-primary skipped: unhealthy`
   - `carrier-primary (10.0.0.5:5060) -> 503 Service Unavailable`
   - `Failover permitted for 503`
   - `carrier-backup -> 200 OK`
   - `Call established`
7. **HTTP JSON.** camelCase, with the same error envelope and list shape as Phase 1. A 400 from validation is `{"error":{"code":"bad_request","message":"...","fields":[FieldError...]}}`.
   - **Trunk:** `{"id","name","mode","username","hasPassword","realm","fromDomain","registerExpires","optionsInterval","sourceCidrs":[...],"maxCalls","defaultCallerId","enabled","destinations":[{"host","port","priority","weight"}],"createdAt","updatedAt"}`. `password` is accepted on POST and PATCH and never returned; `hasPassword` reports whether one is set.
   - **Trunk status:** `GET /api/v1/trunks/status` → `{"items":[livestate.TrunkStatus + "name"]}`.
   - **Outbound route:** `{"id","position","name","matchKind","match","sourceExtensions","schedule":Schedule|null,"numberTransform":Transform,"callerIdTransform":Transform,"trunks":[trunkId...],"failoverCodes","emergency","enabled"}`.
   - **Inbound route:** `{"id","position","name","didKind","did","trunkId"|null,"sipDomain","headerName","headerRegex","schedule","callerIdTransform","destinationKind","destination","enabled"}`.
   - **Reordering:** `PUT /api/v1/routes/{outbound|inbound}/order` takes `{"ids":[...]}` (a full permutation) and returns 204.
   - **Route tester:** `POST /api/v1/routing/test` takes `{"from":"<extension number>"|"trunk:<id>","number":"...","callerId":"...","at":"RFC3339?"}` and returns `{"decision":{"kind","extension","sipUri","number","callerId","route","trunks":[names in try order],"rejectCode","reason"},"trace":[Step...]}`. Trunk usability in the tester comes from live state when Valkey is reachable. Otherwise it treats every enabled trunk as usable and adds a trace step saying so.
   - **CDRs:** CDR JSON gains `direction`, `originalDestination`, `rewrittenDestination`, `route`, `trunk`. `GET /api/v1/cdrs/{id}` returns the CDR plus `"trace":[Step...]` and `"explanation"` (for failed calls, the reason of the last trace step).
   - **Extensions:** gain `externalNumber`.

## Task 1: Shared contracts (lead)

Files: `migrations/00003_trunks_routing.sql`, `internal/routing/types.go`, `internal/secret/`, `internal/livestate/trunks.go` and `trunks_test.go`, `internal/config` (`HELLO_SECRET_KEY`), `deploy/docker-compose/compose.yaml` (lab key), this plan.
Interfaces: everything listed in Shared contracts.

- [x] Write the migration and confirm it applies with `HELLO_TEST_DATABASE_URL=… go test -run Migrate ./test/integration/` → ok.
- [x] Write `internal/secret` and run `go test ./internal/secret/` → `TestSealOpen` passes.
- [x] Write the livestate trunk state and run `HELLO_TEST_VALKEY_ADDR=… go test -run Trunk ./internal/livestate/` → the lease, slot and status tests pass.
- [x] Add `HELLO_SECRET_KEY` to config and run `go test ./internal/config/` → ok.
- [x] Commit to `phase-2-trunks-routing`, then branch `phase-2-routing`, `phase-2-control`, `phase-2-sip` and `phase-2-ui`, each in its own worktree.

## Task 2: Routing engine (branch phase-2-routing)

Files: `internal/routing/engine.go` (Compile, Table, Decide), `transform.go`, `schedule.go`, `engine_test.go`, `bench_test.go`.
Interfaces: produces the functions in `types.go`'s trailing comment; consumes nothing outside the stdlib.

- [ ] Implement `Compile` with full validation, returning a FieldError for each problem. RE2 only, a 500-character limit, and templates may reference only groups the regex defines (numbered and named). Destination kinds are checked; `extension` must exist in `Config.Extensions`.
- [ ] Implement `ApplyTransform` (strip, then prefix, then regex template). A result containing anything but `+`, digits, `*` and `#` is an error.
- [ ] Implement `Schedule.Open(at)` with the time zone, cross-midnight windows and DST, and test both DST transitions in `Europe/Berlin`.
- [ ] Implement `Decide`:
  - **From an extension:** look up the internal extension; then match outbound routes in position order, checking enabled, source extensions, schedule and pattern; then rewrite the number and caller ID (fallbacks: the extension's external number, then the trunk default, then the extension number); then build the candidates from usability and order the destinations by priority, then weight (deterministic: a weighted order seeded by a hash of the call's number). With no usable trunk, return KindReject 503 with a reason.
  - **From a trunk:** match inbound routes in position order (enabled, trunk, DID with `+` and whitespace normalised, SIP domain, header regex, schedule). An `external` destination recurses into outbound routing as the trunk.
  - **Unmatched:** reject with 404.
  - **Trace:** every step is traced.
- [ ] Implement `TrunkForSource`: the lowest-ID enabled trunk whose `SourceCIDRs` or `ResolvedIPs` contain the IP.
- [ ] Write `TestRouteMatchAndRewrite`, a table of at least 40 cases covering every branch above, each asserting the decision and the exact trace text. Write `TestRoutingDecisionLatency` (`HELLO_BENCH=1`; 100 routes, p99 under 1ms) and `TestCompileValidation`. Run mutation checks on the template group check, schedule `Open` and the failover-code defaults.
- [ ] Run `go test -race ./internal/routing/` (pass), then lint and `procoder check`.

## Task 3: Control plane (branch phase-2-control)

Files: `internal/store/` (trunks, destinations, routes, order, external number, CDR fields and trace), `internal/api/` (handlers, OpenAPI, tests), `cmd/hello-control/main.go` (secret box, route tester wiring).
Interfaces: produces the HTTP JSON in contract 7. Consumes `routing.Compile` and `Decide` (stub them against `types.go` until Task 2 lands; the merge brings the real engine), `secret.Box` and `livestate.TrunkStatus`.

- [ ] Write the store functions. Each mutation runs in one transaction with the audit row and the revision bump plus NOTIFY. The trunk password is sealed with AAD `trunk:<id>` and never selected into API responses. Reorder takes a full permutation inside one transaction, using the deferred unique constraint.
- [ ] Validate in two stages:
  - **Field level:** names, CIDRs, ports, codes.
  - **Whole configuration:** `routing.Compile` on the saved configuration plus the change, and any FieldError rejects the change with 400 and `fields`.
- [ ] Write the handlers for every route in contract 7, plus the OpenAPI entries. `TestVersionAndOpenAPI` must still route every documented operation.
- [ ] Build the route tester: load the configuration (as hello-sip's snapshot would), `Compile`, then `Decide` with live usability. It has no side effects; `TestRoutingTestEndpoint` asserts no writes, using row counts and the audit table.
- [ ] Write `TestTrunkCRUDSecretHidden`, `TestRouteValidation`, `TestRoutingTestEndpoint`, and a CDR detail test covering `trace` and `explanation`. Add a store integration test for reorder atomicity.
- [ ] Run the full gate.

## Task 4: SIP trunks (branch phase-2-sip)

Files: `internal/snapshot/` (load trunks, destinations, routes and external numbers; open passwords with `secret.Box`; build `routing.Config`; `Compile` per revision, keeping the last good Table when Compile fails and logging it loudly), `internal/sip/` (trunk registrar client, OPTIONS health, trunk B2BUA legs, failover, slots, caller ID, metrics, source validation, the trace), `internal/cdr/` (new fields and trace), `cmd/hello-sip/main.go`.
Interfaces: consumes `routing.Table` and `livestate` trunk functions; produces SIP to and from carriers, trunk metrics and CDR fields.

- [ ] Build the snapshot: the Table, plus `ResolvedIPs` refreshed every 30s by DNS off the request path. A sealed password that doesn't open marks that trunk `misconfigured`; it is not dropped silently.
- [ ] Trunk lease loop: for each enabled trunk, try `AcquireLease(TrunkLeaseKey(id), nodeID, 3×refresh)` and renew while held. The holder REGISTERs (`registration` mode, answering digest with the trunk credentials, re-registering at 80% of expiry, backing off exponentially to 10 minutes on failure) and sends OPTIONS to every destination at the interval. It publishes `PutTrunkRegistration` and `PutDestinationHealth`, and releases the lease on shutdown.
- [ ] Outbound: when `Decide` returns `KindOutbound`, try candidates in order:
  - `AcquireTrunkCall` before each trunk; a full trunk is skipped and traced
  - INVITE to each destination, answering 401/407 with the trunk credentials
  - on a failover code or a timeout, try the next destination, then the next trunk
  - the caller's CANCEL stops failover
  - release the slot on end, and refresh it with the heartbeat
  - present caller ID from the Decision, with From in the trunk's From domain
- [ ] Inbound: an INVITE that does not come from a registered phone goes through `TrunkForSource(source IP)`. With no trunk it gets 403 and counts towards the throttle. Otherwise `Decide` from that trunk and route to the extension (the Phase 1 fork logic), to an external number (outbound), or to a SIP URI.
- [ ] CDRs and trace: record direction, original and rewritten destination, route, trunk and the full trace in the CDR.
- [ ] Metrics: everything in spec S-14.
- [ ] Write unit tests with in-process fake carriers (a sipgo UAS on loopback) for registration, lease takeover, OPTIONS up/down, failover 503 to backup, 486 not failed over, the 407 challenge, the concurrency limit, CANCEL during failover, inbound source validation and `TestTrunkMetrics`, mutation-checked.
- [ ] Run the full gate and the lab suite. Phase 1 lab tests must still pass.

## Task 5: UI (branch phase-2-ui)

Files: `web/src/` (api, pages `Trunks`, `Routes`, `RouteTest`, `CallDetail`, nav, tests).
Interfaces: consumes contract 7 only.

- [ ] Trunks page: a list with live status (polling `/trunks/status` every 5s) and a create/edit form. The password field is write-only: it is shown empty with "set" or "not set", and changing it is explicit.
- [ ] Routes page: inbound and outbound tabs with ordered lists, up and down controls, and `PUT …/order`. Transform and schedule editors, with server field errors shown on the field.
- [ ] Route tester page: from, number and optional time inputs; shows the decision and the trace.
- [ ] Call History row links to `/history/{id}`, which shows the CDR fields, the trace steps in order and the explanation.
- [ ] Write `Trunks.test.tsx`, `Routes.test.tsx` and `CallDetail.test.tsx` from the spec criteria, mutation-checked.
- [ ] Run `pnpm typecheck`, `lint`, `test` and `build`, then `procoder check`.

## Task 6: Simulated carrier, lab, docs (lead, branch phase-2-trunks-routing)

Files: `test/fakecarrier/` (binary and Dockerfile target), `deploy/docker-compose/compose.yaml` (`carrier-primary` and `carrier-backup`), `test/integration/lab_trunk_test.go`, `docs/trunks.md`, `README.md`.
Interfaces: consumes everything above.

- [ ] Build the fake carrier: a registrar with digest (the user and password come from env); OPTIONS answers; outcomes keyed on the dialled number's last digits (`...00` answers, `...86` gives 486, `...03` gives 503, `...08` never answers); an optional 407 challenge on INVITE (env); and an HTTP control endpoint, `POST /call {"from","to","target"}`, that places an inbound INVITE to Hello and answers. It also records every received request for test inspection (`GET /log`).
- [ ] Merge the routing, control, sip and ui branches, resolving conflicts hunk by hunk. Run the full gate.
- [ ] Write the lab tests from spec S-2, S-3, S-5/S-6, S-8 to S-11 and S-16, and extend `TestNoSecretsInLogs`.
- [ ] Write `docs/trunks.md` (adding a real trunk and running the manual check) and update the README configuration table.
- [ ] Run `HELLO_DOCKER=1 go test -timeout 25m ./test/integration/` (pass), then the full gate.
