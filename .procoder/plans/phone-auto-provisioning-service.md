# phone-auto-provisioning-service — implementation plan

Status: draft
Spec: .procoder/specs/phone-auto-provisioning-service.md

## Goal

A phone whose MAC an administrator has assigned to an extension configures itself: it finds Hello through DHCP option 66, a vendor redirect service or a typed URL, fetches its vendor's files from hello-control's provisioning listener over HTTPS with its per-device token, and registers through Kamailio — with every fetch audited, rate-limited, and no secret ever logged.

## Architecture

A new package `internal/prov` holds everything phones touch: the vendor file sets (which request path is which file kind for which vendor), template resolution and rendering, token handling, the provisioning HTTP handler, the rate limiter, the buffered fetch-audit writer and the metrics. hello-control mounts that handler on a second listener (`HELLO_PROV_ADDR`, `:8083`), so the phone-facing surface is isolated from the management API, has its own k8s Service and Ingress host, and can never be reached with a session cookie.

- **hello-control** owns the schema (`migrations/00006_provisioning.sql`), the phone/template/firmware/redirect management APIs, device-secret sealing on binding, firmware storage in MinIO, and the redirect reconcile worker.
- **`internal/prov/redirect`** holds one client per vendor behind a single interface; it is called only by the reconcile worker.
- **hello-sip** is unchanged: it still verifies HA1 values, which binding keeps in step with the sealed secret.
- **kw:** a `hello-prov` Service and an Ingress for `prov.hello.kw.watteel.lab` (cert-manager `cluster-ca`, plain HTTP kept for bootstrap and the CA download, access log off).

Why hello-control and not a separate service: provisioning needs the store, `internal/secret`, the audit table, Valkey and MinIO, all of which hello-control already has and runs with two replicas on kw. A separate binary would duplicate that wiring for no isolation the second listener and its own Ingress do not already give.

## Constraints

- No PostgreSQL call on the SIP path; hello-sip is not touched.
- A SIP secret, a token or a redirect credential never appears in logs, API responses beyond the show-once create/rotate responses, metrics, the fetch audit, previews or traces. Paths are redacted to `/p/****/…` everywhere.
- Templates are `text/template` over a fixed struct, 100 ms deadline, 256 KiB cap; rendering is deterministic (stable ETag).
- ETag re-check under 5 ms p99, full render under 20 ms p99 (`HELLO_BENCH=1` benchmark).
- Rate limiting is cluster-wide in Valkey with an in-memory fallback; it never fails open.
- No new dependencies.
- Each task:
  - leaves `gofmt`, `go vet ./...`, `golangci-lint run ./...` and `go test -race ./...` clean, with the test databases set
  - leaves `procoder check` with 0 blocking findings
  - gives every security-relevant or non-trivial behaviour a test that fails without it, mutation-checked (snapshot immediately before, restore immediately after, `cmp`)
- The REVIEW.md rubric applies. CI runs on the Arc runners; deployment is Kuvryn Sync; no workloads on the user's Mac.
- The spec's questions were answered on 2026-10-06 (`.procoder/ask/answers.md`): `cluster-ca`, trust on first use for DHCP phones, Snom/Yealink/GDMS redirect clients live-tested when their sops secrets exist, and a random per-phone admin password. Tasks 1–5 build the answer-independent parts first; the bootstrap and certificate details in Task 2 and Task 6 wait for the answers.

### Shared contracts (fixed; a stream that needs a change asks the lead and never edits another stream's files)

