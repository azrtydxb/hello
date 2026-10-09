# trunk-internal-numbers — implementation plan

Status: approved (open questions answered 2026-10-09)
Spec: .procoder/specs/trunk-internal-numbers.md

## Goal

An inbound trunk call can ring an internal extension by dialing its number: a new `internal` destination kind looks the normalised DID up in the extension namespace, a per-trunk `internal_dialing` policy (off by default, patterns or all) gates what each trunk may dial, the loop guards stop a trunk call from leaving through its own trunk, and a caller-ID ladder keeps trunk-to-internal calls attributable on the phones. One route serves every extension; the consuming product connects as an IP-authenticated trunk.

## Architecture

All server changes stay in the packages phase 2 built; nothing new is introduced.

- **`internal/routing`** carries the change: `Config` and `Trunk` gain the compiled policy (`internalDialing`), `InboundRoute.DestinationKind` gains `internal`, and `fromTrunk` walks the policy once after DID normalisation, then looks the DID up in `t.extensions` when the route's destination is `internal`. The loop guard lives beside `outboundRoute`: the source trunk is filtered from candidates and a second outbound re-entry rejects 403. The caller-ID ladder replaces the bare "as received" presentation for inbound-to-extension calls. The engine stays pure; the trace texts are fixed strings in the package.
- **`internal/sip`**: `inbound.go` passes nothing new (the Request-URI user is already `Call.Number`); the B2BUA applies the decision's caller ID to the internal leg; nothing else moves.
- **`internal/api`**: the trunk handlers validate `internalDialing` (mode, pattern compile, count, confirm-on-all) and the route tester accepts `"trunk:<id>"` as `from`. Thin handlers calling the routing compile, as today.
- **`internal/snapshot`/store/migrate**: `trunks.internal_dialing jsonb default '{"mode":"off"}'` rides the revisioned snapshot compile; a pattern that fails to compile despite save-time validation marks the trunk `misconfigured` (the existing state, reused).
- **`web/`**: the trunk form gains the policy fields with server-side validation surfaced per field; route and call-detail render the new trace steps.
- **`deploy/kamailio`**: expected unchanged; a drift assertion (S-7) pins the Request-URI forwarding so the requirement is checked, not assumed.
- **`test/fakecarrier`**: its control endpoint gains "place an inbound call addressed to `<user@host>`" if it cannot already target an arbitrary user (phase 2 keyed on the DID; S-8 needs the Request-URI user to be the extension number).

## Constraints

- Default-deny and upgrade-safe: migration default `{"mode":"off"}`; no existing trunk gains reach by the upgrade; the snapshot compile rejects nothing new at revision time.
- Lands after the voice-agent removal: `internal/routing` loses `voice_agent`/`VoiceAgents`/`VoiceSIPAddress`/`VoiceAgentExtensions` in the removal PR; this plan builds on the post-removal `Config`.
- Each task leaves `gofmt`, `go vet ./...`, `golangci-lint run ./...`, `go test -race ./...` (test databases set), `procoder check` 0 blocking, and `procoder test`/`procoder lint` over `web/` where touched green; CI on the Arc runners; kw via Kuvryn Sync; nothing on the user's Mac.
- The route tester and hello-sip share the engine, so `TestRoutingTestFromTrunk` holds without a second decision path.
- Open questions answered 2026-10-09: trunk peers authenticate by pinned source IPs (digest REGISTER stays out of scope, a possible later milestone), the peer's per-call name/number is the caller ID with the ladder as fallback, the extension number is the dialing contract (no DID map), and out-of-policy dials get 404. No task changes shape; Task 2 stands as written.

## Tasks

Task-per-stream where the contracts allow it; task order is merge order.

## Task 1: `internal/routing` engine (branch trunk-internal-engine)

Files: `internal/routing/types.go`, `engine.go`, `inbound*.go`, `engine_test.go`, `fuzz_test.go`, `bench_test.go`.
Interfaces: produces contracts 3 and 6 below; consumes contract 2.

The policy type and compile, the `internal` destination, the guards, the caller-ID ladder, unit tests `TestInternalDestination`, `TestTrunkInternalDialingPolicy`, `TestLoopPrevention` (and the fuzz and bench updated to the new `Config` fields).

- [ ] `Dialing` compile: exact, `X`-pattern (compile to an anchored `[0-9]` class per `X`), or anchored regex, each ≤500 chars, ≤100 patterns; compile errors are `FieldError`s on `trunks[i].internalDialing.patterns[j]`.
- [ ] `fromTrunk` order: normalise DID → policy verdict (traced) → route walk → destination; the `internal` destination looks the normalised DID up in `t.extensions` (enabled extensions only, matching the phone path).
- [ ] Loop guard: in `outboundRoute` when `src.trunk != nil`, filter the source trunk from `candidates` with a traced skip; a candidates-empty result from that filter is 403 "loop prevented"; a call-carried hop flag makes re-entry a 403 invariant break. `Call` gains nothing — the hop count lives in the decision path, which is one pass today.
- [ ] Caller-ID ladder on inbound-to-extension calls: received → trunk default → `<trunk-name>`; transform applied after, as today; traced.
- [ ] `go test -race ./internal/routing/` green with the three new tests; `gofmt`, `go vet`, `golangci-lint` clean; `procoder check` 0 blocking.

