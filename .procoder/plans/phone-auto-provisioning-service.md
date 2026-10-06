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

1. **Schema:** `migrations/00006_provisioning.sql`, as committed (spec Data section).
2. **Types:** `internal/prov/types.go`, as committed:
   - `Vendor` (`yealink`, `poly`, `grandstream`, `snom`, `fanvil`, `generic`)
   - `FileKind` (`common`, `device`, `master`, `firmware`, `ca`, `boot`, `upload`, `other`)
   - `Result` (the spec S-14 list, as string constants)
   - `Phone`, `Line`, `Server`, `BLFKey`, `ProvInfo`, `FirmwareInfo`, `TimeInfo` and `RenderData` (the template variables of spec S-8, field names as documented there)
   - `Template{ID, Vendor, ModelGlob, Priority, Name, Files []TemplateFile, BuiltinRef, Version}`, `TemplateFile{Pattern, ContentType, Body}`
   - `FetchRecord` (one `prov_fetches` row)
   - the functions in its trailing comment, implemented by Task 2 with exactly those signatures: `NormalizeMAC(string) (string, error)`, `MatchFile(v Vendor, model, mac, name string) (FileKind, bool)`, `Resolve(phone Phone, override *Template, all []Template) (Template, bool)`, `Render(ctx, Template, file string, RenderData) ([]byte, error)`, `Validate(Template) []FieldError`, `NewToken() (plain string, hash []byte)`, `RedactPath(string) string`.
3. **Secret sealing:** `internal/secret` AADs `device:<id>`, `phone-token:<id>`, `redirect:<vendor>`. hello-control seals; the renderer opens.
4. **Store interface for the handler** (`internal/prov/store.go`, as committed): `PhoneByToken(ctx, hash []byte) (PhoneRecord, error)` (current or in-grace previous, reporting which), `MarkFetched(ctx, phoneID, FetchState) error`, `PromoteToken(ctx, phoneID) error` (called on the first fetch with a new token), `FlagTokenExposed(ctx, phoneID) error`, `RenderInputs(ctx, phoneID) (RenderData, Template, error)`, `InsertFetches(ctx, []FetchRecord) error`. Task 3 implements it in `internal/store`; Task 2 tests against an in-memory fake.
5. **Redirect client interface** (`internal/prov/redirect/redirect.go`, as committed): `Client{Vendor() prov.Vendor; Capabilities() Caps; Check(ctx) error; Register(ctx, mac, serial, url string) error; Unregister(ctx, mac string) error}`, `Caps{RegistersURL, NeedsSerial, Supported bool}` and `ErrUnsupported`.
6. **Valkey keys:** spec Data section; the limiter lives in `internal/prov/ratelimit.go`.
7. **HTTP JSON** (camelCase, Phase 1 error envelope and list shape; validation 400s carry `fields`):
   - **Phone:** `{"id","mac","vendor","model","label","deviceId","extensionId","extensionNumber","templateId","blf":[number...],"enabled","tokenExposed","uaMismatch","bootArmed","bootReclaimed","redirectStatus":{"state","reason","at"},"firstFetchAt","lastFetchAt","lastFetchIp","lastFetchUa","lastFetchFile","firmwareSeen","createdAt","updatedAt"}`. Create, rotate and re-arm responses add `"provisioningUrl"` once. `POST …/admin-password/reveal` returns `{"adminPassword"}` and writes an audit row; `POST …/admin-password/rotate` returns 204.
   - **Create:** `{"mac","vendor","model","label","extensionId","deviceId"?,"blf","enabled"}`; with no `deviceId` a device is created; with one, the response includes `"secretRotated": true`.
   - **Fetch:** `{"at","ip","userAgent","path","kind","result","status","bytes"}`.
   - **Template:** `{"id","vendor","modelGlob","priority","name","files":[{"pattern","contentType","body"}],"builtin","builtinRef","version","updatedAt"}`.
   - **Firmware:** `{"id","vendor","modelGlob","version","filename","size","sha256","uploadedAt","pinned"}`.
   - **Redirect account:** `{"vendor","enabled","hasCredentials","fromDeployment","settings",…,"lastCheckAt","lastCheckResult","supported"}`; credentials accepted on PUT, never returned; `fromDeployment` accounts (env from the `hello-prov-redirect` secret) are read-only.
   - **Settings:** `{"publicUrl","bootUrl","caUrl","caSha256","dhcp":[{"vendor","option","value"}],"sipServer"}`.
   - **CSV import:** dry run → `{"rows":[{"line","mac","errors":[...]}],"ok":bool}`; apply → the same, plus `"created"`.
