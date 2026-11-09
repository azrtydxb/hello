# pbx-gaps

Status: approved (scope, 2026-10-26). The design defaults under "Defaults chosen for open points" are the working choices; changing one is a spec edit.

Source: the user's decision of 2026-10-26, "Build every unbuilt item from the 2026-10-26 audit", with all twelve competitor gaps chosen (`.procoder/ask/decisions.md`, section "Build every unbuilt item from the 2026-10-26 audit"). Tracker rows are in `.procoder/tracking/unbuilt.md` (rows 118 to 129). Competitor evidence is in `docs/competitive-report.md`. It reads with and amends:

- `.procoder/specs/phonebooks-ptt.md`: the phonebook model (`phonebook.Key`, the delivered set, personal-book privacy). Its "no LDAP" out-of-scope line is superseded for phones by S-24 to S-26.
- `.procoder/specs/phone-feature-parity.md`: decision 15 and its LDAP out-of-scope line are superseded the same way. Its S-n numbers are its own; this file's S-n are local to this file.
- `.procoder/specs/messaging-channels.md`: the house pattern for a spec that spans several modules.

Baseline: `main` at b64cc56 (the checkout this spec was written against). Migrations run through `00040_call_queues.sql`. Code cited below is on that checkout; a name that is not on it is marked as new.

Local numbering: S-n in this file is this spec's own. Other specs' S-n are cited with their file name.

## Problem

Hello has the core of a PBX (routing, ring groups, IVR, queues, voicemail, MOH, conference, phone provisioning) but several things that buyers of a small or mid-size PBX expect are missing, or present only in part:

- No way to restore the system, and no scheduled backup. Any loss of the database or the object store is total.
- Calls to a user's mobile ring only the Hello devices. Simultaneous or sequential ring to an external number is missing.
- Spam and unwanted callers cannot be blocked by number. Anonymous callers and callers from countries the business does not serve reach the phones.
- A caller who waits in a queue cannot be called back and keeps no place in line.
- Logins have only a password. There is no second factor.
- Callers cannot find a colleague by name. A boss and an assistant cannot share a line.
- Voicemail groups, fax (receive and send, with fax-to-email) and a second voicemail language (Dutch) are missing.
- The call history does not show the codec each leg used.
- Phones cannot read a company directory over LDAP. The phone-book work delivers XML only.

This spec covers all twelve. Each is designed against the code that exists, and each is split into PRs that can run in parallel where the files allow.

## Users

- **Administrators** configure backups and destinations, blocklists, trunk rules, 2FA policy, fax destinations and LDAP.
- **Operators** manage queues and callbacks, voicemail groups, blocked-call reports and fax queues.
- **Phone users** use follow-me and divert codes, dial-by-name, voicemail and the boss divert code, and see a directory on the phone.
- **Users with a mailbox** choose the voicemail language, enable 2FA and receive faxes by email.
- **Callers** reach dial-by-name, get a queue callback, and hear a blocked-call announcement when one applies.
- **Developers** run each item's acceptance tests in the integration tier and the phone emulators.

## Defaults chosen for open points

These settle the design questions the user left open. Each is listed for confirmation.

- **D1 Backup format and encryption.** A PostgreSQL custom-format dump (`pg_dump -Fc`) with MinIO objects copied by server-side copy. The archive is encrypted with AES-256-GCM, key from HKDF-SHA256 over `HELLO_SECRET_KEY` (stdlib `crypto/hkdf`), so no new module is needed. The image gains the `postgresql-client` package.
- **D2 Backup schedule and retention.** Daily at 02:00 server time. The last 14 archives are kept. The destination is the MinIO bucket `hello-backups`. An S3-compatible destination is optional.
- **D3 Restore is offline for writes.** A restore enters maintenance mode, where API writes get `503` and calls keep running on the current snapshot. A restore needs an admin and a typed confirmation, and it has a CLI form for when the API is down.
- **D4 Follow-me codes.** `*74` turns follow-me on and `*75` turns it off, for the extension's own profile. Each is checked against the existing dial-number space at save time.
- **D5 Follow-me prompts.** External follow-me legs require "press 1 to accept" by default, so carrier voicemail cannot take the call. DND on the extension stops follow-me. Call forward unconditional (CFU) wins over follow-me.
- **D6 Blocked-call response.** `603 Decline` by default, per trunk, with an optional `486` alternative. Anonymous reject is off per trunk by default. Country restriction is off by default.
- **D7 Unparseable caller numbers.** When a trunk has a country restriction, a caller number that cannot be normalised is blocked. Without a restriction it is allowed.
- **D8 Queue callback timing.** The callback is offered when the estimated wait exceeds 120 s, or when the caller presses `2` on the queue prompt. Three attempts, 2 minutes apart, and the request expires after 2 hours.
- **D9 TOTP parameters.** RFC 6238 with SHA-1, 6 digits, 30-second steps, one step of tolerance either way, and replay protection. Implemented in stdlib with no new module. Ten single-use recovery codes.
- **D10 2FA scope.** Applies to interactive sessions only. API tokens and service accounts are unaffected. An admin can require 2FA per user or per role, but the server refuses to require it for admins until the setter has 2FA enrolled, so no admin can be locked out by a policy change.
- **D11 Dial-by-name.** T9 keypad spelling of the first three letters of the name, then `#`. Matches come from the extensions book with `directory_hidden` excluded. At most three names are read out. A single match is connected directly. A feature code `*411` reaches the same action from a phone.
- **D12 Voicemail group broadcast.** Copy on delivery: one stored object, one row per member box. A broadcast is recorded with `*63<group>` from a member's own extension. Only members may broadcast. Members max 200. A message max 180 s by default.
- **D13 Fax engine.** `spandsp` (LGPL-2.1) through cgo, in a separate binary `cmd/hello-fax` in its own image. The SIP core stays pure Go. PDF rasterisation for outbound fax uses `pdftoppm` (poppler-utils) as an argv-only subprocess with a private temp directory. Received pages are written as 1-bit rows and encoded to PDF with stdlib `compress/zlib`.
- **D14 Fax mode default.** G.711 passthrough on all trunks. T.38 is used only on trunks whose `fax_mode` is `t38`, after the carrier offers it.
- **D15 Fax email.** The PDF is attached and sent to the mailbox owner's address, or to an address set on the fax destination. The body carries the sender number, as the CDR does for the owner. Faxes are kept 90 days.
- **D16 Boss/secretary.** One assistant per boss, and an assistant may serve many bosses. Calls to the boss ring both sets of devices at once, and the first to answer wins. CFU wins over the link. Outbound calls placed by the assistant on the boss's behalf are off by default.
- **D17 Languages.** `en` is the default everywhere. `nl` is available as a per-mailbox voicemail language. A language is used only if its table is complete (`voiceprompt.Languages()`). No caller-side language menu is added in this build.
- **D18 Codec in CDR.** A `cdr_legs` table, written with the CDR in one statement, like `cdr_page_legs` and `cdr_queue_legs`. A summary cdrs.codec holds the caller-side codec. Codec names match the names `internal/media` already uses.
- **D19 LDAP.** LDAPS on 636 is the default, with TLS 1.2 minimum. StartTLS on 389 is optional. Plain LDAP is refused. Anonymous bind is refused. Each phone binds with its own DN and a generated 32-character password. Read-only. Paged results are supported. The LDAP wire codec is the new module `github.com/go-asn1-ber/asn1-ber` (MIT), named here for review; the server logic is in-house.
- **D20 Dependencies.** New Go modules: `github.com/go-asn1-ber/asn1-ber` (LDAP). Test-only: `github.com/go-ldap/ldap/v3` (LDAP client in tests). QR rendering in the web bundle is optional and uses a local library with no third-party service. The fax image's system packages are `spandsp` and `poppler-utils`.

