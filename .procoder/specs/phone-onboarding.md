# phone-onboarding

Status: approved

Source: the user's request of 2026-10-25 (`.procoder/ask/decisions.md`): take a brand-new phone, join it to the network with DHCP option 66 pointing at `http://prov.hello.kw.watteel.lab/p/boot/`, and have it show up in a queue that phones are created from. Builds on `phone-auto-provisioning-service.md` (the `/p/` endpoint, S-4, S-10).

## Problem

A phone must be added by MAC before it can provision. An unknown MAC's request gets an empty 404 and leaves no trace, so the operator has to read the MAC off a label or the phone's screen and type it in.

## Users

- **Operators and administrators:** see the phones that asked, adopt them onto an extension (one or many), dismiss or ignore them.

## In scope

- [S-1] **Capture.** An unknown MAC's request at the boot path (the per-MAC file of any vendor, and a common file whose User-Agent carries a MAC) is answered as before, an empty 404 with nothing served, and upserted into `onboarding_candidates`: MAC, vendor, model and firmware (from the User-Agent, else the file name's vendor), source IP, the requested (redacted) path, first_seen, last_seen, request_count.
- [S-2] **Limits.** Per-source-IP limit of 30 candidate writes a minute; at most 500 candidates (new MACs ignored and counted; queued ones still refresh); a candidate unseen for 30 days expires; only well-formed device MACs; a MAC that is a phone is never written; an ignored MAC is never written. HA-safe: PostgreSQL only (one upsert; an advisory lock for new rows).
- [S-3] **API** (operator and admin; every action audited): `GET /onboarding/candidates`; `POST /onboarding/candidates/{mac}/adopt` (extension by id, number or user; label; vendor/model override); `POST /onboarding/candidates/{mac}/dismiss` (`ignore`); `POST /onboarding/adopt` (bulk, all or none, consecutive from `startNumber` or per row); `GET /onboarding/ignored`; `DELETE /onboarding/ignored/{mac}`. Adopt is the manual phone creation, so validation, device creation, sealed secrets, boot arming and redirect registration are identical.
- [S-4] **UI.** Directory → Onboarding: table (model, MAC, IP, firmware, first/last seen, requests), Adopt, Bulk adopt, Dismiss/Ignore, the ignore list, auto-refresh every 10 s, a sidebar badge with the count, and the note that the phone picks up its configuration on its next retry or after a reboot.
- [S-5] **Metrics.** `hello_onboarding_candidates`; `hello_onboarding_captures_total{outcome}`.

## Out of scope

- Serving anything to an unadopted phone, or adopting without an operator.
- Capturing phones that ask over a path other than `/p/boot/`.
- Changing the vendor redirect services or TR-069.

## Constraints

- Migration `00028_onboarding.sql`, additive. No in-memory-only state. A queued phone's request is not a denial towards the source's block. Phone configs and credentials are never printed or stored by this feature; the adopt answer carries no token.

## Data

`onboarding_candidates(mac PK, vendor, model, firmware, source_ip, requested_path, first_seen, last_seen, request_count)`; `onboarding_ignored(mac PK, vendor, model, created_by, created_at)`.

## Edge cases

- A phone that reports no model: the operator types one when adopting.
- An expired row seen again before the prune starts afresh.
- A candidate adopted or dismissed by another operator meanwhile: 404 on the second action.
- A phone created by hand or CSV import removes its candidate.

## Acceptance criteria

- An unknown Grandstream, Yealink, Snom, Fanvil or Poly MAC at the boot path gets an empty 404 and one candidate with the parsed vendor, model, firmware, IP and path.
- A known MAC, a malformed MAC, an ignored MAC and a source over its limit write nothing; a full queue ignores a new MAC and counts it.
- Concurrent captures never exceed 500 rows.
- Adopting creates the phone armed, deletes the candidate and audits both; a failing row of a bulk adopt changes nothing.