1. **Schema:** `migrations/00006_provisioning.sql`, as committed (spec Data section), plus what the spec left to Task 1: `phones.ua_mismatch` (the API's `uaMismatch`), `phones.device_id` nullable with `CHECK (device_id IS NOT NULL OR NOT enabled)` (an unbound phone cannot be enabled), override `template_id` `ON DELETE RESTRICT` (an override always names a row; to pin a built-in, copy it), `prov_firmware` unique on `(vendor, filename)` (a phone fetches firmware by name), `prov_fetches.phone_id` without a foreign key (the buffered writer may insert after a phone is deleted), a `CHECK` refusing a `path_redacted` that still carries a token, `prov_fetches.ua_mismatch` (spec S-6 records the mismatch on the fetch), and the redirect worker's queue `prov_redirect_jobs` (one pending `register`/`unregister` per vendor and MAC, a new `seq` on every replace, no URL or token stored).
2. **Types:** `internal/prov/types.go`, as committed:
   - `Vendor` (`yealink`, `poly`, `grandstream`, `snom`, `fanvil`, `generic`)
   - `FileKind` (`common`, `device`, `master`, `firmware`, `ca`, `boot`, `upload`, `other`)
   - `Result` (the spec S-14 list, as string constants, plus `not_found` for a well-formed request with nothing to serve and `unavailable` for the 503 of a store or MinIO outage); `Vendors`, `FileKinds` and `Results` list them, and `TestContractMatchesMigration` pins them to the migration's `CHECK` lists
   - `Phone`, `Line`, `Server`, `BLFKey`, `ProvInfo`, `FirmwareInfo`, `TimeInfo` and `RenderData` (the template variables of spec S-8, field names as documented there; `Phone` also carries `MACUpper` and `Vendor`, which `Resolve` needs, and `Line.VoicemailCode` is the message key's feature code of spec S-5)
   - `Template{ID, Vendor, ModelGlob, Priority, Name, Files []TemplateFile, BuiltinRef, Version}` (a built-in has ID 0; `Builtin()`), `TemplateFile{Pattern, ContentType, Body}`, `FieldError{Path, Line, Message}` (the Phase 1 `fields` shape plus the body line), `Firmware` (one `prov_firmware` row)
   - `FetchRecord` (one `prov_fetches` row, `UAMismatch` included)
   - the functions in its trailing comment, implemented by Task 2 with exactly those signatures: `NormalizeMAC(string) (string, error)`, `MatchFile(v Vendor, model, mac, name string) (FileKind, bool)`, `Resolve(phone Phone, override *Template, all []Template) (Template, bool)`, `Render(ctx, Template, file string, RenderData) ([]byte, error)`, `Validate(Template) []FieldError`, `NewToken() (plain string, hash []byte)`, `RedactPath(string) string`, and `Builtins() []Template` (added: Task 3 resolves against and lists the built-ins).
3. **Secret sealing:** `internal/secret` AADs `device:<id>`, `phone-token:<id>`, `phone-admin:<id>`, `redirect:<vendor>`, spelled by `prov.DeviceSecretAAD`, `PhoneTokenAAD`, `PhoneAdminAAD` and `RedirectAAD`. hello-control seals; the store opens them for `RenderInputs`, so the handler only ever sees opened render data.
4. **Store interface for the handler** (`internal/prov/store.go`, as committed): `PhoneByToken(ctx, hash []byte) (PhoneRecord, error)` (current or in-grace previous, reporting which), `MarkFetched(ctx, phoneID, hash []byte, FetchState) error` (also disarms the boot hand-off), `PromoteToken(ctx, phoneID, hash []byte) error` (called on the first fetch with a new token); both are compare-and-set on `hash`, the token hash the request matched (lead decision 2026-10-07, see the note after this list), `FlagTokenExposed(ctx, phoneID) error`, `PhoneByMAC(ctx, mac) (PhoneRecord, error)` (read-only, so the boot path checks the vendor before claiming), `ClaimBoot(ctx, mac) (PhoneRecord, *ProvInfo, error)` (brought forward from Task 2 so no stream edits the contract; it returns the hand-off URL opened in the disarming transaction, which rolls back if the token does not open), `RenderInputs(ctx, phoneID) (RenderData, Template, error)`, `FirmwareByName(ctx, Vendor, filename) (Firmware, error)`, `InsertFetches(ctx, []FetchRecord) error`; the sentinels `ErrNotFound`, `ErrNoTemplate` and `ErrSealed` (anything else is an outage, `503`); and `Opener{OpenFirmware(ctx, objectKey) (io.ReadSeekCloser, error)}`, the `Opener` of `NewHandler`. Task 3 implements both in `internal/store` and over MinIO; Task 2 tests against in-memory fakes.
5. **Redirect client interface** (`internal/prov/redirect/redirect.go`, as committed): `Client{Vendor() prov.Vendor; Capabilities() Caps; Check(ctx) error; Register(ctx, mac, serial, url string) error; Unregister(ctx, mac string) error}`, `Caps{RegistersURL, NeedsSerial, Supported bool}` and `ErrUnsupported`; `Status{State, Reason, At}` with the `State` constants is `phones.redirect_status` and the API's `redirectStatus`. Added so Tasks 3 and 4 meet in the middle: `Client.Lookup(ctx, mac) (url string, found bool, err error)` (drift check, live-test restore), `Credentials` (a map keyed by the S-11 secret key names that prints `[redacted]` through fmt and slog, JSON handler included, as does a logged `Account`; only `json.Marshal` for sealing shows the values), the worker's `Store` (`DueJobs`, `Target(ctx, vendor, mac)`, `Account`, `FinishJob(ctx, Job, *Status)`, `RetryJob(ctx, Job, next, *Status)` (a nil status leaves the phone's alone), `LastDriftCheck`/`SetLastDriftCheck` (kept on the `schema_info` settings row, `prov_drift_checked_at`, so a restart runs an overdue drift check at once), `Registered`, `SetStatus`; Task 3 implements it, with a test where a replacing job lands between `Target` and `FinishJob`: every replace takes a new `prov_redirect_jobs.seq`, carried as `Job.Seq`, and finish and retry match on it), and Task 4's promised `New(vendor, Credentials, settings, *http.Client) (Client, error)` (Task 3's credential check on save), `Deployment(config.ProvRedirect) map[prov.Vendor]Credentials`, `NewWorker(Store, deployment, *slog.Logger) *Worker` and `(*Worker).Run(ctx)`, which hello-control runs only while holding the redirect lease.
   - _Lead decision (2026-10-07), compare-and-set on the matched token:_ `MarkFetched` and `PromoteToken` take the hash `PhoneByToken` matched and update only while it is still the phone's current token (`MarkFetched`: or its in-grace previous one); otherwise they are a no-op returning nil. A fetch in flight across a re-arm (an immediate rotation that arms) or a rotation therefore can neither disarm the re-armed hand-off nor end the newer rotation's grace.
   - _Lead decision (2026-10-07), the CA in templates:_ `ProvInfo.CACertPEM` is the CA certificate itself (one PEM block, trailing newline trimmed; empty without `HELLO_PROV_CA_CERT`), read from the CA file on each render like `/p/ca.crt`. The Poly built-ins set `device.sec.TLS.customCaCert1` and the Grandstream ones CA slot `P8433` from it, boot bodies included.
6. **Valkey keys:** spec Data section; the limiter lives in `internal/prov/ratelimit.go`.
7. **HTTP JSON** (camelCase, Phase 1 error envelope and list shape; validation 400s carry `fields`):
   - **Phone:** `{"id","mac","serial","vendor","model","label","deviceId","extensionId","extensionNumber","templateId","blf":[number...],"enabled","tokenExposed","uaMismatch","bootArmed","bootReclaimed","redirectStatus":{"state","reason","at"},"firstFetchAt","lastFetchAt","lastFetchIp","lastFetchUa","lastFetchFile","firmwareSeen","renderError","createdAt","updatedAt"}` (`serial` added for YMCS and GDMS; `renderError` is true while the phone's latest fetch was `render_error`, the UI flag of the spec's failure modes). Create, rotate and re-arm responses add `"provisioningUrl"` once. `POST …/admin-password/reveal` returns `{"adminPassword"}` and writes an audit row; `POST …/admin-password/rotate` returns 204.
   - **Create:** `{"mac","serial"?,"vendor","model","label","extensionId","deviceId"?,"blf","enabled"}`; with no `deviceId` a device is created; with one, the response includes `"secretRotated": true`.
   - **Fetch:** `{"at","ip","userAgent","path","kind","result","status","bytes"}`.
   - **Template:** `{"id","vendor","modelGlob","priority","name","files":[{"pattern","contentType","body"}],"builtin","builtinRef","version","updatedAt"}`.
   - **Firmware:** `{"id","vendor","modelGlob","version","filename","size","sha256","uploadedAt","pinned"}`.
   - **Redirect account:** `{"vendor","enabled","hasCredentials","fromDeployment","settings",…,"lastCheckAt","lastCheckResult","supported"}`; credentials accepted on PUT, never returned; `fromDeployment` accounts (env from the `hello-prov-redirect` secret) are read-only.
   - **Settings:** `{"publicUrl","bootUrl","caUrl","caSha256","dhcp":[{"vendor","option","value"}],"sipServer"}`.
   - **CSV import:** dry run → `{"rows":[{"line","mac","errors":[...]}],"ok":bool}`; apply → the same, plus `"created"`.
8. **Configuration revision and audit:** every phone, template, firmware, pin and redirect-account change writes an `audit_events` row in the same transaction. The schema's `RESTRICT` keys (a device or extension bound to a phone, an override template, a pinned firmware) surface as foreign-key violations (`23503`), which `internal/store` maps to `409` with the referencing phone or pin named, not the existing not-found mapping. These changes do not bump `config_revision` (hello-sip does not read them), except device binding, which changes HA1 values and so bumps it as Phase 1 device changes do.

## Task 1: Shared contracts (lead)

Files: `migrations/00006_provisioning.sql`, `internal/prov/types.go`, `internal/prov/store.go`, `internal/prov/redirect/redirect.go`, `internal/config` (the `HELLO_PROV_*` settings of the spec, plus `HELLO_PROV_TLS_CERT` / `HELLO_PROV_TLS_KEY` for the compose lab, where no ingress terminates TLS), this plan.
Configuration as committed (`config.Control.Prov`): the listener is enabled when `HELLO_PROV_PUBLIC_URL` is set (an `https://` scheme-and-host URL), not when `HELLO_PROV_ADDR` is non-empty: an environment cannot tell an empty variable from an unset one, and with the spec's rule every existing deployment would stop starting until Task 6 sets the URL. `HELLO_PROV_ADDR` without a public URL is refused. Durations also take whole days (`7d`). `RegisterExpiry` is `HELLO_SIP_REGISTER_MAX_EXPIRES` (default 1h). The redirect credentials of spec S-11 are read into `Prov.Redirect`, each vendor's group all set or all empty.
Interfaces: everything listed in Shared contracts.

- [x] Write the migration and confirm it applies and rolls back with `HELLO_TEST_DATABASE_URL=… go test -run Migrate ./test/integration/` → ok (`TestMigrateProvisioningRollback`, CI go job on PR #26).
- [x] Write `types.go`, `store.go` and `redirect.go` with doc comments; `go build ./...` → ok.
- [x] Add the config fields with defaults and validation (`HELLO_PROV_PUBLIC_URL` required when the listener is enabled; durations and CIDRs parse) and run `go test ./internal/config/` → `TestLoadProv*` pass.
- [ ] Commit to `prov-contracts`, then branch `prov-core`, `prov-control`, `prov-redirect` and `prov-ui`, each in its own worktree.

## Task 2: Provisioning core (branch prov-core)

Files: `internal/prov/` (`files.go` vendor file sets and model-ID tables, `resolve.go`, `render.go`, `token.go`, `handler.go`, `ratelimit.go`, `audit.go`, `metrics.go`, `builtin/` embedded templates for the five vendors, tests and `bench_test.go`).
Interfaces: produces contract 2's functions and `NewHandler(Store, Limiter, Opener, Options) http.Handler`; consumes contract 4 through a fake.

- [x] File sets: for each first-class vendor, the request names of spec S-7 and the vendor reference table, MAC case included, mapped to a `FileKind`; the Yealink model-to-hardware-ID table; `TestVendorFileSets` with every documented path plus cross-vendor negatives.
- [x] Built-in templates: one file set per vendor rendering every spec S-5 field; `TestRenderedConfigContents` parses each vendor's output (the parsers come from `test/provclient`, written here first and owned by this task) and asserts each field; renders twice and compares bytes.
- [x] Resolution and validation: override, then priority, then glob specificity, then id; parse, variable whitelist, 100 ms deadline, 256 KiB cap; `TestTemplateResolutionAndValidation` includes a template that tries `{{.}}` method calls, `call` and range over huge input.
- [x] Tokens: `NewToken` (32 bytes, base32 lowercase, no padding), current/previous lookup, promotion on first new-token fetch, grace expiry, `immediate` revocation; `TestTokenRollingRotation`.
- [x] Handler: the routes of spec S-4; HTTPS detection (TLS or `X-Forwarded-Proto` from `HELLO_PROV_TRUSTED_PROXIES`), client IP from the trusted last hop, allowlist checks, MAC-in-name check, UA evidence (`ua_mismatch`), ETag/304, empty `404` for every denial, discarded capped uploads, `503 Retry-After` on store outage; `TestProvEndpointAuth`.
- [x] Boot path: the non-secret common bodies per vendor (CA install, re-check, boot URL) and the trust-on-first-use hand-off of spec S-10: a per-vendor bootstrap body carrying only the CA and the per-device HTTPS URL; disarm by a conditional update (`UPDATE … WHERE boot_armed` returning the row) so two racing requests cannot both win; `boot_reclaim`, `boot_denied` (outside `HELLO_PROV_BOOT_CIDRS`) and disarm on the first HTTPS fetch. `TestBootTrustOnFirstUse`, including the concurrent-claim case, through the contract's `PhoneByMAC` and `ClaimBoot`.
- [x] Rate limiter: Valkey sliding windows per IP, per denied IP and per phone, the 10-minute block, the in-memory fallback; `TestProvRateLimit` with two handler instances on one Valkey, then with Valkey stopped.
- [x] Audit writer: buffered channel, batch insert every second or 200 rows, redacted paths, drop-and-count when full or the store fails; `TestFetchAudit`.
- [x] Metrics of spec S-17; `TestProvMetrics`. Benchmark (`HELLO_BENCH=1`): ETag re-check under 5 ms p99, render under 20 ms p99.
- [x] Run the full gate; mutation-check the MAC-in-name check, the plain-HTTP refusal, the grace expiry and the redaction (all killed; also the range-depth limit and the in-memory block; the Valkey block check is killed only where `HELLO_TEST_VALKEY_ADDR` is set, i.e. CI). `HELLO_BENCH=1`: p99 ETag re-check 1.2 ms, full render 1.3 ms.

Notes from Task 2 (no contract changed):

- Additive exports for Task 3: `Tokens` (`Rotate`, `Match`, `Promote`, `InGrace`) is the reference for the rolling rotation the store implements in SQL; `DeviceURL`, `FirmwareURL`, `BootURL`, `CAURL` and `ResyncSeconds` (MAC-derived jitter up to a tenth) build `ProvInfo`; `SampleData` is the validation sample (usable for previews without a phone); `ErrNoFile`, `FileFor`, `ExpandPattern`, `GlobMatch`, `YealinkModelID`, `FanvilCommon`. `NewHandler` takes a `*Limiter` (`NewLimiter(valkey.Client, Limits, *slog.Logger)`, or `NewLazyLimiter(func() valkey.Client, …)` for a client that connects after start) and `Options` (public URL, trusted proxies, boot CIDRs, CA file, resync, `*Audit` from `NewAudit`, `*Metrics` from `NewMetrics`); hello-control runs `(*Audit).Run` and sets `Metrics.SetPhones`.
- Template functions: `add`, `mul` and `div` join spec S-8's `xml`, `upper`, `lower` and `default`, because vendor keys are numbered (line key N+2, Grandstream MPK P-values) and some take minutes; the comparison builtins, `len` and `index` are allowed; `call`, `printf`, `print`, `println`, `slice`, `html`, `js`, `urlquery`, `define`, `template` and `block` are refused, and ranges nest at most two deep over list variables only, which bounds every render's work.
- Templates see `RenderData` as maps (no methods are reachable; unknown fields fail with `missingkey=error`), and Validate also type-checks field chains statically.
- Boot path: Poly's per-MAC master on `/p/boot/` is served without a claim (its `CONFIG_FILES` names `<mac>-hello.cfg`), so the hand-off happens on the device file; Yealink's `.boot` files are answered 404 (Hello uses the .cfg flow).
- Any `PUT` under a valid token whose name embeds no other MAC is discarded (204), not only Poly's names.
- For the lead: Poly's `device.sec.TLS.customCaCert1` and Grandstream's CA slots take certificate content, not a URL, and `RenderData` carries only `Prov.CAURL`, so the Poly, Grandstream and Snom built-ins name the CA URL in a comment only (Snom's and Fanvil's CA keys are unconfirmed; Fanvil gets `CACertURL`). Resolved by the lead: `Prov.CACertPEM` (contract 2's note) now installs it on Poly and Grandstream. Several vendor keys marked unconfirmed in the spec's table (Grandstream MPK P-values, Fanvil DSS and admin keys, time-zone keys) are best effort until the lab and real phones confirm them.
- `hello_prov_redirect_ops_total` is `(*Metrics).RedirectOp`; the fixed `redirect.NewWorker` signature has no metrics, so the lead wired it: `(*Worker).Metrics`, set by hello-control to the registered `*prov.Metrics` (the worker's own counter is gone).

## Task 3: Control plane (branch prov-control)

Files: `internal/store/` (phones, tokens, templates and versions, firmware and pins, redirect accounts, fetches, device `secret_enc`, the contract 4 implementation), `internal/api/` (handlers, CSV import, preview, OpenAPI, tests), `internal/media/objects.go` only for a `hello-firmware` bucket helper if the existing client does not cover it, `cmd/hello-control/main.go` (second listener, audit writer and pruner, redirect worker wiring).
Interfaces: produces the HTTP JSON of contract 7 and the contract 4 store; consumes `internal/prov` (stubbed against `types.go` until Task 2 lands) and `redirect.Client` (fake until Task 4 lands).

- [ ] Store: every mutation in one transaction with its audit row. Phone create with a new device (username `<ext>-<last 6 MAC hex>`), or binding an existing device: new secret, sealed with AAD `device:<id>`, HA1 updated, `config_revision` bumped with NOTIFY. Unbinding clears `secret_enc`. Token create and rotation store hash plus sealed token; `immediate` clears the previous token.
- [ ] Device rotate-secret: when the device is bound to a phone, re-seal. `TestPhoneDeviceSecretSealed`.
- [ ] Re-arm (immediate token rotation plus `boot_armed`), admin password generated and sealed on create (AAD `phone-admin:<id>`), reveal with audit and rotate. `TestPhoneAdminPassword`.
- [ ] Handlers for every route of contract 7 and spec Interfaces, OpenAPI entries; `TestVersionAndOpenAPI` still routes every documented operation. `TestPhoneCRUD`.
- [ ] Preview: render through `prov.Render` with the secret and token replaced by `********` before rendering, so masking cannot miss a template that transforms them; no fetch record; `TestPreviewMasksSecrets`.
- [ ] CSV import: parse, validate every row (MAC, vendor, extension, duplicates within the file and against the store), dry run, then apply in one transaction; table test with a 500-row file.
- [ ] Firmware: multipart upload streamed to MinIO under `<vendor>/<sha256>/<filename>` with the hash computed while streaming, 512 MiB cap, pins with `409` on deleting a pinned file; the provisioning handler streams with `Range`. `TestFirmwareHosting` (lab, MinIO from CI services).
- [ ] Settings endpoint: computed DHCP values per vendor, public, boot and CA URLs, CA SHA-256.
- [ ] Pruner: delete `prov_fetches` older than the retention once a day, under a Valkey lease so one replica runs it.
- [ ] Run the full gate.

## Task 4: Redirect clients (branch prov-redirect)

Files: `internal/prov/redirect/` (`yealink.go`, `poly.go`, `grandstream.go`, `snom.go`, `fanvil.go`, `worker.go`, tests with `httptest` servers per vendor).
Interfaces: implements contract 5; the worker consumes the `prov_redirect_jobs` queue of Task 1's migration, writes the phones' `redirect_status`, and uses the store.

- [x] Snom: SRAPS REST with Hawk HMAC-SHA256 (look up the `setting_server` setting id, then create or update the endpoint with the phone's URL), XML-RPC `redirect.registerPhone` as the fallback (settings `{"api":"xmlrpc"}`, the same key ID and secret as basic auth). `TestHawkVectors` pins Hawk to the spec's examples.
- [x] Yealink: RPS JSON API v3.6 (HMAC-signed headers; create Hello's server entry once, then `device/add` with `uniqueServerUrl`, `device/delete`); YMCS v2 (OAuth2 client credentials) only when the account settings select it, with the serial number when MAC-only registration is not enabled.
- [x] Grandstream: GDMS OAuth token and signed calls; `device/add` with MAC and serial into the configured site; `Caps.RegistersURL` false, and the UI states the one-time site setting.
- [x] Poly and Fanvil: `Supported` false and `ErrUnsupported` from every call, with no network call, so the UI shows the manual step.
- [x] Credentials from env (spec S-11 names) take precedence over stored ones and mark the account `fromDeployment` (`Deployment`, used by the worker; the API's `fromDeployment` flag is Task 3's).
- [x] `TestRedirectLive` (`HELLO_PROV_LIVE_REDIRECT=1`): per vendor, skip with the missing key named unless its credential and live-test keys exist; otherwise register the live-test device with a test URL, read it back, and restore the previous registration.
- [x] Worker: on phone create, rotate and delete, enqueue; process with exponential back-off to one hour, give up after 24 hours as `failed`; daily reconcile compares the vendor's stored URL with the phone's current one and reports drift. Runs under a Valkey lease (one replica). (Enqueueing and the lease are Task 3's store and hello-control wiring; `OpsTotal` is registered by hello-control.)
- [x] Credentials are opened only inside the client call and never formatted into errors; `TestRedirectClients` greps every log and error string for the test credentials.
- [ ] Run the full gate.

## Task 5: UI (branch prov-ui)

Files: `web/src/` (api, pages `Phones`, `PhoneDetail`, `ProvTemplates`, `ProvFirmware`, `ProvSettings`, nav entry under Directory, tests).
Interfaces: consumes contract 7 only.

- [ ] Phones: inventory table with filters (never fetched, stale, flagged), create/edit with the device picker and its secret-rotation warning, BLF editor, rotate-token dialog showing the URL once with copy, delete, CSV import with dry run then apply.
- [ ] Phone detail: fields, flags, redirect status, fetch log (paged), preview per file.
- [ ] Templates: list (built-in and copies), copy-to-edit, editor with the variable reference, server validation errors at their line, preview against a chosen phone.
- [ ] Firmware: upload with progress, list, pin per model. Settings: DHCP values with copy buttons, CA download and fingerprint, one card per redirect vendor with write-only credentials and Test.
- [ ] Write `Phones.test.tsx`, `ProvTemplates.test.tsx` and `ProvSettings.test.tsx` from spec S-16, mutation-checked.
- [ ] Run `pnpm typecheck`, `lint`, `test` and `build`, then `procoder check`.

## Task 6: Lab, kw and docs (lead, branch prov-contracts)

Files: `test/provclient/` (request sequences and User-Agents per vendor; the parsers are Task 2's), `test/integration/lab_prov_test.go`, `deploy/docker-compose/compose.yaml` (the provisioning listener with a lab CA and certificate generated at test start), `deploy/kuvryn-sync/kw/resources.yaml` (`hello-prov` Service, Ingress, Certificate, the `HELLO_PROV_*` env on hello-control, the optional `hello-prov-redirect` secret env), `deploy/kuvryn-sync/kw/` (a comment-only skeleton for the redirect sops secret, like the existing ones, until the user supplies it), `deploy/kuvryn-sync/README.md` (the secrets table gains `hello-prov-redirect` and its keys), `test/deploy/` (`TestKwProvisioningIngress`, `TestDocsProvisioningLinks`), `docs/provisioning.md`, `docs/phones.md`, `README.md`.
Interfaces: consumes everything above.

- [x] Merge the core, control, redirect and ui branches, resolving conflicts hunk by hunk. Run the full gate. _(Each landed on main by its own PR, #27–#30; this branch starts from that main.)_
- [x] `test/provclient`: each vendor's sequence for its representative model (spec S-18), with fallbacks (Poly `<mac>.cfg` then `000000000000.cfg`; Grandstream `cfg<mac>.xml`, `cfg<mac>`, …) and the vendor User-Agent format.
- [x] `TestProvisioningAsVendors`: create an extension and a phone per vendor through the API, fetch over HTTPS with `provclient`, parse, register a `test/sipua` phone through Kamailio with the parsed credentials, expect `200 OK`. Extend `TestNoSecretsInLogs`.
- [x] kw manifest and `TestKwProvisioningIngress` (`cluster-ca` certificate, the optional redirect-secret env).
- [x] `docs/provisioning.md` per spec S-19 and `TestDocsProvisioningLinks`; link from `docs/phones.md`; README configuration table.
- [x] Run `HELLO_DOCKER=1 go test -timeout 25m ./test/integration/` on CI (pass), then the full gate.
- [ ] After merge: pin images, Sync to kw, and check live from the LAN: one real or emulated phone per available vendor fetches through `prov.hello.kw.watteel.lab` (DHCP boot hand-off included) and registers. Once the user has supplied the `hello-prov-redirect` secret, run `TestRedirectLive` on kw for Snom, Yealink and GDMS; record the evidence, or the named missing keys, in the stories.

## Acceptance criteria

See `.procoder/specs/phone-auto-provisioning-service.md` — each criterion cites its named test; `TestProvisioningAsVendors` in the lab is the end-to-end proof.