## Common rules (S-1)

- [S-1] Every item in this spec follows these rules unless the item says otherwise.
  - **Feature codes.** New codes are registered with the other feature codes. Save-time checks (`checkDialNumber` in `internal/store/dialspace.go` and `dialGuard`) refuse a clash. Codes added here: `*74` and `*75` (follow-me), `*411` (dial-by-name), `*63<n>` (voicemail group broadcast), `*64` (boss divert). A code must not collide with the existing `*72`/`*73`, `*90` to `*93`, `*78`/`*79`, `*97`, `*2`, `*1`, `*8<n>`, `*80`, park, pickup, MOH and time-condition codes.
  - **Prompts.** Every prompt key a PR adds is added to `internal/voiceprompt/english.go` and `dutch.go` in that PR. `TestCatalogueComplete` must pass.
  - **API.** Each route is in `internal/api/routes.go` with its scope and role, and in `internal/api/openapi.json`. The OpenAPI conformance test must pass.
  - **Audit.** Configuration changes use `configChange`. Security events (2FA enrolment and reset, backup and restore, LDAP credential set and rotate) are audited with actor and target ids. No names, numbers, codes, secrets or message bodies appear in audit rows, logs or metric labels.
  - **Migrations.** Additive goose migrations with a `Down`. The number is the next free one at merge (see Data).
  - **No PostgreSQL call on the SIP path.** Runtime data (blocklist, boss links, follow-me profiles, ring state) rides the configuration snapshot (`internal/snapshot`), or is held in call memory until the call ends.
  - **HA.** Anything that must survive a node loss is in PostgreSQL. Singleton jobs use `TryLock` (`internal/store/ai_core.go`), so exactly one replica runs them.
  - **Gate per PR.** gofmt, `go vet`, golangci-lint (0 issues), `go test -race ./...`, procoder check (0 blocking), Vitest for every new or changed page, and the integration tier (DB tests) dispatched and green before merge and deploy. Deploy to kw is through Kuvryn Sync. No workload runs on the author's Mac. CI runs on Arc.
  - **Docs.** Each item updates the doc named in S-27.

## In scope

### A. Backup and restore

- [S-2] **Backup contents and format.** One archive per run, `hello-backup-<utc>.hbk`, holding:
  - db.dump: the PostgreSQL custom-format dump from `pg_dump -Fc`, run by the leader replica (the image gains `postgresql-client`; the binary path is `HELLO_BACKUP_PG_DUMP`, default `pg_dump`);
  - objects.json: the list of objects in the recordings, voicemail, prompt, ringtone and message-media buckets, each with key, size and SHA-256. Objects are copied to the backup bucket by server-side copy, never streamed through the pod. An object already in the backup bucket with the same SHA-256 is not copied again;
  - manifest.json: Hello version, schema version (goose version), creation time, counts, the SHA-256 of each part and the key fingerprint.
  - The archive is encrypted per part with AES-256-GCM and a random 96-bit nonce, under a key from HKDF-SHA256 over `HELLO_SECRET_KEY` with info `hello-backup-v1`. The key is never written into the archive. The fingerprint is the first 8 bytes of SHA-256 over the derived key, and restore refuses a different fingerprint.
- [S-3] **Schedule, destinations, retention and verification.** A singleton `backup_settings` row holds: enabled, cron (default `0 2 * * *`), destination kind `minio` (default, bucket `hello-backups`) or `s3` (endpoint, bucket, region, path-style flag, access key and secret sealed with AAD `backup:s3`), and `keep_count` (default 14).
  - The run is taken under `TryLock`, on the leader only. A failed run is retried once after 15 minutes. A second failure sends an email through `internal/mailer` when SMTP is configured, and always sets the metric `hello_backup_last_success_timestamp` to age.
  - `backup_runs` records each run (kind `scheduled` or `manual`, status, start and end, sizes, the object key and error class).
  - Each run ends with a verification: the manifest is re-read, and the SHA-256 of db.dump is recomputed from the stored object. A mismatch marks the run `failed` and keeps the archive for inspection.
  - API: `GET|PUT /api/v1/backups/settings`, `GET /api/v1/backups`, `POST /api/v1/backups` (manual run, admin), `DELETE /api/v1/backups/{id}` (admin, removes archive and row).
- [S-4] **Restore.** Two paths:
  - API: `POST /api/v1/backups/{id}/restore` with body `{"confirm":"RESTORE <id>"}` (admin). `?dry_run=true` verifies the manifest and every part hash and returns counts only, and writes nothing.
  - CLI: `hello-control backup restore --from <object-key>` with `HELLO_SECRET_KEY` in the environment, for when the API is down.
  - Steps: verify the manifest and every part hash; refuse when the backup's schema version is newer than the binary's; refuse a different key fingerprint; enter maintenance (writes return `503 maintenance`, SIP keeps running on the current snapshot, and no configuration is accepted); `pg_restore --single-transaction --clean --if-exists` into the database; restore each object whose key is missing or whose SHA-256 differs from the backup, by server-side copy; never delete an object absent from the backup; rebuild the snapshot; leave maintenance.
  - Every restore writes an audit row with the backup id, counts and outcome.
  - The restore test (integration tier) restores into an empty database and compares row counts for every table.

### B. Follow-me

- [S-5] **Profile and codes.** Table `follow_me` (extension id primary key, `enabled`, `strategy` as `simultaneous` or `sequential`, `ring_own_devices` default true, `timeout_seconds` 5 to 120 default 30, `confirm_external` default true, `dnd_blocks` default true). Table `follow_me_targets` (extension id, `position` 1 to 5, `kind` as `external`, `extension` or `ring_group`, `target` text, `delay_seconds` 0 to 60 default 0; primary key on extension id and position). An external target is validated like a trunk dial number, and a target equal to the extension itself is refused.
  - Codes: `*74` turns the extension's profile on, `*75` turns it off. A user may toggle only the extension that the device belongs to. No PIN.
  - API: `GET|PUT /api/v1/extensions/{id}/follow-me` (operator and above; a user through `/me` for their own extension).
- [S-6] **Ring behaviour.** Follow-me runs when a call reaches the extension's ring step, after CFU and before CFNA.
  - `simultaneous`: the extension's own devices (if `ring_own_devices`) and all targets ring at once.
  - `sequential`: each target rings for its `delay_seconds`, then the next starts. The total is capped by `timeout_seconds`.
  - An external leg is dialled through outbound routing (`internal/routing`) with the trunk's outbound caller-ID rule. The caller ID is the original caller's number when the trunk allows it, else the extension's outbound identity.
  - With `confirm_external`, an external leg plays "press 1 to accept" and is dropped after 10 s without the digit, before any bridge.
  - Any leg that answers cancels the others with reason "Call completed elsewhere".
  - `dnd_blocks` with the extension on DND skips follow-me. Emergency calls ignore DND, as routing already does.
  - At most 5 external legs per call. A forwarded external leg that comes back to Hello is caught by the existing forward-loop detection in `internal/sip/callflow.go`.
  - The call-flow trace gets a line per leg. Each external leg is a row in `cdr_legs` (S-22) with role `follow_me`.

