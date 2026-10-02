# trunks-routing

Status: complete

Source: `hello-pbx-spec.md` §28 Phase 2 (Trunks and Routing), plus §11 (dial plan and explainable routing), §12 (SIP trunks), §13 (inbound routing) and §22 (CDRs). Decided 2026-10-02 (`.procoder/ask/answers.md`):

- one SIP node at a time registers each registration-based trunk, under a Valkey lease, and another node takes over when the lease expires
- numbers are rewritten with a regex match and a replacement template using capture groups, plus simple strip and prefix fields
- trunks are tested against a simulated carrier in the lab, with one manual check against a real trunk
- media flows directly between phone and carrier; anchoring waits for Phase 5

## Problem

After Phase 1, Hello only connects its own extensions. A PBX earns its keep by reaching the outside world: calls to and from the public network through SIP trunks, chosen by rules an administrator can read, with failover when a carrier misbehaves. When a call fails, an operator needs the exact reason without a packet capture: which rule matched, how the number was rewritten, which trunk was tried, and what it answered. Phase 2 adds trunks, structured inbound and outbound routing, rewriting, health checks, failover, and a routing trace on every call.

## Users

- **Administrators:**
  - define trunks (a carrier account or an IP-authenticated peer) and routes, in the UI or the API
  - test a number against the routes before going live
  - see trunk health at a glance
- **Phone users:**
  - dial external numbers the way they normally would (`0501234567`), and the call goes out correctly
  - receive calls to their DID on their extension
- **Operators:**
  - open any call and read its routing trace and failure explanation
  - watch trunk status, latency, utilisation and response codes in metrics
- **Developers:** run every trunk scenario against simulated carriers in the lab.

## In scope

- [S-1] **Trunks:** `/api/v1/trunks` CRUD.
  - Each trunk has a name, a mode (`registration` or `ip`), one or more destinations (`host[:port]`, priority, weight), an optional username and password, an optional realm and From domain, the registration expiry, the OPTIONS interval, and the source CIDRs it may send from.
  - It also has a maximum number of concurrent calls, a default caller ID and an enabled flag.
  - Destinations that are hostnames resolve through DNS SRV, then A/AAAA.
  - Trunk passwords are encrypted with AES-256-GCM under `HELLO_SECRET_KEY`. They are returned only in the create response, never afterwards, and never logged.
- [S-2] **Trunk registration:** for each enabled `registration` trunk, exactly one SIP node holds a Valkey lease (`hello:trunkreg:{id}`) and REGISTERs to the carrier, answering its digest challenge and re-registering before expiry.
  - When the holder stops renewing, another node takes the lease and registers within one lease period.
  - The registration state (registered, failed, the last response code, the expiry) is shared in Valkey.
- [S-3] **OPTIONS health:** the lease holder for each trunk sends OPTIONS to every destination at the trunk's interval.
  - A destination is `up` after a final response or `down` after a timeout; its latency and last code are shared in Valkey.
  - A `down` destination is skipped by routing until it is `up` again.
- [S-4] **Outbound routes:** an ordered list of routes. Each has:
  - a match: a prefix or a regex on the dialled number, optional source extensions (empty means all), and an optional schedule of weekly time windows in a named time zone
  - a number transform: strip N leading digits, add a prefix, or a regex with a replacement template (`+971${1}`), applied in that order
  - a caller-ID transform in the same shape
  - an ordered list of trunks
  - the SIP response codes that allow failover to the next trunk (default 408, 480, 500, 502, 503 and 504, plus no answer from the destination)
  - an emergency flag
- [S-5] **Inbound routes:** an ordered list matching on:
  - the called number (DID: exact, prefix or regex)
  - the source trunk
  - the request's SIP domain
  - one header-name and regex pair
  - a schedule

  The destination is an extension, an external number (routed through outbound routing), or a SIP URI. The SIP URI covers AI voice agents and other SIP services.

- [S-6] **Trunk source validation:** an INVITE that does not come from a registered phone is accepted only if its source IP is in a trunk's source CIDRs or among its resolved destinations. Otherwise it gets 403 and counts towards the failed-auth throttle. The trunk it matched becomes the call's source trunk.
- [S-7] **The routing engine:**
  - **Order:** for a call from an extension, the dialled number is looked up as an internal extension first, then matched against outbound routes in order. A call from a trunk uses inbound routes.
  - **Determinism:** the outcome depends only on the configuration snapshot, the call and the clock, never on hidden state.
  - **Trace:** every step is appended to a routing trace, in the shape of spec §11.