## Task 2: Migration and trunk validation (branch trunk-internal-api, after Task 1)

Files: `migrations/000NN_trunk_internal_dialing.sql` (and its down), `internal/api/trunks*.go`, `internal/api/*_test.go`, `internal/snapshot` (trunk compile).
Interfaces: consumes contracts 4 and 5; produces nothing new.

Migration `000NN_trunk_internal_dialing.sql` with its down; `internal/api` trunk handlers validate `internalDialing` and answer 400 with the failing field; `TestTrunkDialingValidation`; the snapshot compile marks a miscompiled trunk `misconfigured`.

- [ ] Write the migration, confirm it applies and downgrades with `HELLO_TEST_DATABASE_URL=… go test -run Migrate ./test/integration/`.
- [ ] Trunk handler validation per contract 5 (mode set, pattern compile, ≤500 chars, ≤100, `patterns` non-empty iff mode `patterns`, `confirm: true` for mode `all`), 400 with the failing field.
- [ ] Snapshot compile marks a miscompiled trunk `misconfigured`; its routes skip.
- [ ] `TestTrunkDialingValidation` passes; full gate green (`go test -race ./...`, lint, `procoder check`).

## Task 3: Route tester and SIP caller ID (branch trunk-internal-sip, after Task 1)

Files: `internal/api/routingtest*.go` and its tests, `internal/sip/inbound.go`, the B2BUA files carrying caller ID to the internal leg, `test/integration` CDR/trace tests.
Interfaces: consumes contracts 3, 5 and 6.

`"trunk:<id>"` in the route tester (`TestRoutingTestFromTrunk`); the B2BUA presents the ladder's caller ID on the internal leg (`TestInternalCallerIDLadder`); CDR fields asserted (`TestTrunkInternalCDR`).

- [ ] Route tester accepts `from: "trunk:<id>"` and returns the same trace a real call would produce; writes nothing.
- [ ] B2BUA applies the ladder's choice to the internal leg; the route transform applies after, as today.
- [ ] `TestRoutingTestFromTrunk`, `TestInternalCallerIDLadder`, `TestTrunkInternalCDR` pass; full gate green.

## Task 4: Lab and edge (branch trunk-internal-lab, after Tasks 2 and 3)

Files: `test/fakecarrier/`, `test/integration/lab*`, `test/deploy` (kamailio drift tests), `docs/trunks.md`.
Interfaces: consumes nothing new; exercises Tasks 1–3 end to end.

`test/fakecarrier` arbitrary-user inbound control; `TestTrunkInternalLab` (ring, answer, audio both ways, out-of-policy 404); the kamailio drift assertion `TestKamailioTrunkURI`; the trunk guide (`docs/trunks.md`) gains a section "Let a trunk dial internal numbers".

- [ ] Fakecarrier control endpoint places an inbound call addressed to an arbitrary Request-URI user.
- [ ] `TestTrunkInternalLab` (`HELLO_DOCKER=1`): INVITE to `sip:1012@…` on a `1XX` trunk rings 1012, audio both ways; out-of-pattern DID rejected 404; trace shows the verdict.
- [ ] `TestKamailioTrunkURI` asserts the trunk path forwards the Request-URI user unchanged.
- [ ] Docs section "Let a trunk dial internal numbers" in `docs/trunks.md`.

## Task 5: Console (branch trunk-internal-ui, after Tasks 2 and 3)

Files: `web/src/pages/` (trunk form, route tester, route/call detail), their `*.test.tsx`, `web/src/api/`.
Interfaces: consumes contract 5 (API shapes) and contract 6 (trace texts matched by text, never index).

The trunk form policy fields, trace rendering (`procoder test` and `procoder lint` over `web/`); the route-tester page accepts a trunk source.

- [ ] Trunk form: mode radio, pattern list with live validation, server-side field errors surfaced.
- [ ] Route tester page accepts a trunk source; route and call detail render the new trace steps in order.
- [ ] `procoder test web/` and `procoder lint web/` green.

## Shared contracts (fixed; a stream that needs a change asks the lead and never edits another stream's files)

1. **Config** (`internal/config`): nothing new — the policy is per-trunk data, not environment.
2. **Routing types** (`internal/routing/types.go`): `type Dialing struct{ Mode string; Patterns []string }` on `Trunk.InternalDialing` (zero value: mode `off`); `InboundRoute.DestinationKind` gains `"internal"`; `Decision` unchanged — `internal` sets `Kind` `inbound` and `Extension`.
3. **Policy verdict** (`internal/routing`): `func (t *Table) dialingAllows(tr *Trunk, did string) (ok bool, pattern string)` — patterns in list order, first match wins; `X` matches `[0-9]`; a regex is tried as given and with the leading `+` toggled, as the DID match does.
4. **Migration:** `trunks.internal_dialing jsonb not null default '{"mode":"off"}'` plus its `CHECK` on `mode`, with the down migration dropping the column.
5. **API:** trunk bodies carry `internalDialing` `{"mode": "...", "patterns": [...], "confirm": true?}`; mode `all` without `confirm` is 400; the route tester's `from` grammar is `"<extension>"` or `"trunk:<id>"`.
6. **Trace texts** (`internal/routing`): the policy verdict, the loop-skip, the caller-ID choice and the `internal` lookup are fixed strings in the package, so the tester, hello-sip and the console render the same words; the console matches on the step text, never its index.