### C. Blocklist, anonymous reject and country restriction

- [S-7] **Blocklist.** Table `call_blocklist` (id, `pattern`, `match` as `exact` or `prefix`, `action` as `block` or `allow`, `scope` as `global` or a trunk id, `reason` up to 120 characters shown only to admins, `enabled`). A pattern is stored as a `phonebook.Key` of the number (`internal/phonebook/key.go`), with a trailing `*` on `prefix` patterns.
  - Order: an `allow` entry always beats a `block` entry. Among entries of the same action, an exact match beats a prefix, and a longer prefix beats a shorter one.
  - Limit 10,000 entries. CSV import (`number,action,match,scope,reason`) has a dry run with per-row errors, in the same shape as the phonebook CSV (`phonebooks-ptt` P-7).
  - A blocked call gets `603` (or the trunk's reject code, D6) and is never forked or rung. Its CDR has disposition `blocked` and reason `blocklist`. It is counted in `hello_blocked_calls_total{reason}`. An optional announcement (an existing announcement id) plays first.
  - The rule applies to inbound calls from trunks, at the routing decision in `internal/routing/decide.go` and the inbound path in `internal/sip/inbound.go`.
  - API: `GET|POST /api/v1/blocklist`, `PUT|DELETE /api/v1/blocklist/{id}`, `POST /api/v1/blocklist/import` (operator and above; `?dry_run=true`).
- [S-8] **Anonymous reject and country restriction (per trunk).** New columns on `trunks`: `reject_anonymous` (default false), `allowed_country_codes` (text array of calling-code digits, empty means no restriction), `unknown_caller_action` (`allow` or `block`, default `block` when a restriction is set, D7), and `reject_code` (`603` default, or `486`).
  - Anonymous means a From user of `anonymous`, `Privacy: id`, or a withheld or missing caller number.
  - Order of checks on an inbound trunk call: `allow` entries (S-7), then anonymous reject, then `block` entries, then the country restriction, then routing.
  - The country check normalises the caller number with `phonebook.Key`, using `HELLO_DIAL_INTL_PREFIX`. A national number (leading `0`) is normalised with `HELLO_DIAL_COUNTRY_CODE`. An E.164 number must begin with an allowed code. A number that cannot be normalised follows `unknown_caller_action`.
  - Internal calls and calls from extensions are never checked. An anonymous internal device is never rejected.
  - Settings are in the trunk form and the trunk API. The blocked-call report is in the CDR filter (`disposition=blocked`).

### D. Queue callback

- [S-9] **Offer and kept position.** A queue gains `callback_enabled` (default false), `callback_offer_after_seconds` (default 120) and `callback_digit` (default `2`), in `internal/queue` and its store. Callback is offered when the estimated wait exceeds the threshold, or when the caller presses the digit on the queue prompt.
  - The caller must have a dialable number. An anonymous caller is not offered callback.
  - On accept, the caller hears a confirmation and hangs up. A `queue_callbacks` row is written with the queue, the number, the caller name from shared name sources only (`phonebooks-ptt` P-10), status `waiting`, attempts 0, `position_key` set to the caller's original enqueue time, and `expires_at` 2 hours later.
  - A callback keeps its place. In the queue's ordering it sorts by `position_key`, so it is served as if it had never left.
  - A request from a number already waiting in the same queue is coalesced into the existing row. At most 100 waiting callbacks per queue.
  - Queue CDR legs (`cdr_queue_legs`, migration 00040) get events `callback_requested`, `callback_dialled`, `callback_connected` and `callback_failed`.
- [S-10] **Execution, retries and HA.** `queue_callbacks` is durable in PostgreSQL.
  - A dispatch sweep, per queue under `TryLock` (or `FOR UPDATE SKIP LOCKED` on the row), finds the head callback when a staffed agent would take a call under the queue strategy. The callback's `position_key` decides its place among waiting calls.
  - The sweep originates a call to the caller's number with `originateFor` (`internal/sip/transfer.go`). External numbers go through outbound routing. The caller is rung first, and the agent is rung only after the caller answers, so no agent waits on an absent customer. Then the call is connected to the planned agent, and the row becomes `connected`.
  - No answer, busy or failure counts as an attempt. The next attempt is 2 minutes later, up to 3 attempts. After the third failure the row is `failed` (CDR disposition `callback_failed`, not an abandon).
  - A lease (`lease_until`, 60 s) is written with each claim. A row whose lease has passed is reclaimed, so a node loss does not strand a callback.
  - Cancel: `DELETE /api/v1/queues/{id}/callbacks/{callbackId}` (operator). Read: `GET /api/v1/queues/{id}/callbacks` (viewer).
  - Expired and finished rows are purged after 7 days.
  - The CDR `kind` set gains `callback`.

### E. Two-factor authentication (TOTP)

- [S-11] **Enrolment, login and recovery.**
  - Enrolment: `POST /api/v1/me/2fa/setup` returns the base32 secret and an `otpauth://` URI once. The secret is stored sealed (`internal/secret`, AAD `user:<id>:totp`) and inactive. `POST /api/v1/me/2fa/enable` with a valid code activates it and returns 10 recovery codes once. Recovery codes are stored as SHA-256 hashes, since they are 80-bit random values.
  - TOTP: RFC 6238 with HMAC-SHA1, 6 digits, 30 s steps, one step of tolerance either way (D9). users.totp_last_step stores the last accepted step, and a code at or before it is refused, so a code cannot be replayed. Implemented with stdlib `crypto/hmac`, `crypto/sha1` and `encoding/base32`. The RFC 6238 Appendix B vectors are a test.
  - Login: `POST /api/v1/login` with a 2FA user answers `202` with `{"mfa":"required","mfaToken":"..."}`. The token is random, stored as a hash in `auth_mfa_challenges`, valid 5 minutes and single use. `POST /api/v1/login/2fa` takes `mfaToken` and a TOTP code or a recovery code, and then issues the session as the password login does today.
  - Recovery codes are single use and audited. Fewer than 3 left shows a banner in the profile.
  - Limits: 5 wrong codes per challenge, and 10 per user per 10 minutes, after which the challenge is refused. Responses are the same for an unknown challenge and a wrong code.
  - The QR code for enrolment is rendered in the browser from the `otpauth://` URI with a local library. The secret is also shown as text.
- [S-12] **Enforcement, reset and lockout guard.**
  - users.totp_required is set by an admin per user. A global `require_2fa_roles` list (default empty) can name roles.
  - A required user who has not enrolled is limited, after the password step, to `/api/v1/me/2fa/*` until enrolment is done.
  - Setting `require_2fa_roles` to include `admin`, or setting `totp_required` on an admin, is refused unless the setter has active 2FA. This keeps any admin from being locked out by a policy change.
  - Reset: `DELETE /api/v1/users/{id}/2fa` (admin), audited. The reset of the last enrolled admin is refused over the API. The break-glass form is the CLI `hello-control user 2fa-reset <username>`, which needs shell access to the control plane.
  - Users whose only login is SSO (integrations P11) have no local TOTP, and their identity provider enforces 2FA.
  - API tokens and service accounts are not affected. This is written in `docs/user-accounts.md`.

### F. Dial-by-name directory

- [S-13] **Directory action.**
  - A new IVR destination kind `directory` (`IVRDirectory`, added to `IVRDestinationKinds` in `internal/store/ivr.go`), and the feature code `*411` (D11), which reach the same action from a phone.
  - The caller enters the first three letters of the name on the keypad (T9 spelling: 2 ABC, 3 DEF, and so on), then `#`. Up to 7 keys are taken.
  - The search runs over the extensions book (`phonebooks-ptt` P-1), built in memory into the snapshot, with `directory_hidden` extensions excluded. A name matches when the keypad spelling of its first or last name begins with the keys. Matching is on the letters, never a guess.
  - One match: the callee's recorded name (voicemail_boxes.name_object) or a spoken name is played, "connecting to", and the call is blind-transferred to that extension, which then uses its own voicemail if unanswered.
  - Two or three matches: the names are read in last-name order with digit choices `1` to `3`.
  - More than three: "please enter more letters". No match: "no match", then back to entry. Three failed entries send the call to the menu's invalid-entry destination.
  - Prompt keys: `directory_enter`, `directory_connecting`, `directory_one`, `directory_choose`, `directory_more`, `directory_none`, in both languages.

### G. Voicemail groups

- [S-14] **Groups and broadcast.**
  - Tables `voicemail_groups` (id, `number` unique in the dial-number space, name, enabled) and `voicemail_group_members` (group id, extension id, primary key on both). Members have a voicemail box.
  - Delivery: an IVR or route destination `voicemail_group` (new kind) or a dial of the group number leaves the message in every member's box. Each member row in `voicemail_messages` references the same stored object (D12). Deleting one member's row keeps the object while another row references it.
  - MWI is updated for each member box through the existing MWI path.
  - Broadcast: a member dials `*63<group number>` from its own extension, records the message, and it is delivered as voicemail to every member. Nothing rings. A non-member gets a `403` announcement.
  - Limits: 200 members per group; 180 s per message by default (per-group setting, max 600).
  - API: `GET|POST /api/v1/voicemail-groups`, `PUT|DELETE /api/v1/voicemail-groups/{id}` (admin to write, viewer to read).

### H. Fax

- [S-15] **Fax engine and passthrough.**
  - `cmd/hello-fax` (new) runs a T.30 terminator for receive and a sender for transmit, using `spandsp` through cgo (D13). It lives in its own image, `hello-fax`, which carries `spandsp` and `poppler-utils`. hello-sip never links spandsp.
  - Media: hello-sip relays the fax leg to hello-fax through a UDP port pair from the existing relay path, with G.711 (PCMU or PCMA only). For a fax leg, echo cancellation, silence suppression and comfort noise are off, and the jitter buffer is fixed.
  - Detection: a route to a fax destination, or CNG (1100 Hz) on an answered call to a DID set as fax.
  - Receive: raw 1-bit rows from spandsp, encoded to PDF in Go (stdlib `compress/zlib`). Limit 50 pages or 20 MB per fax. Stored in MinIO at `fax/<yyyy>/<id>.pdf`, with a `faxes` row (direction, peer number, pages, status, result code, object key, owner).
  - Send: `POST /api/v1/faxes` (multipart PDF, at most 10 MB and 50 pages, destination in E.164, optional caller ID). The PDF is rasterised by `pdftoppm` at 204 by 196 dpi, run with fixed argv in a private temp directory, and not through a shell. hello-sip originates the call through outbound routing, and hello-fax transmits. Retries: 3, 5 minutes apart, on busy, no answer or no carrier.
  - Passthrough is the default on all trunks (D14).
- [S-16] **T.38 on trunks.** A trunk's `fax_mode` is `passthrough` (default), `t38` or `off`.
  - On a `t38` trunk, hello-sip accepts an `m=image udptl t38` offer on a fax call, answers with `T38FaxVersion 0`, `T38MaxBitRate 14400`, `T38FaxRateManagement transferredTCF` and `T38FaxUdpEC t38UDPRedundancy`, and relays UDPTL to hello-fax's T.38 terminal (`spandsp` `t38_terminal`). UDPTL is relayed unmodified except for addresses, through a new relay mode in `internal/media`.
  - Switchover: a fax call on audio is re-INVITEd to `image` when CNG is heard (inbound) or when hello-fax asks (outbound). If the carrier answers `488`, the call stays on G.711 passthrough.
  - Interop with a particular carrier and a real fax machine is a hardware test (lab list). The code path and its tests are not deferred.
- [S-17] **Fax to email, API and page.**
  - A fax destination (route target kind `fax` for a DID, or IVR kind `fax`) names an owner extension. On completion the PDF is emailed through `internal/mailer` to the owner's users.email (user-accounts), or to an address set on the destination (D15). The attachment is named `fax-<yyyymmdd-hhmm>-<id>.pdf`.
  - Retention: faxes are kept 90 days (configurable 1 to 3650), purged under `TryLock` in batches of 500, with objects deleted before rows.
  - API: `GET /api/v1/faxes` (filter `direction`, `status`), `GET /api/v1/faxes/{id}/pdf`, `POST /api/v1/faxes`, `DELETE /api/v1/faxes/{id}`. Operators see all faxes. A user sees faxes whose destination is owned by their extension.
  - Page `/faxes` with received, sent, status and download.

### I. Boss and secretary

- [S-18] **Links and ring behaviour.**
  - Table `boss_links` (boss extension id unique, assistant extension id, `ring_mode` as `both` or `assistant_first`, `screening` default false, `enabled`, `divert_on` default false, `outbound_as_boss` default false).
  - `both`: the boss's devices and the assistant's devices ring together, and the first to answer takes the call. `assistant_first`: the assistant's devices ring for 10 s, then the boss's devices and the assistant's devices together.
  - An assistant's devices ring with the `internal` tag, or with the `boss` tag once phone-feature-parity S-39 has landed.
  - CFU on the boss wins, and the assistant does not ring. CFNA runs after the boss's timeout.
  - DND on the boss silences the boss's devices. The assistant still rings.
  - Answering by the assistant bridges the call and cancels the boss's legs. The CDR records `answered_by` as the assistant's extension, and the trace reads "Answered by assistant for <boss>".
- [S-19] **Screening, divert and identity.**
  - `screening` on: the assistant's answer plays "call for <boss name>, connect or take a message?". `1` connects to the boss; `2` sends the call to the boss's voicemail.
  - Divert: `*64` from the boss's own device toggles `divert_on`. While it is on, the boss's own devices do not ring and only the assistant rings. The flag is in PostgreSQL and reaches the snapshot, so every replica sees it.
  - `outbound_as_boss` on: the assistant's outbound calls use the boss's outbound caller-ID rule. Off by default (D16).
  - Divert and screening answers are audited with ids only.
  - API: `GET|PUT /api/v1/boss-links` (operator and above).

### J. Multi-language prompts and Dutch voicemail

- [S-20] **Language selection and resolution.**
  - Languages are those with complete tables in `internal/voiceprompt` (`Languages()`): `en` (default) and `nl`.
  - New column voicemail_boxes.prompt_language (null means inherit). A voicemail prompt uses the box's language. For other prompts the order is the owning menu's `prompt_language` (IVR), then the queue's `prompt_language`, then the global `PromptLanguage` setting, then `en`.
  - The box language is set in the voicemail settings page, in `GET|PUT` on the box (operator), and by the box's own user in `/me` (D17).
  - No caller-side language menu in this build.
- [S-21] **Prompt tables and pre-generation.**
  - Every prompt key added by any item in this spec is in both `english.go` and `dutch.go` (S-1).
  - Pre-generation (`prewarmVoicemail` in `internal/api/voicemail_voice.go`) runs for each language used by any box, not only the global language. Stored TTS is keyed per language under the existing prompt key layout.
  - A missing TTS clip falls back to the recorded prompt, then to English. Each language's prompts are cached in memory once loaded.
  - Dutch wording for each new key is reviewed by the user before the PR merges (a native-speaker review item in the PR template).

### K. Codec per leg in the call history

- [S-22] **Per-leg codec capture.**
  - New table `cdr_legs` (`cdr_id`, `position`, `role`, `extension`, `trunk`, `codec`, `payload_type`, primary key on `cdr_id` and `position`). `role` takes `caller`, `callee`, `forward`, `transfer`, `follow_me`, `ring_group`, `ivr`, `voicemail`, `recording`, `page`, `park`, `conference`, `callback`, `fax`.
  - Codec source: for anchored legs, the codecs the call settled on (`applyNegotiated` and `negotiatedOr` in `internal/sip/media.go`); for direct-media legs, the answer SDP; for Hello-terminated endpoints (voicemail, IVR, MOH, recording, fax), the codec the endpoint chose from the offer. Names are those `internal/media` already uses (`PCMU`, `PCMA`, `G722`, `opus`, `G729`, `iLBC`).
  - A leg that never got media has a null codec.
  - The codec is held in call memory and written with the CDR in one statement, as `cdr_page_legs` is.
  - Summary column cdrs.codec: the caller-side codec.
- [S-23] **Log line, API and page.**
  - The `call codec` INFO log line already exists on main (#190: `leg`, `media_mode`, `codec`, `clock_rate`, `payload_type`, `ptime`, `app`, `reason`, emitted by `logCodec`/`logDirectCodec`/`logTerminatedCodec` in `internal/sip/media.go`). A4 persists the same values at the same points; it does not add a second log line. A4 is delivered on branch `cdr-codec`.
  - API: `GET /api/v1/cdrs/{id}` returns `codecLegs` (array of role, codec, payload type). The CDR filter gains `codec=`. The CSV export gains a `codec` column.
  - Metric `hello_call_codec_total{codec,role}`, with the label set bounded to the known codecs and `other`.
  - The CDR page shows the codec per leg.

### L. Phone LDAP directory

- [S-24] **Listener, transport and bind.**
  - LDAP is served by hello-control on a separate listener: `HELLO_LDAP_ADDR` (default `:636`, LDAPS, TLS 1.2 minimum, certificate from the Hello CA through cert-manager, or the operator's certificate). An optional StartTLS listener is `HELLO_LDAP_STARTTLS_ADDR` (default unset, port 389). The feature is `HELLO_LDAP_ENABLED` (default false). The base DN is `HELLO_LDAP_BASE` (default `dc=hello,dc=local`).
  - Plain LDAP is refused: every operation before TLS gets `unwillingToPerform`, except StartTLS and the Root DSE read.
  - Simple bind only. Each phone has a DN `uid=<phone id>,ou=phones,<base>` and a random 32-character password, sealed and shown once on the phone page. Rotation is in the UI and API.
  - Anonymous bind is refused (`HELLO_LDAP_ANON`, default false). SASL is not supported.
  - Bind failures are limited to 5 per 10 minutes per DN and per source address. Over the limit the answer is `busy`.
  - The Root DSE lists the vendor name, the supported controls (paged results, 1.2.840.113556.1.4.319) and the StartTLS extension.
- [S-25] **Search semantics and content.**
  - A phone's search sees exactly its own delivered set (`phonebooks-ptt` P-2 and P-5): extensions, company, the group's named books, and the personal book of the user linked to the phone's extension. No other entry is visible.
  - Each entry has DN `cn=<opaque entry id>,ou=phonebook,<base>` (the DN does not carry the name), class `inetOrgPerson` with its superclasses, and attributes `cn`, `givenName`, `sn`, `displayName`, `o` (company), `telephoneNumber` (work, and other numbers), `mobile` and `homePhone`. Entries sort the same way as the XML directory (`phonebooks-ptt` P-4).
  - Filters: `and`, `or`, `not`, `equalityMatch`, `substrings`, `present`. Approximate match is treated as equality. Attributes not in the list above are not returned. `userPassword`, bind DNs and any other hidden attribute are never returned.
  - Limits: `sizeLimit` 200 (a client may ask for less), `timeLimit` 10 s, filter depth 10, filter elements 50, paged results page size at most 100. A result over the size limit returns `sizeLimitExceeded` with the first 200.
  - Writes (`add`, `modify`, `delete`, `modifyDN`, `compare`) get `unwillingToPerform`.
  - Scope: `baseObject`, `singleLevel` and `wholeSubtree` under the base.
  - The rendered set is the same one the XML directory serves, cached under the same book versions (`phonebooks-ptt` P-11).
  - Logs carry counts and result codes only, never DNs, attributes or filter values.
  - Wire codec: `github.com/go-asn1-ber/asn1-ber` (D20); everything else is in `internal/ldapdir` (new).
- [S-26] **Vendor keys and phone configuration.**
  - A phone group gains `ldap_enabled` (default false). When on, the built-in template writes the vendor's LDAP directory keys (table below). The XML directory stays delivered alongside.
  - Keys written: the host (from `HELLO_PROV_PUBLIC_URL`), port 636, TLS on, CA trust through the vendor's CA key (the Hello CA bundle the provisioning already serves), base DN, bind DN, bind password (secret flag), name and number attribute mapping, the search filters, display name and max hits 200.
  - Where a model has no LDAP support (the capability table of `phone-feature-parity` S-2), the template omits the keys and the phone keeps its XML book.
  - Test: an LDAP client in `test/provclient` (test-only `github.com/go-ldap/ldap/v3`) runs the phone's search sequence against the listener; `test/integration` covers the TLS refusal, the bind, a scoped search and a write refusal.

### M. Docs and spec edits

- [S-27] **Docs and reversals.**
  - Docs, each updated in its PR: docs/backup.md (new), docs/follow-me.md (new), docs/blocklist.md (new), `docs/queues.md` (callback), `docs/user-accounts.md` (2FA), `docs/ivr.md` (dial-by-name), `docs/voicemail.md` (groups and language), docs/fax.md (new), `docs/media.md` (codec per leg, T.38), docs/ldap.md (new, with the LDAPS trust steps for each vendor).
  - Spec edits in the LDAP PR (A7): `.procoder/specs/phonebooks-ptt.md` "Out of scope" line on LDAP, and `.procoder/specs/phone-feature-parity.md` decision 15 and its out-of-scope line, are changed to "superseded by pbx-gaps S-24 to S-26 (2026-10-26)". The 2026-10-26 entry in `.procoder/ask/decisions.md` already records the reversal.
  - `.procoder/tracking/unbuilt.md` rows 118 to 129 move to `merged #<pr>` as each PR merges.

## Out of scope

- A Hello-hosted fax carrier or a fax DID plan. Fax uses the customer's own trunk.
- Fax over WebRTC or video fax, and T.38 on a trunk that has not offered it.
- Outbound fax from a PDF that is not raster-convertible by poppler (no OCR, no conversion of Office files).
- A caller-side language menu, and a language other than `en` and `nl` (no new prompt tables in this build).
- Speech recognition for dial-by-name; the keypad only.
- Follow-me to a mobile over a non-Hello trunk without the trunk's outbound rule.
- A blocklist fed by an external reputation service. Entries are admin-managed or imported.
- Two or more assistants on one boss, and a boss-side call-screening queue.
- Callback over a trunk that does not allow outbound origination.
- A TOTP app-less method (SMS or e-mail codes), a hardware key (WebAuthn), and 2FA on API tokens.
- LDAP writes, LDAP over SASL or Kerberos, and LDAP as an identity source for Hello logins (that is integrations P8 and P11, the client side).
- Multi-tenant or per-tenant LDAP bases.

## Constraints

- No PostgreSQL call on the SIP path. The blocklist, trunk rules, follow-me profiles, boss links and dial-by-name index ride the snapshot. Codec and per-leg rows are written after the call, not during it.
- Security:
  - Blocked-call, follow-me and fax responses never reveal whether a number is on the blocklist beyond the configured code;
  - the backup key and LDAP passwords are sealed and never logged;
  - TOTP secrets, recovery codes and MFA challenges are never logged;
  - LDAP is TLS-only and anonymous-free by default;
  - fax files and PDFs are served only through the owner check.
- Compatibility: a trunk with no new settings behaves as before (no reject, no restriction, passthrough fax). A user with no 2FA logs in as before. A phone group with no LDAP setting provisions as before.
- Performance: a blocklist match adds under 5 microseconds to call setup (benchmark); a dial-by-name search over 5,000 extensions answers in under 50 ms; an LDAP search over 5,000 entries returns in under 200 ms p99.
- Dependencies: new modules are named in D20 and reviewed in their PRs. `spandsp` and `poppler-utils` are system packages in the `hello-fax` image only.
- Deployment: kw via Kuvryn Sync. The LDAP port (636, and 389 if enabled) is a Service port in the Helm chart. The fax image is a separate deployment with no public exposure. No workload runs on the author's Mac.

## Interfaces

- **SIP (hello-sip):** follow-me legs; blocked-call response (`603`, `486`); the `*64`, `*63<n>`, `*74`, `*75`, `*411` feature codes; blind transfer through the existing REFER path for dial-by-name; UDPTL and re-INVITE to `image` (S-16); no new SUBSCRIBE events.
- **Feature codes:** `*74`, `*75`, `*411`, `*63<n>`, `*64`.
- **Provisioning (phone-facing):** the LDAP listener (S-24) on 636, and 389 if enabled. Vendor keys in the templates (S-26).
- **REST (each in `internal/api/routes.go` and `openapi.json`):**
  - `GET|PUT /api/v1/extensions/{id}/follow-me`;
  - `GET|POST /api/v1/blocklist`, `PUT|DELETE /api/v1/blocklist/{id}`, `POST /api/v1/blocklist/import`;
  - `GET /api/v1/queues/{id}/callbacks`, `DELETE /api/v1/queues/{id}/callbacks/{callbackId}`;
  - `POST /api/v1/me/2fa/setup`, `POST /api/v1/me/2fa/enable`, `DELETE /api/v1/me/2fa`, `POST /api/v1/login/2fa`, `DELETE /api/v1/users/{id}/2fa`;
  - `GET|PUT /api/v1/backups/settings`, `GET|POST /api/v1/backups`, `DELETE /api/v1/backups/{id}`, `POST /api/v1/backups/{id}/restore`;
  - `GET|POST /api/v1/voicemail-groups`, `PUT|DELETE /api/v1/voicemail-groups/{id}`;
  - `GET|POST /api/v1/faxes`, `GET|DELETE /api/v1/faxes/{id}`, `GET /api/v1/faxes/{id}/pdf`;
  - `GET|PUT /api/v1/boss-links`;
  - `GET|PUT /api/v1/voicemail/boxes/{id}/language` (the box language, S-20);
  - `GET /api/v1/cdrs/{id}` gains `codecLegs`; `GET /api/v1/cdrs` gains `codec=`;
  - `GET|PUT /api/v1/phone-groups/{id}/ldap` (the group's LDAP setting), `POST /api/v1/phones/{id}/ldap/password` (rotate, shown once).
- **RBAC:** viewers read queues, callbacks, faxes they can see, blocklist, boss links and backups list. Operators manage blocklist entries, callbacks, boss links, voicemail groups, follow-me for any extension, and faxes. Admins manage backups and restore, 2FA policy and resets, trunk rules, fax destinations and LDAP settings. A user manages only their own 2FA, their own follow-me and their own box language.
- **UI:** `/blocklist`, `/backups`, `/faxes`, `/voicemail/groups`, `/boss`, `/queues/{id}/callbacks`, the 2FA section of the profile page, the follow-me section of the extension page, the language field of the voicemail page, the codec column of the CDR page, the LDAP section of the phone group page. Every new page has a Vitest test.
- **Config:** `HELLO_BACKUP_PG_DUMP`, `HELLO_LDAP_ENABLED`, `HELLO_LDAP_ADDR`, `HELLO_LDAP_STARTTLS_ADDR`, `HELLO_LDAP_BASE`, `HELLO_LDAP_ANON`, `HELLO_FAX_ENABLED`, `HELLO_FAX_ADDR`, `HELLO_FAX_PDFTOPPM`.
- **Metrics:** `hello_backup_runs_total{kind,result}`, `hello_backup_last_success_timestamp`, `hello_blocked_calls_total{reason}`, `hello_follow_me_legs_total{kind,result}`, `hello_callback_total{result}`, `hello_2fa_logins_total{result}`, `hello_directory_search_total{result}`, `hello_voicemail_group_deliveries_total{result}`, `hello_fax_total{direction,result}`, `hello_boss_divert_total{result}`, `hello_call_codec_total{codec,role}`, `hello_ldap_requests_total{op,result}`, `hello_ldap_binds_total{result}`.

## Data

Migration numbers are indicative. Each PR takes the next free number at merge (the highest on the checkout is `00040`). The indicative order is:

| Item                  | Migration (indicative)   | Changes                                                                                                                          |
| --------------------- | ------------------------ | -------------------------------------------------------------------------------------------------------------------------------- |
| S-3 backups           | `00041_backups`          | `backup_settings`, `backup_runs`                                                                                                 |
| S-7 blocklist         | `00042_blocklist`        | `call_blocklist`; `trunks` gains `reject_anonymous`, `allowed_country_codes`, `unknown_caller_action`, `reject_code`, `fax_mode` |
| S-11 2FA              | `00043_totp`             | `users` gains `totp_secret_enc`, `totp_enabled_at`, `totp_last_step`, `totp_required`; `auth_mfa_challenges`                     |
| S-22 codec            | `00044_cdr_legs`         | `cdr_legs`; cdrs.codec                                                                                                           |
| S-5 follow-me         | `00045_follow_me`        | `follow_me`, `follow_me_targets`                                                                                                 |
| S-9 callback          | `00046_queue_callbacks`  | `queue_callbacks`; `queues` gains `callback_enabled`, `callback_offer_after_seconds`, `callback_digit`                           |
| S-14 voicemail groups | `00047_voicemail_groups` | `voicemail_groups`, `voicemail_group_members`                                                                                    |
| S-20 language         | `00048_prompt_language`  | voicemail_boxes.prompt_language; `ivr_menus` and `queues` gain `prompt_language`                                                 |
| S-15 fax              | `00049_faxes`            | `faxes`; `trunks.fax_mode` if not in `00042`                                                                                     |
| S-18 boss             | `00050_boss_links`       | `boss_links`                                                                                                                     |
| S-24 LDAP             | `00051_ldap`             | `phone_groups.ldap_enabled`; `phones.ldap_password_enc`, `phones.ldap_dn`                                                        |

Renumber to the next free at merge. Each migration is additive and has a `Down`.

- **MinIO:** `hello-backups` (S-3); `fax/` prefix in the existing media bucket (S-15).
- **Valkey:** none new. Live state is snapshot or PostgreSQL.

## Edge cases

- A blocked number that is also on an `allow` entry is allowed (S-7).
- An anonymous call from a trunk that allows anonymous calls, to a number with a country restriction, is subject to the restriction, since its number is unknown (S-8).
- A follow-me target that is the extension itself is refused at save. A target that forwards back to the extension is caught as a loop (S-6).
- A follow-me profile on an extension whose device is offline rings only the targets (S-6).
- A callback whose caller hangs up before the agent is rung is marked `failed` with disposition `callback_failed` (S-10).
- Two callback requests from one number in one queue are coalesced (S-9).
- A TOTP code from the last accepted step is refused, even if it is valid (S-11).
- A TOTP user whose device clock is 90 s off is refused with the same error as a wrong code, and the message does not say the clock is the cause (S-11).
- An admin attempts to require 2FA for admins with no enrolled admin: refused (S-12).
- A dial-by-name entry with 8 letters is refused and asks for fewer (S-13).
- A dial-by-name match on `directory_hidden` extension finds nothing, and the search does not reveal that the extension exists (S-13).
- A broadcast from a non-member is refused with `403` and a short announcement (S-14).
- A voicemail-group message to a member whose box is full is kept by the other members and logged for the full box (S-14).
- A fax that fails after some pages keeps the pages received, with status `partial` and the result code (S-15).
- A T.38 re-INVITE refused with `488` continues on passthrough (S-16).
- A boss whose assistant is also the boss of someone else: each link rings only for its own boss (S-18).
- A boss divert set while the boss is on a call: the boss's other devices are not rung for the next call, and the current call is unaffected (S-19).
- A voicemail box whose language is removed from the tables falls back to English (S-20).
- An LDAP filter with more than 50 elements is refused with the LDAP result `adminLimitExceeded` (S-25).
- A phone whose LDAP password is rotated keeps its old password until the new one is provisioned (check-sync, `phone-feature-parity` S-5) and then fails the old one (S-24).
- A restore whose key fingerprint differs is refused before any row is written (S-4).

## Failure modes

- **PostgreSQL unavailable:** backups and restores fail with a classified error and no partial state (a restore is one transaction for the database part). Blocklist, trunk rules, follow-me and boss links keep working from the snapshot. Callbacks wait. 2FA login fails closed. LDAP serves the last rendered set from Valkey if present, else `unavailable`.
- **Valkey unavailable:** LDAP renders from PostgreSQL per request; the bind rate limit falls back to memory.
- **hello-fax unavailable:** the fax call is answered `488` (receive) or queued (send, with retries). Calls are unaffected.
- **Carrier does not accept T.38:** passthrough.
- **Backup destination unavailable:** the run fails, retries once, and alerts (S-3).
- **Callback caller unreachable:** three attempts, then `failed` (S-10).
- **Node loss mid-callback:** the lease expires and another node reclaims the row (S-10).
- **Phone ignores the LDAP settings:** the phone page reports "directory not queried" from the absence of LDAP requests in the log, as the XML book does (`phonebooks-ptt` failure modes).

## Acceptance criteria

- [ ] [S-2] `TestBackupArchive` in `internal/backup`: an archive built from a fixture database and two objects has a manifest with every part hash, decrypts with the key, and refuses a different key fingerprint.
- [ ] [S-3] `TestBackupRunLeader` in `internal/backup`: two replicas under `TryLock` run one backup; a failed run is retried once; the verification fails on a changed part.
- [ ] [S-4] `TestRestoreIntegration` in `test/integration` (`HELLO_DOCKER=1`): restore into an empty database matches row counts for every table, the newer-schema archive is refused, and objects are not deleted.
- [ ] [S-5] [S-6] `TestFollowMe` in `internal/sip`: simultaneous and sequential ring order, the first answer cancels the others, DND skips follow-me, CFU wins, a self-target is refused at save, and an external leg is dialled through outbound routing.
- [ ] [S-7] `TestBlocklistOrder` in `internal/routing`: allow beats block, exact beats prefix, and a blocked call is never forked (`603` only). `TestBlocklistImport` in `internal/api` checks the dry run.
- [ ] [S-8] `TestTrunkCallerPolicy` in `internal/sip`: anonymous reject, the country list (E.164, national with `HELLO_DIAL_COUNTRY_CODE`, unparseable under each action), and internal calls never rejected.
- [ ] [S-9] [S-10] `TestQueueCallback` in `internal/sip`: an offered callback keeps its `position_key`, a coalesced request adds no row, the caller is rung before the agent, three failures give `failed`, and a lease expiry reclaims a row.
- [ ] [S-11] `TestTOTP` in `internal/auth`: RFC 6238 Appendix B vectors; a replayed step is refused; recovery codes are single use; five wrong codes end the challenge.
- [ ] [S-12] `TestTOTPPolicy` in `internal/api`: requiring 2FA for admins is refused without an enrolled setter; the last enrolled admin cannot be reset over the API.
- [ ] [S-13] `TestDirectorySearch` in `internal/sip`: T9 matching, one, two and three match cases, a hidden extension absent, an 8-letter entry refused.
- [ ] [S-14] `TestVoicemailGroup` in `internal/sip`: copy on delivery with one object and one row per member, a non-member broadcast refused, MWI updated for each member, a deleted row keeps a shared object.
- [ ] [S-15] [S-16] [S-17] `TestFaxPipeline` in `cmd/hello-fax` (cgo, in the `hello-fax` image): a golden receive (raw bitmap to PDF), a send from PDF (rasterised, page count), the T.38 `488` fallback, and the email attachment naming. `TestFaxIntegration` in `test/integration` is a dispatched run.
- [ ] [S-18] [S-19] `TestBossLink` in `internal/sip`: both-ring and assistant-first orders, the assistant's answer cancels the boss, CFU wins, divert and screening flows, and the CDR `answered_by`.
- [ ] [S-20] [S-21] `TestPromptLanguage` in `internal/voiceprompt` and `internal/api`: resolution order, a Dutch box gets Dutch voicemail prompts, English stays the default, and TTS pre-generation runs per box language. `TestCatalogueComplete` passes with every new key in both tables.
- [ ] [S-22] [S-23] `TestCDRLegCodec` in `internal/store` and `internal/sip`: an anchored G.722 call has `G722` on its caller leg, a voicemail leg has the endpoint's choice, a failed-before-media leg has null, the CSV has the `codec` column, and the log line has no numbers.
- [ ] [S-24] `TestLDAPBind` in `internal/ldapdir`: plain LDAP refused, anonymous bind refused, a phone DN binds, a bad password is limited, and StartTLS works on the optional listener.
- [ ] [S-25] `TestLDAPSearchScope` in `internal/ldapdir`: a phone sees only its delivered set, a personal book only for its linked user, hidden attributes are never returned, writes refused, the size and depth limits are enforced.
- [ ] [S-26] `TestLDAPKeys` in `internal/prov`: each vendor template writes its LDAP keys only when `ldap_enabled`, the secret is masked, and a model without LDAP omits them. `TestLDAPAsVendors` in `test/integration` runs each vendor's search sequence through `test/provclient`. Every `verify` key is listed as confirmed or "needs hardware".
- [ ] [S-27] Each PR updates its doc, and `.procoder/tracking/unbuilt.md` is updated per merge; fails if a merged pbx-gaps PR leaves its tracker row at todo or wip (checked at review).

## Phased delivery

Phase 0 is this spec and the trimmed competitor report, one docs-only PR. The phases after it are one PR each.

**Wave A, all independent, start together.** Shared-file notes are in the table below the list.

1. **A1 Blocklist, anonymous reject and country restriction** (S-7, S-8). `internal/routing`, `internal/sip/inbound.go`, `internal/snapshot`, `internal/store`, routes, `/blocklist`.
2. **A2 Backup and restore** (S-2, S-3, S-4). `internal/backup` (new), `cmd/hello-control`, Dockerfile (`postgresql-client`), Helm, docs/backup.md.
3. **A3 TOTP 2FA** (S-11, S-12). `internal/auth`, `internal/api/handlers.go` (login), `internal/store` users, profile and login pages.
4. **A4 Codec per leg in CDR** (S-22, S-23). `internal/sip/media.go`, `internal/store/cdr.go`, `internal/cdr`, CDR page.
5. **A5 Multi-language and Dutch per box** (S-20, S-21). `internal/voiceprompt`, `internal/api/voicemail_voice.go`, voicemail page. Lands before other prompt-adding PRs.
6. **A6 Voicemail groups** (S-14). `internal/sip/vm*.go`, `internal/store/pbx.go`, `internal/store/ivr.go` (kind `voicemail_group`), `/voicemail/groups`.
7. **A7 Phone LDAP server** (S-24, S-25, S-26, S-27 spec edits). `internal/ldapdir` (new), `cmd/hello-control`, `internal/prov/builtin/*` (five vendors), Helm Service port, docs/ldap.md. Hard dependency: `phonebooks-ptt` phase B (the rendered delivered set) is on main. If it is not, A7 waits for it; it does not start a parallel copy of the set.

**Wave B, after the named PRs merge.**

8. **B1 Follow-me** (S-5, S-6). Needs A4 for `cdr_legs`. `internal/sip/groups.go`, `internal/sip/callflow.go`, `internal/sip/features.go`, extension page.
9. **B2 Boss and secretary** (S-18, S-19). Needs B1 (shared files). `internal/sip/groups.go`, `internal/sip/callflow.go`, `internal/sip/features.go`, `/boss`.
10. **B3 Dial-by-name** (S-13). Needs A5 (keys) and A6 (`ivr.go`). `internal/store/ivr.go`, `internal/sip/ivr*.go`, IVR editor.
11. **B4 Queue callback** (S-9, S-10). Needs A4 and A5. `internal/queue`, `internal/sip/queue*.go`, `internal/sip/transfer.go`, queues page.
12. **B5 Fax engine, email and API** (S-15, S-17). Needs A1 (`inbound.go`), A4 and A5. `cmd/hello-fax` (new), `internal/fax` (new), `internal/media` (passthrough flags), `internal/sip` (fax detection), `/faxes`, `hello-fax` image.
13. **B6 Fax T.38** (S-16). Needs B5. `internal/media` (UDPTL relay), `internal/sip` (SDP image line, re-INVITE), trunk form.

**Shared files.** A PR that touches a shared file rebases on the PR merged before it; the merge order is the list order above unless noted.

| File or area                                           | Items that change it                                                                                       |
| ------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------- |
| `internal/api/routes.go`, `internal/api/openapi.json`  | every API item                                                                                             |
| migrations (number at merge)                           | every item with a table                                                                                    |
| `internal/voiceprompt/english.go`, `dutch.go`          | A1 (blocked announcement key), A6, A5, B1 (follow-me prompt), B3, B4, B5, B6 (fax prompts); A5 lands first |
| `internal/sip/inbound.go`                              | A1, B5                                                                                                     |
| `internal/sip/groups.go`, `callflow.go`, `features.go` | B1, B2 (B2 after B1)                                                                                       |
| `internal/store/ivr.go`                                | A6, B3                                                                                                     |
| `internal/store/cdr.go`, `internal/sip/media.go`       | A4 (first), B1, B4, B5 (leg roles)                                                                         |
| `internal/sip/queue*.go`                               | B4 only                                                                                                    |
| `internal/media`                                       | B5 (passthrough), B6 (UDPTL)                                                                               |
| `internal/prov/builtin/*`                              | A7; no other item                                                                                          |
| Helm chart                                             | A2, A7, B5                                                                                                 |
| Dockerfile                                             | A2 (`postgresql-client`), B5 (separate `hello-fax` image)                                                  |
| `web/src` routes and nav                               | every web item                                                                                             |

Parallel capacity: Wave A is seven PRs with the shared files above. Wave B is six PRs, two of which share groups.go (B1 and B2) and one of which shares ivr.go (B3, after A6).

## Open questions

<!-- None. The user chose all twelve features on 2026-10-26; design details are decided in "Defaults chosen for open points" (D1 to D20); hardware and external-account checks are in the lab list below. -->

## Lab list (hardware and external accounts only)

1. A real fax machine through a carrier that offers T.38, and through passthrough on a G.711 trunk (S-15, S-16).
2. A carrier that accepts a T.38 re-INVITE (S-16).
3. One phone per vendor on LDAP: Yealink, Poly, Grandstream WP822 and WP8x6, Snom, Fanvil, each over LDAPS (S-26).
4. A mobile on a real carrier for follow-me with "press 1 to accept" (S-6).
5. A Dutch voicemail read by a native speaker (S-21).