- [S-8] **Trunk selection and failover:** within the chosen route, trunks are tried in order, skipping any that are disabled, unhealthy or full. Within a trunk, destinations go by priority, then weight. When a failover code comes back, the next destination or trunk is tried. The final response to the caller is the last trunk's answer, or 503 when no trunk was usable. Each attempt is recorded in the trace.
- [S-9] **Concurrency limits:** each trunk's active calls are counted cluster-wide in Valkey; an attempt beyond `max_calls` skips that trunk as full.
- [S-10] **Caller ID:**
  - An extension may have an external number.
  - An outbound call presents the route's caller-ID transform applied to that external number, falling back to the trunk's default caller ID, then to the extension number.
  - An inbound call presents the carrier's caller ID as received. An optional per-route transform normalises it.
- [S-11] **Routing traces are stored and explained:**
  - Every call's trace and final outcome is saved in its CDR (a `trace` JSON column).
  - CDRs gain the original destination, the rewritten destination, the selected route and trunk, and the direction (`internal`, `inbound` or `outbound`).
  - `GET /api/v1/cdrs/{id}` returns the trace, with a one-line failure explanation for failed calls.
- [S-12] **Route tester:** `POST /api/v1/routing/test` takes `{"from": "<extension or trunk>", "number": "...", "at": "<RFC 3339, optional>"}`. It returns the trace and the decision against the current configuration without placing a call, and with no side effects.
- [S-13] **Validation:**
  - **Before saving:**
    - every regex compiles
    - every template references only groups its regex defines
    - every outbound route lists at least one existing trunk
    - schedules parse
    - time zones exist
  - **Rejection:** an invalid route gets 400 with the failing field.
  - **Bookkeeping:** every route and trunk change is audited and bumps the configuration revision, as in Phase 1.
- [S-14] **Metrics:**
  - `hello_trunk_status{trunk,destination}`
  - `hello_trunk_registered{trunk}`
  - `hello_trunk_options_latency_seconds{trunk,destination}`
  - `hello_trunk_calls_total{trunk,result}`
  - `hello_trunk_active_calls{trunk}`
  - `hello_route_decision_seconds` (histogram)
- [S-15] **UI:**
  - **Trunks:** a page with live status, latency, registration state and utilisation. The password is entered once and never shown again.
  - **Routes:** inbound and outbound route lists that can be reordered, with forms that validate on the server.
  - **Route tester.**
  - **Call History:** a detail view showing the routing trace and the failure explanation.
- [S-16] **Simulated carrier and lab:** `test/fakecarrier`, a Go binary and image, provides:
  - a registrar with digest auth
  - OPTIONS answers
  - scripted outcomes keyed on the dialled number's last digits: answer, ring forever, 486, 503, or no response
  - an HTTP control endpoint that places an inbound call to Hello

  The lab runs two of them, `carrier-primary` and `carrier-backup`. A new trunk guide under docs/ explains how to add a real trunk and how to run the manual check.

## Out of scope

- SIP over TCP and TLS, for trunks and phones alike — a later phase.
- Media anchoring, an RTP relay, transcoding and SRTP — Phase 5. With direct media, NAT'd phones may get one-way audio with some carriers. This limitation is documented.
- Ring groups, hunt groups, voicemail and announcement destinations — Phase 4.
- Inbound digest authentication of carriers. Carriers are identified by source IP.
- Least-cost routing, rating, billing, number portability lookups, STIR/SHAKEN.
- A visual or scripted dial-plan language. Routes stay structured (spec §11).
- Holidays and per-date exceptions in schedules: weekly windows only.

## Constraints

- No PostgreSQL call on the SIP path. Routes and trunks join the revisioned snapshot from Phase 1.
- A routing decision takes under 1ms p99 for 100 routes, measured by a benchmark.
- Regexes are Go RE2 (linear time), and each one is limited to 500 characters, so a route cannot be a denial-of-service vector.
- Trunk passwords and `HELLO_SECRET_KEY` never appear in logs, API responses after creation, metrics, CDRs or traces. A trace shows "credentials: configured", never the value.
- Behaviour stays deterministic. Of two equally matching inbound routes, the earlier one wins. An outbound route's trunk order is fixed.
- UDP only, as in Phase 1.

