# trunk-internal-numbers

Status: approved

Source: the pivot of 2026-10-09 (`.procoder/ask/decisions.md`): the call/voice agents leave Hello and become a separate product that connects to Hello as a SIP trunk, so SIP trunks must be able to reach internal numbers, not only the outside world. Builds on `trunks-routing` (complete). The voice-agent routing of phase 3 (the `voice_agent` destination kind, the registry, the runtime) is being removed; this spec lands on the removal. Open questions answered 2026-10-09 (`.procoder/ask/answers.md`): IP-pinned trunk auth, per-call caller identity with the S-4 ladder as fallback, the extension number as the dialing contract, 404 for out-of-policy dials.

## Problem

An inbound trunk call can already reach exactly one thing: the inbound route's fixed destination — an extension, an external number or a SIP URI. Three gaps follow.

1. **Dial-by-extension needs a route per extension.** To let a trunk peer (the consuming product, a hosted PBX, a gateways' range of DIDs) reach any extension, an administrator must write one inbound route per extension, or one regex route per pattern — and the regex route still names one fixed destination. The dialed number is already in the Request-URI user (`internal/sip/inbound.go`), but from a trunk it is never looked up in the extension namespace; only a call from a phone gets the internal lookup (`fromExtension` in `internal/routing/decide.go`).
2. **No per-trunk control over what a trunk may reach internally.** Any trunk whose INVITE passes source validation can use every inbound route. Worse, the `external` destination routes through outbound routing as a call from the source trunk, so a trunk caller can be sent back out — through the same trunk. There is no loop guard and no way to say "this trunk may call extensions 1XX and nothing else".
3. **Caller ID for trunk-originated internal calls.** An internal extension rings with the received caller ID as-is. When a trunk peer sends none (the consuming product legitimately identifies itself by trunk, not number), the phone shows a blank caller.

## Users

- **Administrators:**
  - on each trunk, decide whether it may dial internal numbers, and which: a mode (`off` by default, patterns, or all), with patterns like `1XX` or `+9714555[0-9]{3}`
  - read in the routing trace exactly why a trunk call reached, or did not reach, an internal number
- **Trunk peers (the consuming product, carriers):**
  - dial `sip:1012@hello` with no DID provisioning per extension and ring extension 1012, subject to the trunk's policy
- **Phone users:**
  - see a sensible caller ID when a trunk peer calls them, even when the peer sends no number

## In scope

- [S-1] **The `internal` inbound destination.** Inbound routes gain a destination kind `internal` beside `extension`, `external` and `sip_uri`. A call routed to it takes the DID the route matched (normalised: whitespace stripped, leading `+` handling as the DID match does) and looks it up in the extension namespace — the same map a phone's dialled number is looked up in. Found: the call rings that extension (`KindInbound`, `Extension` set). Not found: 404, trace step "not an internal number". One route now serves every extension. The `extension` destination keeps its meaning (a fixed target, ignoring the dialed number).

- [S-2] **Per-trunk internal dialing policy.** Each trunk gains `internal_dialing`:
  - `mode`: `off` (default), `patterns`, or `all`
  - `patterns`: a list of patterns checked against the DID after normalisation: an exact number, or a pattern with `X` as any digit (`1XX`, `05X`), or a Go regex anchored as given (`+9714555[0-9]{3}`). Empty with mode `patterns` is a validation error.

  With mode `off`, an `internal` destination is unreachable for that trunk: routes with such a destination are skipped for it (a traced skip, not a silent one), and a call that no route then takes gets the last route's rejection as today. The policy is checked once per call, after DID normalisation and before the route walk, and the verdict is a trace step ("trunk X may dial 1012: pattern 1XX" / "…: internal dialing off").

  Feature codes are not in the extension namespace and stay unreachable from trunks regardless of mode; the same for `external` and `sip_uri` destinations — this policy gates internal dialing only.

- [S-3] **Loop and toll-fraud guards.**
  - A trunk-sourced call routed onward through outbound routing (an `external` destination) may not leave through its own source trunk: that trunk is excluded from the route's candidates, with a trace step "trunk X skipped: call came from it". No candidate remains: 403 "loop prevented", not 503.
  - One trunk-to-outbound hop per call: a call that has already been routed from a trunk through outbound routing cannot enter outbound routing again. Today that cannot happen (an inbound route's `external` destination is the only re-entry and it fires once); the engine makes it a stated invariant, rejected with 403 if a future path violates it.
  - Mode `all` is accepted in the API only when the request carries `"confirm": true`, so `all` cannot be set by a stray default in a client.

- [S-4] **Caller ID for trunk-to-internal calls.** The peer identifies itself per call: the name/number it sends on the INVITE is what the phone shows. When an inbound call rings an internal extension (`extension` or `internal` destination), the presented caller ID is the received caller ID if any; else the source trunk's `default_caller_id` if set; else the source trunk's name in angle brackets (e.g. `<ex-agent>`), so the phone always shows something attributable. The route's caller-ID transform, when one is set, applies to whatever value this produces, as today. The choice is a trace step. Calls to `external` and `sip_uri` destinations keep the current behaviour (received value, transform-normalised).

- [S-5] **CDRs.** Direction stays `inbound` for trunk-to-internal calls. The CDR's `rewritten_destination` records the resolved extension number, `route_name` the inbound route; the trace carries the policy verdict and the caller-ID decision. No new columns and no migration beyond the trunks table.

- [S-6] **API and console.**
  - `GET|POST /api/v1/trunks`, `PATCH /api/v1/trunks/{id}` carry `internalDialing` (`{"mode": "...", "patterns": [...]}`); validation: mode is one of the three, patterns compile as exact, `X`-pattern or anchored regex, each ≤500 characters, at most 100 per trunk; `patterns` non-empty iff mode is `patterns`. Invalid: 400 with the failing field.
  - The route tester (`POST /api/v1/routing/test`) accepts `"from": "trunk:<id>"` and returns the trace including the policy verdict, so an administrator proves a trunk's reachability before going live.
  - The console's trunk form gains the policy fields (mode radio, pattern list with live validation); the route detail renders the new trace steps in order.

- [S-7] **Edge path.** Kamailio forwards trunk INVITEs to hello-sip with the Request-URI unchanged today; the spec requires it stays so (no rewriting of the user part on the trunk path), asserted by a lab test rather than new cfg logic.

- [S-8] **Tests.**
  - `internal/routing` unit tests: the table-driven policy matrix (mode × pattern × DID), the `internal` lookup hit and miss, the loop guard, the caller-ID ladder, and trace text for each verdict.
  - `test/integration`: a trunk INVITE to `sip:1012@…` rings extension 1012 through one `internal` route; a disallowed DID on the same trunk gets 404; mode `off` skips the route; an `external` destination whose route names only the source trunk gets 403 "loop prevented"; the CDR and trace fields of S-5.
  - Lab (`TestLabSmoke`, `HELLO_DOCKER=1`): the fakecarrier places an inbound call addressed to an extension number; it rings, answers, and audio flows both ways; the trace in Call History shows the policy verdict. The kamailio drift test asserts the trunk path still forwards the Request-URI user unchanged (S-7).

## Out of scope

- Inbound digest authentication of trunk peers (Hello accepting REGISTERs from them). A trunk peer authenticates by source IP against the trunk's source CIDRs, as carriers do since phase 2. The consuming product pins its signalling IPs. Digest is a possible later milestone, not this one.
- Reaching ring groups, queues, voicemail or announcements from trunks — internal numbers means extensions only, until those features exist.
- Trunk-to-trunk dialing as a feature. A trunk reaching another trunk's numbers only via an explicit `external` destination, with the S-3 guards; no convenience surface.
- Feature codes, dial-plan rewrites of the Request-URI at the edge, and per-trunk caller-ID transforms (the per-route transform covers it).
- Number normalization beyond the existing whitespace and `+` handling: no per-trunk normalization rules; a peer must send extensions the way Hello numbers them.

## Constraints

- No PostgreSQL call on the SIP path: the policy joins the trunk in the revisioned snapshot, compiled once per revision like every regex today.
- Default-deny: `internal_dialing.mode` defaults to `off` on new and on existing trunks (migration default), and a trunk created before this spec never gains internal reach by an upgrade.
- Deterministic: patterns are checked in list order, the first match wins, and the trace names the pattern. Of two matching routes the earlier wins, as today.
- The engine stays pure: policy and pattern matching are functions of the Table and the Call; the route tester and hello-sip cannot disagree.
- Regexes are Go RE2, ≤500 characters, ≤100 per trunk, bounded as phase 2's.
- A reject is a reject: 404 for "not an internal number" and "no route", 403 for loop and for a disabled or unknown source trunk, matching phase 2's code use.

## Interfaces

- **HTTP:**
  - `GET|POST /api/v1/trunks`, `GET|PATCH|DELETE /api/v1/trunks/{id}`: `internalDialing` as in S-6.
  - `POST /api/v1/routing/test`: `from` values `"<extension>"` and `"trunk:<id>"`.
  - OpenAPI: new and changed schemas for `Trunk`, `RouteTestRequest` and the trace steps.
- **SIP:** none new. Inbound INVITE handling gains the destination kind and the guards; the Request-URI user is already the DID.
- **Data:** `trunks` gains `internal_dialing jsonb` (default `{"mode":"off"}`), migration `000NN_trunk_internal_dialing.sql` with its down migration.
- **UI:** the trunk form and the route/call-detail trace views.

## Data

- **PostgreSQL:** `trunks.internal_dialing jsonb not null default '{"mode":"off"}'`; `CHECK (internal_dialing->>'mode' in ('off','patterns','all'))`.
- **Snapshot:** the trunk's compiled policy (mode plus compiled patterns: exact, digit-pattern or regex) rides the existing trunk compile.
- **Valkey, hello-sip memory:** unchanged.

## Edge cases

- A trunk with mode `patterns` and pattern `1XX` dials `101X`: `X` in the dialed number is not a digit; the pattern does not match; the call falls through as today (404 unless another route takes it), and the trace says why.
- Two patterns overlap (`1XX` then `10[12]`): the first in list order wins and is named in the trace.
- An `internal` route matches a DID that is an extension, while the source trunk has mode `off`: the route is skipped with "internal dialing off on trunk X"; if that was the only route, 404.
- The dialed number matches an extension, but that extension is disabled: the lookup misses (the map holds enabled extensions, as the phone path today) — 404.
- An `external` destination whose route lists several trunks including the source: the source is skipped, the others are tried; only when no trunk remains is it 403, not 503, and the trace distinguishes the reasons.
- A trunk peer sends `sip:1012@hello.example` with a SIP domain an inbound route pins: unchanged behaviour — the domain condition is evaluated before the destination.
- The consuming product re-INVITEs or transfers an internal leg to another extension: in-dialog requests follow the existing transfer path, which is not re-policy-checked; the initial INVITE decided the reach.

## Failure modes

- **Snapshot unavailable:** a node keeps the last compiled table, as in HA level 1; the policy travels with the trunk, so a stale table means a stale policy, visible in the trace's config revision.
- **A pattern fails to compile despite save-time validation** (defensive): the trunk is marked `misconfigured` in the snapshot, like a trunk whose password cannot decrypt, its routes skip, and the status is visible in the UI.
- **The consuming product sends a storm of probes at extensions it may not dial:** the failed-call throttle and pike (per source IP) bound it as today; nothing new is needed because source validation precedes routing.

## Acceptance criteria

- [ ] [S-1] `TestInternalDestination` in `internal/routing` passes. It fails if an `internal` destination does not ring the extension the DID names, if it ignores the normalisation the DID match applied, if a miss does not reject 404 with "not an internal number" in the trace, or if the `extension` destination stops meaning "fixed target".
- [ ] [S-2] `TestTrunkInternalDialingPolicy` in `internal/routing` passes. It fails if any cell of the mode × pattern × DID matrix gives another verdict than the table, if the trace does not name the winning pattern, if mode `off` does not skip the route with a traced reason, or if a feature code becomes reachable.
- [ ] [S-2] [S-6] `TestTrunkDialingValidation` in `internal/api` passes. It fails if an unknown mode, an uncompilable or oversized pattern, more than 100 patterns, or `patterns` set with mode `off` is accepted, or if mode `all` is saved without `"confirm": true`.
- [ ] [S-3] `TestLoopPrevention` in `internal/routing` passes. It fails if a trunk-sourced external route still selects the source trunk without a trace step, if a sole-trunk case answers 503 instead of 403 "loop prevented", or if outbound routing can be re-entered twice on one call.
- [ ] [S-4] `TestInternalCallerIDLadder` in `test/integration` passes. It fails if a phone sees, for a trunk call with a number, without a number, and without either default, something other than the received caller ID, the trunk's default caller ID, and the bracketed trunk name respectively, or if the route transform is not applied to the produced value.
- [ ] [S-5] `TestTrunkInternalCDR` in `test/integration` passes. It fails if a trunk-to-internal call's CDR lacks direction `inbound`, the resolved extension as `rewritten_destination`, the route name, or the policy and caller-ID steps in the trace.
- [ ] [S-6] `TestRoutingTestFromTrunk` in `internal/api` passes. It fails if `"from": "trunk:<id>"` gives a trace that differs from a real call with the same input, or if it writes anything.
- [ ] [S-8] `TestTrunkInternalLab` in `test/integration` (`HELLO_DOCKER=1`) passes. It fails if a fakecarrier INVITE to `sip:1012@…` on a trunk with pattern `1XX` does not ring extension 1012 and carry audio both ways, if a DID outside the pattern is not rejected, or if the trace lacks the verdict.
- [ ] [S-7] `TestKamailioTrunkURI` in `test/deploy` passes. It fails if the kamailio configuration rewrites the Request-URI user on the trunk path (drift test against the shipped cfg).

## Open questions

<!-- All resolved 2026-10-09, answered by Pascal, recorded in .procoder/ask/answers.md: (1) pinned IPs, digest REGISTER out of scope — a possible later milestone; (2) the peer's per-call name/number is the caller ID, the ladder of S-4 stays as fallback; (3) the extension number is the contract, no DID map; (4) 404 for out-of-policy dials, as written. -->