8. **Configuration revision and audit:** every phone, template, firmware, pin and redirect-account change writes an `audit_events` row in the same transaction. These changes do not bump `config_revision` (hello-sip does not read them), except device binding, which changes HA1 values and so bumps it as Phase 1 device changes do.

## Task 1: Shared contracts (lead)

Files: `migrations/00006_provisioning.sql`, `internal/prov/types.go`, `internal/prov/store.go`, `internal/prov/redirect/redirect.go`, `internal/config` (the `HELLO_PROV_*` settings of the spec, plus `HELLO_PROV_TLS_CERT` / `HELLO_PROV_TLS_KEY` for the compose lab, where no ingress terminates TLS), this plan.
Interfaces: everything listed in Shared contracts.

- [ ] Write the migration and confirm it applies and rolls back with `HELLO_TEST_DATABASE_URL=… go test -run Migrate ./test/integration/` → ok.
- [ ] Write `types.go`, `store.go` and `redirect.go` with doc comments; `go build ./...` → ok.
- [ ] Add the config fields with defaults and validation (`HELLO_PROV_PUBLIC_URL` required when the listener is enabled; durations and CIDRs parse) and run `go test ./internal/config/` → `TestLoadProv*` pass.
- [ ] Commit to `prov-contracts`, then branch `prov-core`, `prov-control`, `prov-redirect` and `prov-ui`, each in its own worktree.

## Task 2: Provisioning core (branch prov-core)

Files: `internal/prov/` (`files.go` vendor file sets and model-ID tables, `resolve.go`, `render.go`, `token.go`, `handler.go`, `ratelimit.go`, `audit.go`, `metrics.go`, `builtin/` embedded templates for the five vendors, tests and `bench_test.go`).
Interfaces: produces contract 2's functions and `NewHandler(Store, Limiter, Opener, Options) http.Handler`; consumes contract 4 through a fake.

- [ ] File sets: for each first-class vendor, the request names of spec S-7 and the vendor reference table, MAC case included, mapped to a `FileKind`; the Yealink model-to-hardware-ID table; `TestVendorFileSets` with every documented path plus cross-vendor negatives.
- [ ] Built-in templates: one file set per vendor rendering every spec S-5 field; `TestRenderedConfigContents` parses each vendor's output (the parsers come from `test/provclient`, written here first and owned by this task) and asserts each field; renders twice and compares bytes.
- [ ] Resolution and validation: override, then priority, then glob specificity, then id; parse, variable whitelist, 100 ms deadline, 256 KiB cap; `TestTemplateResolutionAndValidation` includes a template that tries `{{.}}` method calls, `call` and range over huge input.
- [ ] Tokens: `NewToken` (32 bytes, base32 lowercase, no padding), current/previous lookup, promotion on first new-token fetch, grace expiry, `immediate` revocation; `TestTokenRollingRotation`.
- [ ] Handler: the routes of spec S-4; HTTPS detection (TLS or `X-Forwarded-Proto` from `HELLO_PROV_TRUSTED_PROXIES`), client IP from the trusted last hop, allowlist checks, MAC-in-name check, UA evidence (`ua_mismatch`), ETag/304, empty `404` for every denial, discarded capped uploads, `503 Retry-After` on store outage; `TestProvEndpointAuth`.
- [ ] Boot path: the non-secret common bodies per vendor (CA install, re-check, boot URL) and the trust-on-first-use hand-off of spec S-10: a per-vendor bootstrap body carrying only the CA and the per-device HTTPS URL; disarm by a conditional update (`UPDATE … WHERE boot_armed` returning the row) so two racing requests cannot both win; `boot_reclaim`, `boot_denied` (outside `HELLO_PROV_BOOT_CIDRS`) and disarm on the first HTTPS fetch. `TestBootTrustOnFirstUse`, including the concurrent-claim case. The store interface gains `ClaimBoot(ctx, mac) (PhoneRecord, bool, error)`.
- [ ] Rate limiter: Valkey sliding windows per IP, per denied IP and per phone, the 10-minute block, the in-memory fallback; `TestProvRateLimit` with two handler instances on one Valkey, then with Valkey stopped.
- [ ] Audit writer: buffered channel, batch insert every second or 200 rows, redacted paths, drop-and-count when full or the store fails; `TestFetchAudit`.
- [ ] Metrics of spec S-17; `TestProvMetrics`. Benchmark (`HELLO_BENCH=1`): ETag re-check under 5 ms p99, render under 20 ms p99.
- [ ] Run the full gate; mutation-check the MAC-in-name check, the plain-HTTP refusal, the grace expiry and the redaction.

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
Interfaces: implements contract 5; the worker consumes a small queue table or the phones' `redirect_status` (decided in Task 1's migration) and the store.