## Interfaces

- **Env:** `HELLO_SECRET_KEY` is required on hello-control and hello-sip: 32 bytes, base64. It encrypts trunk passwords and is identical on every node.
- **HTTP:**
  - **Trunks:** `GET|POST /api/v1/trunks`, `GET|PATCH|DELETE /api/v1/trunks/{id}`, `GET /api/v1/trunks/status` (live: registration, per-destination health, active calls).
  - **Outbound routes:** `GET|POST /api/v1/routes/outbound`, `GET|PATCH|DELETE /api/v1/routes/outbound/{id}`, `PUT /api/v1/routes/outbound/order` (an array of IDs).
  - **Inbound routes:** the same set under `/api/v1/routes/inbound`.
  - **Routing:** `POST /api/v1/routing/test`, `GET /api/v1/cdrs/{id}` (with the trace).
  - **Extensions:** `PATCH /api/v1/extensions/{id}` gains `externalNumber`.
  - **Documentation:** every route is in the OpenAPI document.
- **SIP:**
  - REGISTER and OPTIONS out to carriers.
  - INVITE, ACK, CANCEL, BYE, re-INVITE and UPDATE to and from carriers, with Hello as a B2BUA on the trunk leg.
  - A carrier's 401 or 407 challenge to an outbound INVITE is answered with the trunk's credentials.
- **UI routes:** `/trunks`, `/routes`, `/routes/test`, `/history/{id}`.

## Data

- **PostgreSQL** (migration 00003, owned by hello-control):
  - `trunks`: id, name unique, mode, username, password_enc, realm, from_domain, register_expires, options_interval, source_cidrs cidr[], max_calls, default_caller_id, enabled, timestamps.
  - `trunk_destinations`: trunk_id, host, port, priority, weight.
  - `outbound_routes`: id, position, name, match_kind, match, source_extensions text[], schedule jsonb, number_transform jsonb, callerid_transform jsonb, failover_codes int[], emergency, enabled.
  - `outbound_route_trunks`: route_id, trunk_id, position.
  - `inbound_routes`: id, position, name, did_kind, did, trunk_id null, sip_domain, header_name, header_regex, schedule jsonb, callerid_transform jsonb, destination_kind, destination, enabled.
  - `extensions` gains `external_number`.
  - `cdrs` gains direction, original_destination, rewritten_destination, route_name, trunk_name and `trace jsonb`.
- **Valkey** (written by hello-sip):
  - `hello:trunkreg:{id}`: the registration lease
  - `hello:trunk:{id}:state`: registration state and per-destination health
  - `hello:trunk:{id}:calls`: active-call counter, kept by the B2BUA with a per-call TTL guard so a dead node's calls stop counting
- **hello-sip memory:** routes and trunks compiled into the snapshot (regexes compiled once per revision).

## Edge cases

- The same number matches several outbound routes: the first enabled one whose schedule is open wins. A closed schedule is a recorded trace step, not a silent skip.
- A regex matches but the template produces an empty string, or a number with characters other than `+`, digits, `*` and `#`: the route fails validation at save time. At call time, a defensive check rejects the call with a trace step.
- Every trunk on the route is down or full: 503 to the caller, and the trace lists why each was skipped.
- A carrier answers 503, then the backup answers 200: the call connects through the backup, and the trace shows both attempts.
- A carrier challenges an INVITE with 407 (proxy auth) rather than 401: this is answered with the same credentials.
- The lease holder dies while registered: another node registers within one lease period, and the carrier then sees the new contact.
- An inbound INVITE arrives from a carrier IP shared by two trunks: the first trunk (lowest ID) matching both the source and the DID wins.
- A caller hangs up while failover is in progress: the in-flight attempt is cancelled and no further trunk is tried.
- DST transitions and schedules that cross midnight.
- A DID with or without a leading `+`, and with whitespace in carrier headers: normalised before matching. The normalisation is shown in the trace.

## Failure modes

- **Valkey unavailable:**
  - Concurrency counters and trunk state can't be read. Non-emergency outbound calls get 503 with the trace step "trunk state unavailable".
  - Emergency routes ignore concurrency limits and use the trunks' last known health, so emergency calls are never blocked by Valkey.
  - Lease holders keep registering until their registrations expire.
- **PostgreSQL unavailable:** routing continues from the last snapshot (HA Level 1). CDRs, traces included, buffer as in Phase 1.
- **No trunk responds:** each attempt times out after the transaction timeout and fails over. The caller hears ringback only once a provisional response arrives.
- **`HELLO_SECRET_KEY` differs between nodes, or does not match stored passwords:**
  - hello-control rejects a trunk create when it cannot round-trip the password.
  - hello-sip marks each such trunk `misconfigured`, with a clear log line and status, and does not register it.
- **A carrier keeps rejecting registration:** back off exponentially (to a 10-minute cap), with the state and last code visible in the UI.

## Acceptance criteria

- [ ] [S-1] `TestTrunkCRUDSecretHidden` in `internal/api` passes. It fails if a trunk password appears in any response after create, is stored unencrypted, or cannot be decrypted by a holder of `HELLO_SECRET_KEY`.
- [ ] [S-2] `TestTrunkRegistrationLeaseFailover` in `test/integration` (`HELLO_DOCKER=1`) passes. It fails if the simulated carrier does not see exactly one registered contact per trunk, or if, after the registering node is killed, no other node registers within one lease period.
- [ ] [S-3] `TestTrunkOptionsHealth` in `test/integration` passes. It fails if stopping a carrier does not mark its destination down within two OPTIONS intervals, if routing still selects the down destination, or if restarting the carrier does not mark it up again.
- [ ] [S-4] [S-7] `TestRouteMatchAndRewrite` in `internal/routing` passes. It fails if prefix, regex, source-extension or schedule matching, or the strip/prefix/template transforms, give a result other than the table of cases, or if the trace omits any step.
- [ ] [S-4] `TestRoutingDecisionLatency` in `internal/routing` (`HELLO_BENCH=1`) reports p99 for 100 routes. It fails if p99 exceeds 1ms.
- [ ] [S-5] [S-6] `TestInboundRouting` in `test/integration` passes. It fails if an inbound call from a carrier to a DID does not ring the mapped extension, if a call from an IP that isn't a trunk is not rejected with 403, or if a header or schedule condition is ignored.
- [ ] [S-8] `TestOutboundFailover` in `test/integration` passes. It fails if a call answered 503 by carrier-primary does not complete through carrier-backup, if a 486 is wrongly failed over, or if the trace lacks either attempt.
- [ ] [S-9] `TestTrunkConcurrencyLimit` in `test/integration` passes. It fails if a trunk with `max_calls` 1 carries a second simultaneous call instead of failing over, or if a counter stays held after its call ends.
- [ ] [S-10] `TestCallerIDPolicy` in `test/integration` passes. It fails if the carrier does not receive the expected caller ID for an extension with an external number, for one without, and with a route transform applied.
- [ ] [S-11] `TestCDRTrace` in `test/integration` passes. It fails if an outbound, an inbound and a failed call do not each produce a CDR with direction, original and rewritten destination, route, trunk and a trace whose steps match the call, or if the failed call lacks a failure explanation.
- [ ] [S-12] `TestRoutingTestEndpoint` in `internal/api` passes. It fails if the route tester's trace differs from the one a real call with the same input produces, or if it writes anything.
- [ ] [S-13] `TestRouteValidation` in `internal/api` passes. It fails if an uncompilable regex, a template referencing a missing group, a route without trunks, a bad time zone or an oversized regex is accepted.
- [ ] [S-14] `TestTrunkMetrics` in `internal/sip` passes. It fails if registration, OPTIONS and a completed trunk call do not move the trunk metrics.
- [ ] [S-15] `procoder test` and `procoder lint` pass over `web/`. `Trunks.test.tsx` fails if the password is shown after saving. `Routes.test.tsx` fails if reordering does not send the new order, or a server validation error is not shown on its field. `CallDetail.test.tsx` fails if the trace steps are not rendered in order.
- [ ] [S-16] `TestLabSmoke` (`HELLO_DOCKER=1`) also places one outbound and one inbound call through `carrier-primary`. It fails if either does not complete. a new trunk guide under docs/ documents the real-trunk check.
- [ ] [S-1] [S-11] `TestNoSecretsInLogs` also checks for trunk passwords and `HELLO_SECRET_KEY`. It fails if either appears in any log, trace or CDR.

## Open questions

<!-- All resolved 2026-10-02; answers in .procoder/ask/answers.md and folded into Source, In scope and Constraints. -->