- [ ] Snom: SRAPS REST with Hawk HMAC-SHA256 (look up the `setting_server` setting id, then create or update the endpoint with the phone's URL), XML-RPC `redirect.registerPhone` as the fallback.
- [ ] Yealink: RPS JSON API v3.6 (HMAC-signed headers; create Hello's server entry once, then `device/add` with `uniqueServerUrl`, `device/delete`); YMCS v2 (OAuth2 client credentials) only when the account settings select it, with the serial number when MAC-only registration is not enabled.
- [ ] Grandstream: GDMS OAuth token and signed calls; `device/add` with MAC and serial into the configured site; `Caps.RegistersURL` false, and the UI states the one-time site setting.
- [ ] Poly and Fanvil: `Supported` false and `ErrUnsupported` from every call, with no network call, so the UI shows the manual step.
- [ ] Credentials from env (spec S-11 names) take precedence over stored ones and mark the account `fromDeployment`.
- [ ] `TestRedirectLive` (`HELLO_PROV_LIVE_REDIRECT=1`): per vendor, skip with the missing key named unless its credential and live-test keys exist; otherwise register the live-test device with a test URL, read it back, and restore the previous registration.
- [ ] Worker: on phone create, rotate and delete, enqueue; process with exponential back-off to one hour, give up after 24 hours as `failed`; daily reconcile compares the vendor's stored URL with the phone's current one and reports drift. Runs under a Valkey lease (one replica).
- [ ] Credentials are opened only inside the client call and never formatted into errors; `TestRedirectClients` greps every log and error string for the test credentials.
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

- [ ] Merge the core, control, redirect and ui branches, resolving conflicts hunk by hunk. Run the full gate.
- [ ] `test/provclient`: each vendor's sequence for its representative model (spec S-18), with fallbacks (Poly `<mac>.cfg` then `000000000000.cfg`; Grandstream `cfg<mac>.xml`, `cfg<mac>`, …) and the vendor User-Agent format.
- [ ] `TestProvisioningAsVendors`: create an extension and a phone per vendor through the API, fetch over HTTPS with `provclient`, parse, register a `test/sipua` phone through Kamailio with the parsed credentials, expect `200 OK`. Extend `TestNoSecretsInLogs`.
- [ ] kw manifest and `TestKwProvisioningIngress` (`cluster-ca` certificate, the optional redirect-secret env).
- [ ] `docs/provisioning.md` per spec S-19 and `TestDocsProvisioningLinks`; link from `docs/phones.md`; README configuration table.
- [ ] Run `HELLO_DOCKER=1 go test -timeout 25m ./test/integration/` on CI (pass), then the full gate.
- [ ] After merge: pin images, Sync to kw, and check live from the LAN: one real or emulated phone per available vendor fetches through `prov.hello.kw.watteel.lab` (DHCP boot hand-off included) and registers. Once the user has supplied the `hello-prov-redirect` secret, run `TestRedirectLive` on kw for Snom, Yealink and GDMS; record the evidence, or the named missing keys, in the stories.

## Acceptance criteria

See `.procoder/specs/phone-auto-provisioning-service.md` — each criterion cites its named test; `TestProvisioningAsVendors` in the lab is the end-to-end proof.
