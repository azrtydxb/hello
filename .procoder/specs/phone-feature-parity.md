# phone-feature-parity

Status: approved

Source: the user's instruction of 2026-10-25, "add all missing features, ALL, and also the ringing thing", repeated as "every row of the WP8x6 analysis marked missing, partial or unverified, at every priority" (`.procoder/ask/decisions.md`, "Phone feature parity, distinctive ringing and doorbell"). Inputs are two Grandstream gap analyses written against `main` 496df94 on 2026-10-25:

- **[R822]** "Grandstream WP822 vs Hello PBX: gap analysis" (WP822 firmware 1.0.11.78 in the lab; sources: WP8xx Administration Guide, User Guide, Security Guidelines, release notes 1.0.11.75)
- **[R8x6]** "Hello PBX vs Grandstream WP8x6 (WP816 / WP826 / WP836): gap analysis" (two WP836 phones on 1.0.1.44 and 1.0.1.80; P-values from Grandstream's template `wp8x6_config_1.0.3.39.txt` and `wp810_822_825_config_1.0.11.83.txt`)

This spec extends `.procoder/specs/phone-auto-provisioning-service.md` (templates, firmware, tokens), `.procoder/specs/pbx-features.md` (feature codes, ring groups, voicemail, presence), `.procoder/specs/media-anchoring.md` (relay, SDP) and `.procoder/specs/trunks-routing.md` (emergency routes). It does **not** repeat `.procoder/specs/phonebooks-ptt.md`: phonebooks (its phases A to D), server paging and intercom (phase E) and multicast PTT (phase F) are being built from that spec. Where a gap here belongs to that work, this spec names the phonebooks-ptt requirement and adds only the delta, and the delta is marked **"addition to phonebooks-ptt phase X"**.

Every P-value and provisioning key below is cited to its source. A key that no source in hand confirms for the model and firmware in question is marked **verify** and is confirmed in the lab before the phase that writes it closes; a lab result that differs corrects the template and this table, not the design.

## Problem

Grandstream WP822 and WP836 Wi-Fi handsets register and call through Hello, but much of what a user does every day fails silently. The voicemail LED never lights, because Hello sends MWI as a SIP MESSAGE and refuses `SUBSCRIBE Event: message-summary`. The Park softkey and pickup codes lead nowhere. Configuration changes reach a phone only on its 24 h resync. A phone set to DND tells nobody. An instant message gets a 200 OK and is then dropped. A G.722-only offer cannot reach voicemail. The Grandstream template writes BLF keys these phones do not have, and none of the codec, keep-alive, transport, Wi-Fi, hardening or emergency settings they need. SIP runs over UDP only, with no TLS or SRTP. Neither analysis found a way to tell a door ring, a colleague and a customer apart by sound. Hello also has no notion of a door station. The user wants every gap closed, distinctive ringing on all five vendors, and doorbells, including ones that are not SIP devices.

## Users

- **Phone users** need a voicemail light that works, Park and Pickup that work from the handset, DND on the phone that Hello honours, messages that arrive, and a ring that tells a door from a customer from a colleague.
- **Administrators** need to map ring tags to tones once for every vendor, add a door station as an extension, connect a non-SIP doorbell by webhook, push a config change to a phone now, provision Wi-Fi, hardening and emergency data per site, and pin firmware per model.
- **Reception and security staff** answer the Door group, open the door with one key, and receive alarm and emergency notifications.
- **Operators** watch MWI, check-sync, park, pickup, message, webhook and TLS metrics, and use syslog from handsets when troubleshooting.
- **Developers** need each behaviour reproducible in `test/provclient` and `test/integration`, with the WP822 and WP8x6 as known models.

## Defaults chosen for open points

The user asked for everything and left the details open. These defaults settle each design question; changing one is a spec edit.

1. **MWI** uses RFC 3842 `message-summary` SUBSCRIBE plus unsolicited NOTIFY. The SIP MESSAGE MWI push is removed outright, with no flag to keep it.
2. **Feature codes** (new defaults, all configurable; they avoid the existing `*72`/`*73`, `*90`-`*93`, `*78`/`*79`, `*97`, `*2`, `##`, `*1` and the phonebooks-ptt `*8<n>`/`*80`): group pickup `*60`, directed pickup `*61<ext>`, park `*70` (system chooses the slot) and `*70<slot>` (park to a slot), retrieve `*71<slot>`, alarm `*55`, door open from a phone during a door call `*50` (sends the station's door-open DTMF).
3. **Park lot**: one lot by default, slots 701 to 720. Unretrieved parked calls return to the parker after 120 s, then go to the parker's voicemail after a further 60 s.
4. **Pickup scope**: an extension may pick up calls ringing any member of a pickup group it belongs to. With no explicit pickup group, the default scope is the ring groups the extension is a member of.
5. **DND/forward on phones**: Hello is the source of truth. Vendors with BroadSoft-style feature-key synchronisation (`as-feature-event`) get it enabled; vendors without it have local DND/forward disabled and their DND key provisioned as the Hello feature code.
6. **SIP MESSAGE**: delivered between extensions to every registered contact. When none is registered, it is stored up to 24 h (max 100 per extension) and delivered on the next REGISTER. A message to a non-extension or a trunk gets `404`/`403`; nothing is ever 200-and-dropped.
7. **Transport**: UDP stays the default for existing phones. TCP and TLS listeners are added and the Hello CA signs the SIP certificate (phones already trust it via P8433). New Grandstream WP templates use TLS plus SDES SRTP **optional** (not forced) once the TLS phase is deployed, so a phone that fails TLS lab checks can be switched back per model.
8. **Codecs**: provisioned order G.722, PCMA, PCMU, Opus (G.729, iLBC, G.723, G.726 disabled). The anchor relays any audio payload type. Voicemail, announcements and recording decode G.722 natively and Opus through one vetted pure-Go Opus decoder/encoder; the dependency is named in the plan and needs the usual review.
9. **Distinctive ringing**: seven built-in tags (internal, external, ringgroup, page, doorbell, alarm, emergency-callback) plus admin-defined custom tags. Each ring group can override the ringgroup tag with its own tag. Hello emits one canonical Alert-Info shape (S-41) and the provisioning maps it to a tone per vendor.
10. **Ringtones**: each tag maps to a vendor built-in ringer number by default. Custom WAV upload is supported for Yealink, Poly, Snom and Fanvil; Grandstream custom tones need Grandstream's own ring-tone format and are **verify**, with the built-in ringer used until then.
11. **Doorbell**: a door station is an extension of kind door. Its DTMF door-open code is stored sealed and never logged. The webhook endpoint uses HMAC-SHA256 over timestamp plus body with a 300 s replay window and a rate limit of one ring per 5 s and 60 per hour per webhook. Video preview is optional and **verify** throughout.
12. **E911**: Hello serves a minimal HELD location server from the provisioning listener, with locations assigned per phone group. Geolocation/PIDF-LO is added on emergency trunk legs only for trunks flagged for it (default off).
13. **Firmware**: WP8x6 is pinned to one lab-tested 1.0.3.x build, the newest of 1.0.3.29 to 1.0.3.39 that passes the lab run. WP822 is pinned to the lab build 1.0.11.78 until a newer one passes. A per-model minimum version blocks downgrades below a vendor floor (WP822 1.0.11.22 [R822 RN]).
14. **Wi-Fi profiles** are opt-in per phone group, with secrets sealed. A profile push is refused unless the group already has one profile marked "verified on a test phone", because a bad Wi-Fi push strands the handset.
15. **LDAP stays out of scope** because of the accepted decision of 2026-10-25 (phonebooks-ptt "no LDAP"). Every other row of both reports is built or explicitly configured.
16. **Hot desking** is not offered by either WP family [R822 §3], so it is documented as unsupported. Account sharing [R8x6 AS] is provisioned instead.

## In scope

**A. Daily-use (P1) gaps**

- [S-1] **Standard MWI.** `handleSubscribe` in `internal/sip/presence.go` accepts `Event: message-summary` (RFC 3842) for an extension's own mailbox only (the subscriber must authenticate as a device of that extension; otherwise `403`). It answers `200`, sends an immediate NOTIFY with the existing body built in `internal/sip/voicemail.go`, and sends a NOTIFY on every change (new, heard, deleted). Subscriptions ride the existing presence subscription store, with Expires clamped to 60 to 3600 s. Unsolicited NOTIFY `Event: message-summary` (no Subscription-State dialog; out-of-dialog per RFC 3842 §3 practice) goes to every registered contact of the extension that holds no subscription. The SIP MESSAGE MWI push is removed. Unknown events get `489 Bad Event` instead of `400` (closes the reg-event row too, see S-37).
- [S-2] **Model capability table.** A Go table in `internal/prov`, keyed by vendor and model glob, records: line count (WP816 2, WP822 2, WP826/WP836 3 [R8x6 AG], others from the provisioning spec), BLF key support (false for WP8* [R822 §4][R8x6 headline 1]), phonebook entry cap (WP822 500 [R822 §7], WP8x6 1000 [R8x6 §7]), PTT channel support (WP8x6 only), feature-key sync support, HELD support, minimum firmware floor and P193 minimum (60 for Grandstream [R8x6 §1]). The API, CSV import and the Phones and PhoneDetail UI use it: BLF entries are hidden and refused with `400` for models without BLF, and the line count is shown.
- [S-3] **Grandstream WP template variant.** The built-in Grandstream template `internal/prov/builtin/grandstream/device.xml` gains model guards (`.Phone.Model` matched against WP8* and against WP816/WP826/WP836 separately). For WP models it **stops writing P323 to P325** and writes the following, each P-value from [R8x6 TPL] unless marked:
  - codec order P57 to P64 as G.722, PCMA, PCMU, Opus, then none [R8x6 §6]; Opus PT P2385 = 123;
  - DTMF RFC 2833 only: P2301 = 0, P2302 = 1, P2303 = 0 [R8x6 §6]; P79 = 101;
  - SUBSCRIBE for MWI P99 = 1 [R8x6 §5] (WP822 same key [R8x6 comparison table]); voicemail access number P33 defaulting to the voicemail feature code `*97` when no per-line code is set [R822 §1];
  - OPTIONS keep-alive P2397 = 1 with a 30 s interval (interval and max-tries keys **verify**) [R8x6 §2]; WP822 RFC 5626 keep-alive P84989 = 0 [R822 §2];
  - resync P193 clamped to at least 60 [R8x6 §1]; User Protection P9996 = 0 [R8x6 §1];
  - check-sync without reboot P95030 = 0 (WP8x6 fw 1.0.3.20 and later) and NOTIFY authentication P4428 = 1 [R8x6 §1];
  - auto-answer P298 = 1, barge P26019, mute-on-answer P26071, warning tone P26072, custom Alert-Info P2356 (all addition to phonebooks-ptt phase E, which owns their values) [R8x6 §3, §8];
  - emergency numbers P8385 from Hello's emergency routes (S-28);
  - park P60069 = park code, retrieve P2336 = retrieve code (WP8x6) [R8x6 §3] (S-6);
  - IM designated account P22650 = 1 when SIP MESSAGE is enabled (S-8) [R8x6 §5];
  - WP822 Enable DND Feature P8446 and Feature Key Synchronization P2325 per S-7 [R822 §1, §3].
    The WP822 lab "Download Device Configuration (XML)" export confirms each WP822 key before its phase closes (**verify** for every WP822 key not in [R822 RN]).
- [S-4] **Codec-robust anchor, voicemail and recording.** `ParseAudioSDP` in `internal/media/sdp.go` no longer requires PCMU or PCMA for relay-only anchoring: the anchor relays whichever audio payload type both legs agree on, including G.722, Opus, G.729, iLBC and dynamic PTs, and keeps telephone-event. Endpoints that decode (voicemail, announcements, recording `*1`) answer with the first codec they support from the offer, in the order G.722, PCMA, PCMU, Opus, and only answer `488` when none is offered. Recording writes G.722 and Opus calls as decoded audio. This closes the "voicemail with wideband phones", "G.722", "Opus" and "recording of wideband calls" rows.
- [S-5] **Check-sync NOTIFY, remote resync and reboot.** hello-sip sends `NOTIFY` with `Event: check-sync` to every registered contact of a phone's device. It answers the phone's digest challenge (P4428 default on [R8x6 §1]) with the phone's SIP credentials taken from the device record (sealed HA1; the challenge uses the phone's realm). Two forms are used: `check-sync` (re-provision; WP8x6 with P95030 = 0 does not reboot) and `check-sync;reboot=true` (reboot) (the reboot parameter is **verify** per vendor). Triggers:
  - a "Resync" and a "Reboot" button on PhoneDetail, with bulk actions on Phones;
  - an automatic resync after any change to the phone's resolved template, group, lines, Wi-Fi, ring map or book assignment, debounced 30 s per phone and limited to `HELLO_PROV_PUSH_RATE` (default 20 per minute cluster-wide).
    Other vendors use their own event: Yealink and Snom `check-sync` (reboot form **verify**), Poly `check-sync` (**verify**), Fanvil `check-sync` or `resync` (**verify**). Results (`sent`, `200`, `481/404 not registered`, `timeout`) are shown on the phone page and audited. A factory reset via `Event: reset` is never sent, and the template keeps "Allow SIP Reset" off [R822 §2].
- [S-6] **Call park and retrieve.** A park lot (G-default 3) is held in Valkey under the hello:park key prefix with the slot, the parked call's anchor and the parker, so any hello-sip replica can retrieve it. Three paths park a call:
  - dialling `*70` or `*70<slot>` during a call, via the DTMF feature-code path;
  - a blind REFER to `*70` (this is what the WP8x6 Park softkey sends with P60069 [R8x6 §3]);
  - a blind REFER to `*70<slot>`.
    The parked party hears music on hold (S-33). The parker hears the slot number from a built-in announcement. `*71<slot>` retrieves the call. Timeout and return follow default 3. BLF on a slot number (`Event: dialog` for 701 to 720) shows occupancy on phones that have BLF keys. The CDR records the park and the retrieve as legs. When the anchor's owning node fails, a parked call follows the existing in-call HA handoff (`.procoder/specs/incall-ha.md`).
- [S-50] **Call pickup.** Pickup groups (a name and member extensions) are kept in the configuration snapshot.
  - Group pickup `*60` answers the oldest call ringing any extension in the dialler's pickup scope (default 4).
  - Directed pickup `*61<ext>` answers the call ringing that extension if it is in scope.
  - An INVITE with Replaces naming an early dialog in scope does the same (S-17).
    The picked-up call is bridged to the dialler, and every other ringing leg is cancelled with the Reason "Call completed elsewhere". A short error tone plays when nothing in scope is ringing. The CDR records the pickup as the answering leg. On phones the codes are offered as speed dials (S-12), and written to a vendor pickup key where one exists (**verify**).
- [S-7] **DND and forward sync from the phone.**
  - Hello serves `SUBSCRIBE Event: as-feature-event` (the BroadSoft Feature Event Package as implemented by Yealink, Poly and Grandstream) for an extension's own device. It answers with the current DND and forward state and applies `SetDoNotDisturb`/`SetForwarding` requests from the phone to the extension, through the same code path as `*78`/`*72` so presence, ring groups and BLF see one truth.
  - Provisioning enables it where supported: Yealink `account.N.feature_key_sync = 1`; Poly `voIpProt.SIP.serverFeatureControl.dnd = 1` and `.cf = 1`; Grandstream P2325 = 1 [R822 RN; R8x6 AG]. Each is **verify** on its vendor.
  - For vendors or models without it (Snom, Fanvil unless the lab shows support), local DND/forward is disabled (Grandstream P8446 = 0 [R822 RN]; others **verify**) and the phone's DND key is provisioned as a speed dial to `*78`.
- [S-8] **SIP MESSAGE between extensions.** A MESSAGE from an authenticated device to an extension number is delivered to every registered contact of that extension, or stored and delivered later under default 6. The sender gets `202 Accepted` when the message is stored and `200` when it reaches at least one contact.
  - Content types: `text/plain` only, at most 1,300 bytes, otherwise `415`/`413`.
  - Abuse limits: 30 messages per minute per sender, otherwise `429`.
  - Privacy: bodies are never logged. They appear in the self-service UI history for 30 days and are then deleted.
  - The answer-200-and-drop path in `internal/sip/features.go` is removed.
- [S-9] **Wi-Fi roam, keep-alive and dead-call detection.**
  - A CRLF keep-alive (RFC 5626 double-CRLF ping over UDP/TCP) is answered with a single CRLF over TCP and TLS, and ignored without an error log over UDP.
  - Session timers (RFC 4028) are supported. Hello honours a phone's `Session-Expires` and `Min-SE` and refreshes as the refresher when the far end does not. It requests `Session-Expires: 1800` on legs to devices of models whose capability says they support it, and tears down a call whose refresh fails, so a handset that drops off Wi-Fi mid-call no longer leaves the call hung. The anchor also ends an anchored call after `HELLO_MEDIA_DEAD_SECONDS` (default 60) without RTP in both directions, unless the call is on hold.
  - A re-INVITE carrying a new `c=` address mid-call (an IP change after a roam) is relayed and the anchor re-learns the address. A re-REGISTER from a new address replaces the old contact for the same instance (`+sip.instance` or same Call-ID).
  - The lab confirms a mid-call AP roam on the same subnet, a roam across subnets, and a handset idle for more than 1 h behind NAT [R822 §10].

**B. Distinctive ringing**

- [S-39] **Ring tags.**
  - A tag is a lowercase name of 1 to 32 characters (`[a-z0-9-]`). The built-in tags are internal, external, ringgroup, page, doorbell, alarm and emergency-callback.
  - Admins add custom tags (at most 32).
  - A ring group has an optional tag that overrides ringgroup for calls through it. The Door group's default tag is doorbell (S-46).
- [S-40] **Tag rules.** On every INVITE Hello sends to an internal device, it chooses one tag. The first match wins, in this order:
  1. the call's kind: page, intercom, alarm, door call, or a callback to an extension within 30 minutes of its emergency call;
  2. ordered admin rules (at most 100). A rule matches on any of: caller number pattern (the dial-plan pattern syntax of `.procoder/specs/trunks-routing.md`), inbound trunk, dialled DID, caller extension kind, and a time window. Its action is a tag;
  3. the ring group's tag when the call came through a ring group;
  4. external for calls that entered on a trunk;
  5. internal otherwise.
     The tag and the rule that chose it are written to the call-flow trace and the CDR as ring_tag.
- [S-41] **Header form.** On legs to internal devices Hello first strips any received Alert-Info (phonebooks-ptt P-16 already does this for every request from a trunk or phone, so tags cannot be injected). It then adds one header in the canonical form `Alert-Info: <http://127.0.0.1/Bellcore-drN>;info=hello-<tag>`, where N is the tag's Bellcore slot (1 to 5, assigned per tag in the ring map, default internal 1, external 2, ringgroup 3, doorbell 4, alarm 5, others 1). The info text serves the vendors that match on text and the Bellcore URL the vendors that only map Bellcore-dr1 to dr5 (Grandstream WP8x6 P95026 [R8x6 §3], Snom **verify**). Where the callee's vendor is known (the phone bound to the contact's device, or else the REGISTER User-Agent), the vendor table may name a different form, and that form is used instead. On page and intercom legs, the auto-answer header of phonebooks-ptt phase E takes precedence. A second Alert-Info value carrying the page tag is added only for vendors the lab shows accept two values (**verify**), so a member that does not auto-answer still rings with the page tone.
- [S-42] **Ring map.** Admins map each tag to a tone in the UI. A tone is one of:
  - a **vendor built-in ringer**, chosen per vendor from that vendor's list (Yealink 1 to 8 and the 5 Bellcore ringers, Poly ringer1 to ringer24, Grandstream system/1/2/3, Snom Ringer1 to Ringer10, Fanvil Type 1 to Type 9; all **verify** per model);
  - a **custom tone** from the tone library (S-43);
  - **silent** (no ring; visual only) where the vendor offers it.
    Every tag has a default built-in tone per vendor so nothing has to be configured. The map is global, with an optional override per phone group.
- [S-43] **Tone library.** Admins upload WAV files (PCM 8 or 16 kHz, mono, 16-bit, at most 30 s and 512 KiB; anything else `400`). They are stored in MinIO (bucket hello-ringtones) and served under the phone's token at `GET /p/<token>/ring/<name>.wav` with the same rules as other provisioning files (ETag, empty `404` denial, audit kind ringtone). Vendors that need another format get a server-side conversion only where stdlib can do it (resample and PCM); Grandstream's ring-tone format is **verify** and is not produced until the lab confirms its encoder.
- [S-44] **Provisioning of the ring map** for every vendor through the built-in templates in `internal/prov/builtin.go`, using the keys in "Vendor reference: distinctive ringing". A change to the map triggers S-5 resync for affected phones.

**C. Doorbell**

- [S-45] **Door station extension.** Extensions gain a kind, user (default) or door. A door extension:
  - has devices like any extension, so a GDS37xx, 2N, Akuvox or DoorBird registers with its own SIP credentials;
  - has no voicemail, cannot be a ring-group member, and never receives pickup, page, intercom or MESSAGE;
  - is hidden from the extensions phonebook unless the admin shows it;
  - may call only its configured targets: the Door ring group, extensions and ring groups listed on it. Any other destination is answered `403`, so a compromised door unit cannot dial out on a trunk.
  - Calls from it carry ring tag doorbell (S-40 rule 1).
- [S-46] **Door ring group.** A ring group may be marked door (any strategy). When created it gets the doorbell tag. Its members ring with the doorbell tone, and its call-flow and CDR carry kind door. The station's call button dials the group's number, configured on the station; Hello does not provision door stations.
- [S-47] **DTMF door-open code.** Each door extension may hold a door-open code: 1 to 8 characters of `0-9*#`, stored sealed and never logged or returned by the API after it is saved. During an answered call with a door extension:
  - the answering user dials `*50`, and Hello sends the code as RFC 2833 events to the door leg through the anchor (door calls are always anchored for this);
  - Hello marks the press in the call-flow trace without the digits;
  - the call stays up for `HELLO_DOOR_HANGUP_SECONDS` (default 3) and Hello then hangs up both legs if the door's "hang up after open" flag is set.
    The callee can also key the station's own code directly; it is relayed as normal DTMF. Provisioning gives each phone in a phone group that has door targets an "Open door" key or softkey dialling `*50`, where the vendor allows: Yealink DSS key type DTMF or speed dial, Grandstream WP DialDTMF softkey [R8x6 AG Programmable Keys], Poly and Snom and Fanvil soft keys (**verify** each). The web call view has an "Open door" button for the answering user's live call.
- [S-48] **Doorbell webhook.** Admins create webhooks for non-SIP doorbells. Each has a name, a secret (shown once, stored sealed), an action and an optional chime announcement (an existing announcement). The action is one of:
  - **ring the Door group:** Hello originates a call from a virtual caller named after the webhook to the Door ring group with tag doorbell. Answering plays the chime announcement, or a built-in chime, and the call ends after the announcement or after 30 s;
  - **chime page:** a one-way page to a phonebooks-ptt paging group, which plays the announcement and ends.
    The endpoint is `POST /api/v1/door/webhooks/{id}/trigger`, outside session auth. It requires an X-Hello-Timestamp header within 300 s of now and an X-Hello-Signature header equal to hex HMAC-SHA256(secret, timestamp + "." + body). The body is optional JSON (a label, at most 1 KiB). A missing or bad signature gets `401` and never reveals whether the id exists. Rate limit (default 11): above it the answer is `429` with `Retry-After`. Triggers are audited without the body, and a CDR with kind door is written.
- [S-49] **Video preview (optional, verify).** For door stations that offer video (H.264 `m=video` in their SDP), Hello passes the video stream through to callee devices whose SDP accepts it (direct media, or the anchor relays the video port pair unchanged). It also provisions the vendor's door-phone preview where one exists: Yealink door phone or IP camera settings on camera-capable models, Grandstream GDS settings P32000 to P32004 on WP822 [R822 §8, RN], Fanvil and Snom door-phone features. Everything here is **verify**: no lab hardware has been confirmed, and nothing else in the doorbell phases depends on it.

**D. Remaining protocol, provisioning and device gaps**

- [S-10] **Multiple accounts per phone.** A phone gets up to its model's line count of lines, each bound to a device (line 1 is today's binding). The templates in `internal/prov/builtin.go` write lines 2 and 3 with the vendor's account keys: Grandstream account 2 P401 and later, account 3 P501 and later [R8x6 TPL]; WP822 account 2 keys (P60155, P2425 and others) [R822 RN]; other vendors from the provisioning spec's account blocks, **verify**. MWI, BLF, ring tags and check-sync apply per line.
- [S-11] **Account sharing.** WP8x6 host and guest settings P22521 and P22522 [R8x6 AS] are provisioned from a phone-group setting so several handsets share one line. It is documented as the alternative to binding several devices to an extension.
- [S-12] **Per-phone and per-group setting overrides.** An admin may set extra vendor keys per phone group and per phone (key, value, secret flag). The resolution order is template, then group, then phone. Secret values are sealed and shown masked. Keys are validated against a deny list of keys Hello owns (account, token URL, CA, admin password). This closes the "per-phone extra settings" row and carries the niche one-off keys: speed dials P23001/P23003 [R822 RN], hotline/off-hook auto dial P71/P8388 [R822 RN], wallpaper P2916/P2917 [R822 RN], VoWLAN target delay P82281 [R822 RN], power save P82269 [R822 RN], band selection P25438 [R8x6 TPL] and side key P82317 [R8x6 TPL]. First-class UI fields exist for the side key (PTT, paging or alarm) and for speed dials, which replace BLF on WP models (Hello "BLF" entries on a WP phone are offered as speed dials).
- [S-13] **SIP over TCP.** hello-sip listens on TCP 5060 (configurable) alongside UDP. Via, Contact, Record-Route and the edge proxy keep the received transport, and responses and in-dialog requests reuse the connection, with an idle timeout of 1 h and keep-alive per S-9. A template setting picks the transport per model (Grandstream P130 = 1 for TCP [R8x6 TPL]).
- [S-14] **SIP over TLS (`sips`).** A TLS listener on 5061 uses a certificate issued by the Hello CA (cert-manager on kw) with TLS 1.2 minimum and 1.3 preferred. The `sips:` refusal in `internal/sip/b2bua.go` is replaced by real handling: a `sips` request routes only over TLS, and is refused with `416`/`480` when no TLS path exists. Templates: Grandstream P130 = 2, P2329 sips scheme on, TLS version keys P22293/P22294 (WP822 [R822 RN]); other vendors' transport keys from the provisioning spec, **verify**.
- [S-15] **SRTP (SDES).** For calls between endpoints that both offer `a=crypto`, direct media passes the crypto attributes through unchanged. For anchored calls the anchor terminates SRTP per leg (AES_CM_128_HMAC_SHA1_80 required; AES_256_CM_HMAC_SHA1_80 optional) with keys generated per leg and never logged. SRTP is offered only on TLS legs. Grandstream SRTP mode P183 is "enabled but not forced" by default [R8x6 §2].
- [S-16] **DTLS-SRTP and ICE.** For WP8x6 firmware 1.0.3.20 and later, DTLS key exchange P60078 and media ICE P95002 [R8x6 §2, ICE] are provisioned **off** by default, because SDES is used. Hello implements:
  - ICE-lite on the anchor: it answers STUN connectivity checks with its anchor candidates and accepts a=candidate from offers, so phones with ICE enabled interoperate;
  - DTLS-SRTP termination on the anchor, for endpoints that offer only DTLS.
    Both are enabled per model through the capability table once the lab passes (**verify**). STUN/NAT keep-alive P52 stays Auto [R8x6 §2].
- [S-17] **Reliable provisional responses and Replaces.**
  - Hello advertises `100rel` in `Supported` and handles PRACK end to end on bridged calls. `Require: 100rel` from a phone is honoured rather than refused, and the allow list in `internal/sip/server.go` includes PRACK. Phones keep 100rel off (Grandstream P272 = 0 [R8x6 §2]).
  - `INVITE` with `Replaces` (RFC 3891) is supported for early dialogs as well as confirmed ones. This covers attended transfer before answer [R822 §3], and directed pickup by Replaces from phones that send it on a BLF key.
  - Static and dynamic attended transfer and "Refer-To use target contact" are lab-verified on WP822 and WP836 [R8x6 §3].
- [S-18] **Call waiting and busy handling.** An extension gains a call-waiting flag (default on). With it on, a call to a busy extension rings its devices (the phone shows call waiting); with it off, the caller gets busy or the extension's forward-busy target. The lab verifies the WP822 and WP836 call-waiting tone [R822 §3].
- [S-19] **Anonymous outbound.** On trunk legs Hello honours `Privacy: id` and an anonymous From from a device (the WP822 "Send Anonymous" setting [R822 §3]). It sends the trunk's configured P-Asserted-Identity with `Privacy: id` and an anonymous From, and never strips the privacy request. This is test-covered.
- [S-20] **DNS SRV and backup servers for HA.** The kw deployment publishes the SIP-over-UDP, SIP-over-TCP and SIPS-over-TCP SRV records for the SIP service name, as a documented DNS record set. The templates in `internal/prov/builtin.go` can set DNS mode SRV, the WP822 failback timer P60056 and "Register before DNS SRV failover" P29095 [R822 RN]. Where SRV is not used, they can provision backup outbound proxies from the HA node list. The default stays A-record mode until the failover lab test (a node killed under a Wi-Fi handset) passes.
- [S-21] **QoS marking.** hello-sip marks SIP sockets DSCP 26 (CS3/AF31) and the anchor marks relay RTP sockets DSCP 46 (EF), both configurable (`HELLO_DSCP_SIP`, `HELLO_DSCP_RTP`). The Grandstream template writes P1558 = 26 and P1559 = 46 [R8x6 TPL], with site overrides through S-12.
- [S-22] **Provisioning hardening.** All Grandstream WP models get these values:
  - TR-069 off: P1409 = 0, ACS URL P4503 empty [R8x6 TPL; R822 TR069];
  - SSH disabled, user web access P8469 restricted [R822 RN], keypad configuration menu set to basic, Direct IP calls off [R822 SEC];
  - "Accept Incoming SIP from Proxy Only" on, and incoming INVITEs authenticated: Hello answers the phone's `401` on INVITEs it originates with the device credentials (as for S-5) [R822 §2 SEC];
  - Trusted Domain Name list P60082 set to the SIP domain [R822 RN];
  - "Allow SIP Reset" off;
  - firmware upgrade confirmation bypass P84988 = 1 [R822 RN];
  - config-file authentication: P1 written from the per-phone admin password so the phone can enable "Authenticate Config File" [R822 SEC];
  - XML config file password (AES-encrypted config, P1359-style [R8x6 AG]), served encrypted when the phone has a config password set (**verify** the cipher format before it is turned on; off by default).
    The other vendors' equivalents (TR-069 off, SSH off) are written where the provisioning spec's vendor blocks name them, **verify**.
- [S-23] **Wi-Fi profiles.** A phone group may carry up to 10 Wi-Fi profiles: SSID, security (WPA2-PSK, WPA3-SAE, WPA2/3 Enterprise PEAP or TLS, OWE where the firmware has it), sealed PSK or EAP identity/password, priority, preferred account, and a "verified on a test phone" flag (default 14). They are written to WP8x6 P83000, P83002 and P83003 plus the priority and preferred-account keys [R8x6 TPL], and to the WP822 SSID keys (**verify**, 20 profiles max [R822 §10]). The OEM override P84991 that preserves Wi-Fi is respected [R822 RN].
- [S-24] **Roaming and radio settings.** Per phone group:
  - 802.11r on/off, 802.11k on/off, 802.11v P84992 [R822 RN];
  - roaming threshold P22232 (default -55 [R822 RN]) and poor-signal scan P22231 [R822 RN];
  - WP8x6 roaming mode P82332 (Default/Boost) and roaming prompt P82334 [R8x6 TPL];
  - band selection P25438 [R8x6 TPL].
    `docs/provisioning.md` gets a "Wi-Fi handsets" section with the AP requirements: 802.11r/k/v, one SSID and VLAN so the IP and registration survive a roam, WMM, and DSCP trust [R8x6 §10].
- [S-25] **802.1X, enterprise CA and SCEP.** Enterprise Wi-Fi profiles (S-23) reference a CA certificate type (WP8x6 1.0.3.25 and later [R8x6 RN]). Hello can act as a SCEP server (RFC 8894) that issues device certificates from the Hello CA to phones that request them (WP8x6 1.0.3.20 and later [R8x6 RN]), with a per-phone one-time challenge password delivered in the phone's config. SCEP is off by default and enabled per phone group.
- [S-26] **Firmware management.** The per-model pins already in `prov_firmware_pins` gain these:
  - the WP8x6 and WP822 defaults of default 13, as documented recommended pins (admins upload the files; Hello ships no vendor firmware);
  - a per-vendor and model-glob **minimum version**: the UI refuses to pin, and the provisioning refuses to render, a firmware below it (WP822 floor 1.0.11.22 [R822 RN]);
  - the firmware version seen per phone (the existing firmware_seen column, from the User-Agent) shown on Phones and PhoneDetail, with a "drift" badge when it differs from the pin;
  - the firmware upgrade window and P238 "Firmware Upgrade and Provisioning" [R8x6 §9], set from the pin.
- [S-27] **Syslog sink.** hello-control (or a small sidecar in the same pod) receives syslog over UDP 514 and TLS 6514. It keeps the last 10,000 lines per phone in Valkey for 24 h and shows them on PhoneDetail. The template sets syslog server P207 and level P208 [R8x6 TPL] per phone group, off by default. Lines pass the existing redaction before storage.
- [S-28] **Emergency calling (E911/HELD).**
  - (a) An emergency-flagged route (`internal/routing/decide.go`) bypasses DND, forwarding, call restrictions and concurrency limits, and is never blocked by a door extension's target list.
  - (b) Template P8385 lists the emergency route numbers [R8x6 §3].
  - (c) Locations: a civic address and optional geo point per phone group, with an optional override per extension.
  - (d) A minimal HELD server (RFC 5985 locationRequest, PIDF-LO response with the location of the requesting phone, identified by its provisioning token in the URL) is served on the provisioning listener. WP8x6 E911 P8565 and the HELD keys P8566 to P8597 [R8x6 TPL] point at it (firmware 1.0.3.20 and later).
  - (e) On emergency routes of trunks flagged geolocation, Hello forwards a phone's Geolocation header and PIDF-LO body, or adds them from (c) (RFC 6442), and adds P-Emergency-Info where WP8x6 P26059 is set [R8x6 TPL].
  - (f) A callback to the extension within 30 min uses ring tag emergency-callback and bypasses DND.
  - (g) An optional "emergency notify" ring group is called, one-way with an announcement naming the extension, on every emergency call.
- [S-29] **Alarm calls.** A phone group may set an alarm destination: an extension or ring group, default unset. The WP8x6 template writes Emergency Call P22269 = 1, number P22270 = `*55`, call way P22601 and auto answer P22602 [R8x6 AG "Alarm"] (firmware 1.0.3.20 and later). Hello maps `*55` from a phone to the caller's group alarm destination, as a call with kind alarm that ignores DND, carries ring tag alarm, and plays a "panic alarm from <extension name>" announcement to the answering party. The side key can be set to alarm (S-12). Without a destination `*55` is answered `404`, and the template omits P22270.
- [S-30] **Grandstream PTT channels and group paging** (addition to phonebooks-ptt phase F). WP8x6 phone groups get PTT settings beyond phonebooks-ptt P-15's multicast listen/send:
  - PTT enable P22177, PTT/group paging address P22183 (default 224.0.1.116), default, priority and emergency channels, accept while busy P22179, codec P22180, ptime P22178;
  - group paging and emergency group P22194;
  - multicast paging P22208, P1566 to P1569 (up to 10 listen addresses), P22532 timeout [R8x6 TPL].
    For the WP822, which has a dedicated PTT button and channel menu rather than DSS keys [R822 §8], the phase F vendor table's Grandstream "DSS key speed dial" PTT row is replaced by channel provisioning. Phonebooks-ptt P-15's validation (address range, at most 10 channels) applies.
- [S-31] **Phonebook caps per model** (addition to phonebooks-ptt phase B). The Grandstream renderer truncates to the model cap from S-2: 500 for WP822 and 1000 for WP8x6 [R822 §7][R8x6 §7]. It keeps books in the delivered-set order, and the phone page warns when entries were dropped. P330 = 3, P331 in host/path form without a scheme, and P332 between 5 and 720 [R8x6 §7] confirm phonebooks-ptt's Grandstream "verify" cells. "Remove Manually-edited Entries on Download" is set on and the Import Group Method is set explicitly [R8x6 §7][R822 §7]. A vCard rendering of the delivered set is offered for WP8x6 1.0.3.24 and later [R8x6 RN] as another slot format, unused by default.
- [S-32] **Auto-answer restriction** (addition to phonebooks-ptt phase E). WP models get "Accept Incoming SIP from Proxy Only" (S-22), which fills phonebooks-ptt's "Accept only from server" cell for Grandstream [R822 §8]. The exact auto-answer header form the WP822 and WP836 honour is recorded in that spec's vendor table when phase E's lab test runs.
- [S-33] **Music on hold.** The anchor plays music on hold to a held or parked party when the holding device sends `sendonly`/`inactive`, or the call is parked. It plays an admin-chosen announcement in a loop, or a built-in tone if none is chosen. The Grandstream MOH URI P2350 [R8x6 §3] is left empty so the phone does not originate its own MOH call.
- [S-34] **Server conference.** Reversed from out of scope on 2026-10-25 (`.procoder/ask/decisions.md`). Rooms are audio only and are mixed on one SIP node (G.711, G.722 and Opus, each converted to a common 8 kHz mixing rate). Behaviour, controls, the HA design and its limits are in `docs/conference.md`. A room has:
  - a number (2 to 10 digits), an optional user PIN and an optional leader PIN (sealed, write-only), and a seat cap of 2 to 50 (default 10; the next caller is refused `486`);
  - wait-for-leader with a hold source (a soft beep, with music on hold plugging in through the `ConferenceHold` dependency), join and leave tones or announcements, mute on entry, and optional recording to the recordings store in ten-minute segments;
  - DTMF controls `*6` (mute or unmute yourself), and for leaders `*5` (mute everyone else), `*7` (lock) and `*8` (remove the last joiner);
  - a place as a routing destination (`destinationKind` `conference` on an inbound route, a node on the flow canvas), and as a phone key (a room number among a phone's BLF keys joins it and lights while the room has people in it);
  - a live console view of participants with mute, kick and lock, over `POST /api/v1/conferences/{id}/control`;
  - one owning node per room by a Valkey lease; joins on another node are forwarded to the owner; a node loss moves the room through the in-call takeover.
    A three-way merge from phones that use a conference URI (the BroadSoft N-way URI P2318 [R8x6 TPL], **verify**) works by setting P2318 to a room number. Local 3-way and 4-way conferencing on the phone keeps working unchanged.
- [S-35] **Presence extensions.**
  - Unknown events get `489` (S-1).
  - `Event: reg` (RFC 3680) is served for an extension's own registrations.
  - `Event: presence` PUBLISH (RFC 3903) is accepted and stored per extension (basic open/closed plus note). Presence SUBSCRIBE returns it with the dialog state folded in, and PUBLISH is added to the allow list.
  - RFC 4662 resource lists (`eventlist`) are supported for dialog subscriptions to a list URI the template names for vendors that use it.
    The WP templates rendered by `internal/prov/builtin.go` keep P188 (PUBLISH) and "SUBSCRIBE for Registration" off, because the WP phones gain nothing from them [R8x6 §4].
- [S-36] **RTCP-XR collector.** Hello accepts `PUBLISH Event: vq-rtcpxr` (RFC 6035) and stores the per-call MOS, jitter and loss with the CDR. It shows them next to Hello's own anchor quality figures. The template sets the WP822 VQ collector P26093 to P26095 [R822 RN] (off by default).
- [S-37] **Small provisioning items.**
  - The WP822 lab run confirms that the config-file name sequence (cfgwp822.xml, then cfg.xml, with the dev-MAC file answered 404) leaves the phone provisioned [R822 §1]. A model-wide cfg-model file stays unserved.
  - The Grandstream account ringtone P104/P423 [R822 RN] is driven by the ring map (S-44).
  - The unverified jitter-buffer and anonymous-call items are covered by S-12 and S-19.
- [S-38] **Lab and emulation.** `test/provclient` gains WP822 and WP836 profiles: their file-name sequence, User-Agent, P-value parser, check-sync responder with digest challenge, message-summary subscriber, as-feature-event subscriber, and a MESSAGE client. `test/integration` gains a WP scenario that covers MWI, check-sync, park, pickup, DND sync, MESSAGE, ring tags and a door call. The hardware lab list below is run on the two WP836 and the WP822 before each phase that depends on it closes.

## Out of scope

- LDAP and LDAPS directories (decision of 2026-10-25, unchanged).
- Hot desking (neither WP family documents it [R822 §3]); account sharing (S-11) covers the shared-handset case.
- Provisioning door stations themselves (Grandstream GDS, 2N, Akuvox, DoorBird are configured on the device); Hello stores their SIP credentials and door-open code only.
- Door-open over a station's HTTP API (relay URL), and access-control integration; only DTMF is used.
- A TR-069 ACS (S-22 turns TR-069 off instead).
- GDMS beyond what the provisioning spec already does [R822 §9 "none beyond spec"].
- Transcoding between trunk-side and phone-side codecs beyond what the decoding endpoints (S-4) need.
- Video in conferences, voicemail or recording; video beyond S-49 pass-through.
- Phone-side features that need nothing from the PBX: noise reduction, Bluetooth, Wi-Fi analyser, USB-C PTT headset, SIP log upload, VPN, local call history [R8x6 rows marked n/a].

## Constraints

- No PostgreSQL call on the SIP path. Tags, rules, ring map, park lot configuration, pickup groups, door targets and alarm destinations ride the existing configuration snapshot, while live park slots, presence and MESSAGE queues live in Valkey with PostgreSQL as the durable source where noted.
- HA: every new piece of runtime state (park slots, subscriptions, queued messages, check-sync pushes) works from any hello-sip replica, and the in-call handoff covers parked and conference calls on a best-effort basis, as it does for pages.
- Security:
  - door-open codes, webhook secrets, Wi-Fi secrets, SCEP challenges and SRTP keys are sealed at rest, never logged, never in metric labels, and never returned after save;
  - the webhook route is the only new unauthenticated route, and it is signature-checked and rate-limited;
  - auto-answer and Alert-Info injection stay closed (phonebooks-ptt P-16).
- Compatibility: every vendor and model keeps provisioning exactly as before until its phase changes its template. A phone with no group, no lines beyond 1 and no overrides renders the same as today, apart from the WP template keys of S-3 and the ring map defaults of S-44.
- Performance:
  - ring tag choice adds under 5 µs to call setup;
  - a check-sync push to 500 phones finishes within the configured push rate;
  - TLS handshakes are not on the call path for an established connection;
  - the anchor's SRTP adds under 50 µs per packet p99 (benchmark).
- Dependencies: at most one new Go module for Opus (S-4) and one for DTLS (S-16, e.g. pion/dtls), each named in the plan and reviewed; everything else uses stdlib and existing modules.
- Deployment: kw via Kuvryn Sync; TLS port 5061 and syslog 514/6514 are exposed through the existing Service definitions; no workloads on the user's Mac; CI on the Arc runners.
- Gate per PR: gofmt, `go vet`, golangci-lint (0), `go test -race ./...` green, `procoder check` 0 blocking, web lint and tests, mutation checks; REVIEW.md applies.

## Interfaces

- **SIP (hello-sip):**
  - new SUBSCRIBE events `message-summary`, `as-feature-event`, `reg`, `presence`, and eventlist;
  - new methods handled: PUBLISH, PRACK, and MESSAGE delivery;
  - outgoing NOTIFY `check-sync` and `message-summary`;
  - TCP 5060, TLS 5061;
  - SDES and DTLS-SRTP, ICE-lite;
  - Session-Expires, `Replaces` on early dialogs;
  - Alert-Info on legs to devices (S-41).
- **Feature codes:** new actions pickup_group, pickup_directed, park, park_retrieve, alarm and door_open, with the defaults in default 2, plus conference rooms (S-34).
- **Provisioning (phone-facing):** `GET /p/<token>/ring/<name>.wav` (S-43); the HELD endpoint `POST /p/<token>/held` (S-28); the SCEP endpoint `/p/<token>/scep` (S-25); the vCard slot (S-31).
- **REST** (each route in `internal/api/routes.go` and `internal/api/openapi.json`; the conformance test checks they agree):
  - `POST /api/v1/phones/{id}/resync`, `POST /api/v1/phones/{id}/reboot`, `POST /api/v1/phones/actions` (bulk);
  - `GET|PUT /api/v1/phones/{id}/lines`; `GET|PUT /api/v1/phones/{id}/settings`, `GET|PUT /api/v1/phone-groups/{id}/settings`, `GET|PUT /api/v1/phone-groups/{id}/wifi`, `GET|PUT /api/v1/phone-groups/{id}/radio`, `PUT /api/v1/phone-groups/{id}/alarm`, `PUT /api/v1/phone-groups/{id}/location`;
  - `GET /api/v1/models` (capability table, read-only);
  - `GET|POST /api/v1/ring-tags`, `PUT|DELETE /api/v1/ring-tags/{name}`, `GET|PUT /api/v1/ring-rules` (ordered), `GET|PUT /api/v1/ring-map`, `GET|PUT /api/v1/phone-groups/{id}/ring-map`, `GET|POST /api/v1/ringtones`, `DELETE /api/v1/ringtones/{id}`;
  - `GET|POST /api/v1/park-lots`, `PUT|DELETE /api/v1/park-lots/{id}`, `GET /api/v1/park-lots/{id}/slots` (live); `GET|POST /api/v1/pickup-groups`, `PUT|DELETE /api/v1/pickup-groups/{id}`;
  - extension create and update gain the kind, door-open code (write-only), door targets, hang-up-after-open and call-waiting fields; ring groups gain the tag and door fields;
  - `GET|POST /api/v1/door/webhooks`, `PUT|DELETE /api/v1/door/webhooks/{id}`, `POST /api/v1/door/webhooks/{id}/rotate`, `POST /api/v1/door/webhooks/{id}/trigger` (HMAC, no session);
  - `GET|POST /api/v1/locations`, `PUT|DELETE /api/v1/locations/{id}`;
  - `GET|POST /api/v1/conferences`, `PUT|DELETE /api/v1/conferences/{id}`;
  - `GET /api/v1/me/messages`, `POST /api/v1/me/messages` (web-originated MESSAGE to an extension);
  - `GET /api/v1/phones/{id}/syslog`;
  - `POST /api/v1/calls/{id}/door-open` (the answering user's own live door call only).
- **RBAC:** viewers read; operators manage ring rules, the ring map, the tone library, park and pickup groups, and trigger resync; admins manage everything else including webhooks, door codes, Wi-Fi, SCEP, locations and reboot. A user may use door-open only on a call they answered.
- **UI:**
  - **PhoneDetail:** lines, overrides, resync/reboot, firmware drift, syslog, capability-driven BLF/speed dials;
  - **Phones:** bulk actions;
  - **Ringing** (`/ringing`): tags, rules, map, library;
  - **Door** (`/door`): stations, webhooks;
  - **Phone groups** (from phonebooks-ptt): Wi-Fi, radio, alarm, location, PTT channels and side key tabs;
  - **Park & pickup** (`/park`);
  - **Messages** (`/me/messages`);
  - **Conferences** (`/conferences`);
  - **Emergency locations** (`/locations`).
    Every new page has a Vitest test.
- **Config:** `HELLO_PROV_PUSH_RATE`, `HELLO_MEDIA_DEAD_SECONDS`, `HELLO_DOOR_HANGUP_SECONDS`, `HELLO_SIP_TCP`, `HELLO_SIP_TLS`, `HELLO_SIP_TLS_CERT`, `HELLO_DSCP_SIP`, `HELLO_DSCP_RTP`, `HELLO_SYSLOG`, `HELLO_SCEP`.
- **Metrics:** hello_mwi_notify_total{kind,result}, hello_checksync_total{vendor,result}, hello_park_total{result}, hello_pickup_total{kind,result}, hello_feature_sync_total{feature,result}, hello_sip_message_total{result}, hello_ring_tag_total{tag}, hello_door_webhook_total{result}, hello_door_open_total{result}, hello_sip_tls_handshake_total{result}, hello_srtp_legs, hello_held_requests_total{result}, hello_alarm_calls_total{result}, hello_syslog_lines_total.

## Data

Migrations follow phonebooks-ptt's `00011` and take the next free number at merge time, one per phase that needs one. All are additive, so a rolling upgrade works.

- **extensions:** kind (user/door), door_open_enc (sealed), door_hangup_after_open, door_targets (jsonb list of extension and ring group ids), call_waiting (default true), location_id.
- **ring_groups:** ring_tag (nullable), is_door.
- **feature_codes:** the action check widened with the actions of Interfaces; the code check widened for prefix codes with appended digits, as phonebooks-ptt does for its intercom code.
- **ring_tags** (name primary key, builtin, bellcore_slot 1 to 5, description); **ring_rules** (id, position, match jsonb, tag); **ring_map** (tag, vendor, phone_group_id nullable, tone kind builtin/custom/silent, value); **ringtones** (id, name, object_key, sha256, sample_rate, duration_ms).
- **park_lots** (id, name, first_slot, slot_count, return_seconds, then_voicemail); live slots in Valkey under the hello:park prefix. **pickup_groups** and **pickup_group_members**.
- **phone_lines** (phone_id, line_no 1 to 3, device_id unique). The existing phones.device_id stays line 1 and is migrated to a phone_lines row while the column is kept for one release.
- **prov_settings** (scope phone or group, scope id, key, value or value_enc, secret flag).
- **wifi_profiles** (group_id, position, ssid, security, secret_enc, priority, account, verified); **phone_group_radio** (group_id, 11r, 11k, 11v, threshold, mode, band).
- **locations** (id, name, civic jsonb, geo point, emergency notify group); **phone_groups** gain location_id, alarm_target and syslog settings.
- **prov_firmware_min** (vendor, model_glob, min_version).
- **sip_messages** (id, from_ext, to_ext, body_enc, created_at, delivered_at; purge after 30 days; undelivered after 24 h marked expired).
- **door_webhooks** (id, name, secret_enc, action, target_id, announcement_id, enabled, last_trigger_at).
- **presence_publications** in Valkey (TTL = PUBLISH expiry). **rtcpxr** columns on cdrs (mos, jitter, loss). cdrs gain ring_tag and kind values door, alarm, park and conference (extending phonebooks-ptt's kind).
- **conferences** (id, number, pin_enc, max_participants).
- **MinIO:** the hello-ringtones bucket.

## Vendor reference: distinctive ringing

Sources:

- Yealink: "Yealink SIP IP Phones Administrator Guide", sections Distinctive Ring Tones and Ring Tones. Keys distinctive_ring_tones.alert_info.X.text and distinctive_ring_tones.alert_info.X.ringer (X 1 to 10), ringtone.url for custom tones, account.X.feature_key_sync.
- Poly: "Poly UC Software Administrator Guide", sections Ringtones and Distinctive Ringing, Alert-Info. Keys voIpProt.SIP.alertInfo.x.value and voIpProt.SIP.alertInfo.x.class, se.rt.<class>.ringer, saf.x sampled audio files for custom tones, voIpProt.SIP.serverFeatureControl.dnd and .cf.
- Grandstream: [R822] AG and RN for "Match Incoming Caller ID" rules from P1488 and account ringtone P104/P423; [R8x6] AG, RN and TPL for the Bellcore-dr1 to dr5 mapping P95026 (firmware 1.0.1.85 and later) and the custom Alert-Info text P2356.
- Snom: snom service wiki settings pages (https://service.snom.com/display/wiki/alert_internal_ring_text and the sibling pages for alert_external_ring_text, alert_group_ring_text, alert_internal_ring_sound, alert_external_ring_sound, alert_group_ring_sound, custom_melody_url).
- Fanvil: "Fanvil IP Phone Administrator Guide" and "Fanvil Auto Provisioning Guide", sections Ring Type and Alert-Info ring.

All key names come from these documents. Every value and every per-model behaviour is **verify** until the lab run of the ringing phase (Yealink and Grandstream on hardware first; Poly, Snom and Fanvil on hardware or the vendor's simulator).

|                           | Yealink                                                               | Poly (VVX)                                                   | Grandstream GRP/GXP                                                    | Grandstream WP822                                                                      | Grandstream WP8x6                            | Snom                                                                                                                | Fanvil                                                |
| ------------------------- | --------------------------------------------------------------------- | ------------------------------------------------------------ | ---------------------------------------------------------------------- | -------------------------------------------------------------------------------------- | -------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------- |
| Header Hello sends (S-41) | canonical                                                             | canonical; matched on the URL (verify)                       | canonical                                                              | canonical                                                                              | canonical; Bellcore-drN used                 | alert-internal, alert-external, alert-group as info for the three built-in classes, canonical for the rest (verify) | canonical                                             |
| Match rule key            | distinctive_ring_tones.alert_info.X.text = hello-<tag>                | voIpProt.SIP.alertInfo.x.value = the URL, .class = custom<n> | Match Incoming Caller ID rule with Alert-Info text (verify key on GRP) | P1488 and following, 10 rules, "Alert-Info" match type [R822 RN] (verify field layout) | P95026 Bellcore-dr1..5 to ringtone [R8x6 RN] | alert_*_ring_text (3 classes)                                                                                       | Alert-Info ring rules: text to ring type (verify key) |
| Tone key                  | distinctive_ring_tones.alert_info.X.ringer = 1..8 or custom file name | se.rt.custom<n>.ringer = ringerN                             | rule's ringtone field (verify)                                         | rule's ringtone field (verify)                                                         | P95026 target ringtone (verify values)       | alert_*_ring_sound = Ringer1..Ringer10 or Custom                                                                    | ring type 1..9 or custom (verify)                     |
| Number of tag slots       | 10                                                                    | 24 classes (verify)                                          | verify                                                                 | 10                                                                                     | 5 (Bellcore)                                 | 3 named plus Bellcore 1..5 (verify)                                                                                 | verify                                                |
| Custom tone delivery      | ringtone.url = `/p/<token>/ring/<name>.wav`                           | saf.x = same URL                                             | not produced (Grandstream format, verify)                              | not produced (verify)                                                                  | not produced (verify)                        | custom_melody_url = same URL                                                                                        | custom ring URL key (verify)                          |
| Default line tone         | account.1.ringtone.ring_type                                          | se.rt.ringer default class                                   | account ringtone (verify)                                              | P104 [R822 RN]                                                                         | account ringtone (verify)                    | ring_sound                                                                                                          | line ring type (verify)                               |

## Vendor reference: door key, MWI, check-sync, feature sync

|               | Yealink                              | Poly                                      | Grandstream WP822                                 | Grandstream WP8x6                   | Snom                              | Fanvil                            |
| ------------- | ------------------------------------ | ----------------------------------------- | ------------------------------------------------- | ----------------------------------- | --------------------------------- | --------------------------------- |
| MWI subscribe | account.N.subscribe_mwi = 1 (verify) | msg.mwi.N.subscribe (verify)              | MWI subscribe flag (key verify) [R822 AG]         | P99 = 1 [R8x6 TPL]                  | subscribe MWI setting (verify)    | MWI subscribe setting (verify)    |
| check-sync    | check-sync (reboot form verify)      | check-sync (verify)                       | event name verify (check-sync expected) [R822 §9] | check-sync, P95030, P4428 [R8x6 §1] | check-sync (verify)               | check-sync or resync (verify)     |
| Feature sync  | account.N.feature_key_sync           | voIpProt.SIP.serverFeatureControl.dnd/.cf | P2325 [R822 RN]                                   | P2325 [R8x6 AG]                     | not known: local DND off (verify) | not known: local DND off (verify) |
| Door-open key | DSS key DTMF `*50` (verify type)     | soft key `*50` (verify)                   | softkey (verify)                                  | DialDTMF softkey [R8x6 AG]          | function key DTMF (verify)        | DSS key DTMF (verify)             |
| Park codes    | n/a (phone uses REFER)               | n/a                                       | not in template [R8x6 comparison]                 | P60069, P2336 [R8x6 §3]             | n/a                               | n/a                               |

## Edge cases

- A phone subscribes to message-summary and also receives an unsolicited NOTIFY: unsolicited NOTIFYs go only to contacts without a subscription, so it never gets both.
- A mailbox changes while a subscription is being refreshed: the NOTIFY carries the current counts, because each NOTIFY reads the state, not a delta.
- check-sync to a phone registered from two contacts: both get it, and the result is the best of the two.
- check-sync to a phone not registered: the result is "not registered" and the change waits for the periodic resync.
- check-sync to a phone that challenges with a realm Hello does not know: the result is "auth failed" and an alert is raised.
- Park to an occupied slot: the request is answered `486` and the parker stays connected. Park when all slots are full: the call is not parked and a busy tone plays.
- Two users retrieve the same slot at once: a Valkey compare-and-delete lets one win, and the other gets "slot empty".
- The parker has hung up when the park times out: the call returns to the parker's extension (ringing all its devices), then goes to voicemail.
- Pickup when nothing is ringing in scope: a short error tone plays.
- Directed pickup of a call already answered: a short error tone plays. Two simultaneous pickups: the first wins.
- A phone sets DND by feature sync while `*78` is dialled elsewhere: last writer wins, and both phones get a NOTIFY with the final state.
- MESSAGE to self: delivered to the other contacts. MESSAGE to a door extension: `403`. A message larger than 1,300 bytes: `413`.
- A ring rule matches a call that is also a door call: the call kind wins (S-40 order).
- A custom tag is deleted while the ring map or ring groups use it: refused with `409` until it is unassigned.
- A Bellcore slot is shared by two tags on a WP8x6: both ring the same tone on that vendor, and the UI warns.
- A door station calls a target not in its list: `403`. A door station's call is forwarded by the callee's forward-always: allowed internally, never to a trunk.
- `*50` in a call that is not with a door extension: ignored, and the digits pass through as normal DTMF.
- The door-open code contains `#`: it is sent as an RFC 2833 event 11.
- A webhook replays an old signature: refused by the timestamp window. A webhook trigger arrives while the Door group is already ringing for the same webhook: it is coalesced and not rung twice.
- A WP822 asked for a third line: refused by the capability table with `400`.
- A WP phone with BLF entries stored before S-2: they are rendered as speed dials and the UI says so.
- TLS to a phone with an expired Hello CA bundle: the phone stays on its previous transport until re-provisioned over HTTPS, which uses the same CA push.
- SRTP offered on a UDP leg: the crypto lines are stripped and plain RTP is answered.
- An Opus-only offer to voicemail before the Opus decoder ships: answered `488`. This cannot happen with the provisioned codec order.
- Emergency callback after the 30-minute window: it rings normally.
- An emergency call from a phone with no location: routed anyway, with no Geolocation added, and the UI flags the phone group.
- A Wi-Fi profile push with no verified profile in the group: refused with `409`.
- A firmware pin below the model minimum: refused with `400`. A phone already running older firmware is shown as drift.
- Syslog from an unknown IP: dropped and counted.

## Failure modes

- **Valkey unavailable:**
  - parking is refused (busy tone) and active parked calls stay on their anchor until retrieved by slot through PostgreSQL-free node memory, or until they time out back to the parker;
  - presence publications and syslog are lost (non-durable by design);
  - queued MESSAGE delivery falls back to PostgreSQL.
- **PostgreSQL unavailable:** calls, ring tags and MWI from the snapshot keep working. API writes fail, the webhook gets `503`, MESSAGE storage gets `503`, and delivery to online contacts still works.
- **A phone ignores check-sync:** the result is a timeout, and the change lands at the periodic resync. The phone page shows "push not acknowledged".
- **A vendor key is wrong for a model (found in the lab):** the template and the vendor table are corrected. Until then the feature is off for that model through the capability table, so nothing half-works silently.
- **TLS certificate renewal fails:** the old certificate is served until expiry, and an alert fires 14 days before expiry. UDP and TCP keep working.
- **The SRTP or DTLS handshake fails on a leg:** the call fails with `488` to the offerer when SRTP was required, and otherwise falls back to RTP.
- **The anchor node dies with parked or conference calls:** the in-call handoff applies (best effort). Parked calls whose slot is lost return to the parker.
- **A door station is unregistered:** the Door group still rings for webhook triggers, and the door extension shows offline in the UI.
- **Webhook flood:** the rate limit answers `429`, and the per-IP limiter of the API applies first.
- **HELD server unreachable from the phone:** the phone sends no location, and Hello adds the stored one on flagged trunks.
- **Syslog flood from one phone:** a per-phone cap of 100 lines per second applies, and the rest are dropped and counted.

## Lab verification list

These items are checked on hardware (two WP836 on 1.0.1.44 and 1.0.1.80, upgraded to the 1.0.3.x pin, and the WP822 on 1.0.11.78). Each phase lists the ones it closes:

1. message-summary SUBSCRIBE and NOTIFY light the LED. Record what today's MESSAGE push does, for the record, before S-1 removes it.
2. check-sync event name, authentication, P95030 behaviour and the reboot form.
3. The P323 to P325 BLF keys have no effect on WP822 and WP836. Speed dial P23001/P23003 on WP822.
4. Codec order keys P57 to P64 on WP822 (from its XML export). A G.722 call through the anchor, to voicemail, and recorded.
5. Park softkey REFER target with P60069 and P2336 on WP836.
6. Feature sync P2325 on both families. P8446 local DND off on WP822.
7. MESSAGE in the WP8x6 IM app with P22650 set. MESSAGE on WP822.
8. Mid-call roam on the same subnet and across subnets. 1 h idle behind NAT. OPTIONS keep-alive P2397.
9. Alert-Info: P1488 rules on WP822, P95026 Bellcore on WP836, and whether a second Alert-Info value is accepted.
10. TLS P130 = 2 and sips, SRTP P183, DTLS P60078 and ICE P95002 on 1.0.3.x.
11. Account 2 and 3 keys, account sharing P22521/P22522.
12. Hardening: proxy-only plus authenticated INVITE with Hello answering the challenge, TR-069 off, P84988.
13. E911 P8565 plus HELD to Hello, alarm P22269 to P22602, side key P82317.
14. PTT channels P22177 to P22194, multicast P22208 and P1566 to P1569.
15. Wi-Fi P83000 to P83003 with a verified profile, roaming P82332/P22232, band P25438.
16. Door: a GDS37xx (or another station) registering as a door extension, `*50` opening it, and video preview on a camera-capable phone.

## Acceptance criteria

- [x] [S-1] `TestMessageSummarySubscribe` in `internal/sip` passes. It fails if:
  - a message-summary SUBSCRIBE from the mailbox's own device is not answered 200 with an immediate NOTIFY carrying the current counts;
  - a change does not NOTIFY every subscription;
  - another extension's device can subscribe;
  - an unknown event is answered other than 489;
  - any MWI is still sent as MESSAGE;
  - an unsubscribed contact does not receive an unsolicited NOTIFY.
- [ ] [S-2] `TestModelCapabilities` in `internal/prov` passes. It fails if:
  - a WP8* model accepts or renders BLF entries;
  - a line number above the model's count is accepted;
  - P193 below 60 is rendered for Grandstream;
  - the API, CSV import and UI disagree with the table (an API test plus `PhoneDetail.test.tsx`).
- [ ] [S-3] `TestGrandstreamWPTemplate` in `internal/prov` passes with golden files for WP822 and WP836. It fails if any key listed in S-3 is missing or has a different value, if P323 to P325 appear, if a WP8x6-only key (P60069, P2336, P22177, P8565, P22269, P95030) appears for WP822, or if a non-WP Grandstream model's output changes.
- [ ] [S-4] `TestAnchorAnyCodec` in `internal/media` and `TestVoicemailWideband` in `internal/sip` pass. They fail if:
  - an anchored call offering only G.722 or only Opus is not relayed;
  - voicemail or an announcement answers a G.722 offer with anything but G.722;
  - a recording of a G.722 call is not decodable audio;
  - telephone-event is lost.
- [x] [S-5] `TestCheckSyncNotify` in `internal/sip` and `TestPhoneResyncAPI` in `internal/api` pass. They fail if:
  - the NOTIFY does not reach every registered contact;
  - a digest challenge is not answered;
  - a reboot is sent for resync;
  - a template change does not push within the debounce;
  - pushes exceed `HELLO_PROV_PUSH_RATE`;
  - an Event: reset is ever sent;
  - a viewer can trigger a push.
- [ ] [S-6] `TestCallPark` in `internal/sip` passes against Valkey. It fails if:
  - `*70`, `*70<slot>` or a REFER to them does not park;
  - the parker does not hear the slot;
  - `*71<slot>` from another replica does not retrieve;
  - two simultaneous retrieves both connect;
  - the timeout does not return to the parker and then voicemail;
  - a slot's dialog BLF does not show occupancy.
- [ ] [S-6] [S-38] [S-3] `TestParkSoftkeyAsWP836` in `test/integration` passes. It fails if the provclient WP836 Park softkey flow (REFER to the P60069 code) does not park, or retrieve via P2336 does not connect.
- [ ] [S-7] `TestFeatureEventSync` in `internal/sip` passes. It fails if a phone's SetDoNotDisturb or SetForwarding does not change the extension as `*78` or `*72` would, if the other devices are not notified, if another extension's device can change it, or if a vendor without feature sync is rendered with local DND enabled.
- [ ] [S-8] `TestSIPMessageDelivery` in `internal/sip` passes. It fails if:
  - a MESSAGE to a registered extension is not delivered to every contact;
  - an offline one is not stored and delivered on REGISTER;
  - a MESSAGE to a trunk, door or unknown number gets 200;
  - a body appears in a log line;
  - the 1,300-byte or 30-per-minute limits are not enforced;
  - the 200-and-drop path still exists.
- [ ] [S-9] `TestSessionTimersAndKeepalive` in `internal/sip` passes. It fails if:
  - a CRLF ping produces an error log or no pong on TCP;
  - a call whose session refresh fails is not torn down;
  - a call with no RTP in both directions for `HELLO_MEDIA_DEAD_SECONDS` stays up while not on hold;
  - a re-INVITE with a new address is not relayed;
  - a re-REGISTER from a new address leaves the old contact.
- [ ] [S-50] `TestCallPickup` in `internal/sip` passes. It fails if:
  - `*60` does not answer the oldest ringing call in scope;
  - `*61<ext>` answers a call outside the dialler's scope;
  - an INVITE with Replaces for an early dialog in scope does not take the call;
  - the other ringing legs are not cancelled;
  - two simultaneous pickups both connect.
- [ ] [S-39] [S-40] `TestRingTagChoice` in `internal/sip` passes. It fails if the order of S-40 is broken, a ring group's tag is ignored, a trunk call is not external, the chosen tag and rule are missing from the CDR and trace, or the choice reads PostgreSQL.
- [ ] [S-41] `TestAlertInfoEmission` in `internal/sip` passes. It fails if:
  - a received Alert-Info reaches a device;
  - a device leg lacks the canonical header or the vendor form;
  - a trunk leg carries any Alert-Info;
  - the auto-answer header of a page or intercom is replaced;
  - the Bellcore slot differs from the tag's.
- [ ] [S-42] [S-44] `TestRingMapProvisioning` in `internal/prov` passes with golden output per vendor. It fails if any vendor's template omits a mapped tag's match rule or tone key from "Vendor reference: distinctive ringing", if a group override is not applied, or if a ring map change does not trigger a check-sync.
- [ ] [S-43] `TestRingtoneLibrary` in `internal/api` and `internal/prov` passes. It fails if a WAV outside the format limits is accepted, if a tone is served without the phone's token or without ETag/304, or if a Grandstream template references a custom tone.
- [ ] [S-45] [S-46] `TestDoorExtension` in `internal/sip` passes. It fails if a door extension can call outside its targets or a trunk, can be a ring-group member or receive MESSAGE, page or pickup, or if its call to the Door group lacks the doorbell tag and the CDR kind door.
- [ ] [S-47] `TestDoorOpenDTMF` in `internal/sip` passes. It fails if:
  - `*50` on a door call does not send the stored code as RFC 2833 to the door leg;
  - the code appears in a log, trace, API response or audit row;
  - `*50` on a non-door call does anything but pass through;
  - hang-up-after-open is not honoured;
  - `POST /api/v1/calls/{id}/door-open` works for a user who did not answer that call.
- [ ] [S-48] `TestDoorWebhook` in `internal/api` passes. It fails if:
  - a request with a missing, wrong or stale signature is not 401;
  - a 401 differs between an existing and an unknown id;
  - the rate limit does not give 429;
  - a valid trigger does not ring the Door group with the doorbell tag (or page the paging group);
  - a coalesced duplicate rings twice;
  - the secret is returned after creation.
- [ ] [S-49] `TestDoorVideoPassthrough` in `internal/sip` passes. It fails if a door call offering H.264 video to a callee whose SDP accepts video loses the video m-line, or adds video to a callee that did not offer it. Hardware preview stays in the lab list as **verify**.
- [ ] [S-10] [S-11] `TestMultiLineProvisioning` in `internal/prov` passes. It fails if lines 2 and 3 do not render the account keys of S-10, if a device is bound to two lines, if the WP822 accepts a line 3, or if account sharing P22521/P22522 is rendered for a group that did not enable it.
- [ ] [S-12] `TestSettingOverrides` in `internal/prov` passes. It fails if the template, group, phone order is broken, a deny-listed key is accepted, a secret value is returned unmasked or rendered for another phone, or the side-key or speed-dial fields render the wrong P-values.
- [ ] [S-13] [S-14] `TestSIPTransports` in `internal/sip` passes. It fails if:
  - REGISTER and INVITE over TCP or TLS do not route with the received transport;
  - a `sips` request leaves over a non-TLS hop;
  - TLS below 1.2 is accepted;
  - Grandstream P130 and P2329 do not follow the model's transport setting.
- [ ] [S-15] [S-16] `TestSRTPAnchor` in `internal/media` passes. It fails if:
  - an anchored SDES call does not encrypt both legs with per-leg keys;
  - a key appears in a log;
  - SRTP is offered on a UDP leg;
  - a DTLS-only offer is not answered with DTLS-SRTP once enabled;
  - an ICE connectivity check to the anchor is not answered;
  - the per-packet benchmark exceeds 50 µs p99.
- [ ] [S-17] `TestPrackAndReplaces` in `internal/sip` passes. It fails if a Require: 100rel offer is refused, a PRACK is not relayed, an INVITE with Replaces for an early dialog does not replace it, or attended transfer before answer leaves both calls up.
- [ ] [S-18] `TestCallWaitingFlag` in `internal/sip` passes. It fails if a busy extension with call waiting on is not rung, or one with it off is rung instead of busy or forward-busy.
- [ ] [S-19] `TestAnonymousOutbound` in `internal/sip` passes. It fails if a device's Privacy: id is dropped on a trunk leg or the trunk leg's From reveals the extension.
- [ ] [S-20] `TestSRVAndBackupProvisioning` in `internal/prov` and the chart test in `test/deploy` pass. They fail if SRV mode renders without the SRV records in the deployment, or if backup proxies do not list the HA nodes.
- [ ] [S-21] `TestDSCPMarking` in `internal/media` and `internal/sip` passes. It fails if a relay socket is not marked 46 or a SIP socket 26 (or the configured values).
- [ ] [S-22] `TestHardeningTemplate` in `internal/prov` and `TestAuthenticatedInviteToPhone` in `internal/sip` pass. They fail if any S-22 key is missing for a WP model, or if Hello does not answer a phone's 401 on an INVITE it originates.
- [ ] [S-23] [S-24] `TestWifiProvisioning` in `internal/prov` passes. It fails if:
  - a profile renders without a verified profile in the group;
  - a PSK appears unsealed in the database or the API;
  - more than 10 profiles are accepted for WP8x6 (or more than 20 for WP822);
  - a radio setting does not render its P-value.
- [ ] [S-25] `TestSCEPServer` in `internal/prov` passes. It fails if a certificate is issued without the phone's one-time challenge, the challenge can be reused, or SCEP answers when off for the group.
- [ ] [S-26] `TestFirmwareMinimum` in `internal/api` and `internal/prov` passes, together with `ProvFirmware.test.tsx`. They fail if a pin below the model minimum is accepted or rendered, or if the drift badge is missing when firmware_seen differs from the pin.
- [ ] [S-27] `TestSyslogSink` passes. It fails if lines from a provisioned phone are not stored and shown, lines from an unknown IP are kept, the per-phone rate cap is not applied, or a redacted secret survives.
- [ ] [S-28] `TestEmergencyCalling` in `internal/sip` and `TestHELDServer` in `internal/prov` pass. They fail if:
  - an emergency call is blocked by DND, forward, restrictions, limits or door targets;
  - P8385 differs from the emergency routes;
  - HELD returns another phone's location or answers without a valid token;
  - a flagged trunk's emergency leg lacks Geolocation and PIDF-LO;
  - an unflagged trunk gets them;
  - a callback within 30 min does not bypass DND.
- [ ] [S-29] `TestAlarmCall` in `internal/sip` passes. It fails if `*55` does not reach the group's alarm destination ignoring DND with ring tag alarm and CDR kind alarm, if `*55` without a destination is not 404, or if P22270 is rendered without a destination.
- [x] [S-30] `TestGrandstreamPTTChannels` in `internal/prov` passes. It fails if a WP8x6 group's PTT channels do not render P22177 to P22194, if a WP822 gets a DSS-key PTT entry, or if validation from phonebooks-ptt P-15 is bypassed.
- [ ] [S-31] `TestGrandstreamPhonebookCap` in `internal/prov/phonebook` passes. It fails if a WP822 directory exceeds 500 entries or a WP8x6 directory exceeds 1000, if truncation breaks the delivered-set order, if P331 carries a scheme, or if the vCard slot is malformed.
- [ ] [S-32] The phonebooks-ptt `TestAutoAnswerHardening` passes with WP822 and WP836 provclient profiles. It fails if a WP template lacks "Accept Incoming SIP from Proxy Only".
- [ ] [S-33] `TestMusicOnHold` in `internal/sip` passes. It fails if a held or parked party hears silence when MOH is configured, or MOH continues after resume.
- [x] [S-34] `TestConferenceRoom`, `TestConferencePIN`, `TestConferenceCapacity`, `TestConferenceControls`, `TestConferenceWaitForLeader`, `TestConferenceMuteOnEntry`, `TestConferenceTwoNodes`, `TestConferenceTakeover` and `TestConferenceKeyLamp` in `internal/sip` pass. They fail if participants cannot hear each other (or a talker hears itself), a wrong PIN joins, an 11th participant joins a default room, a control does not act, a join on the non-owner node does not reach the owner's one mixer, or a node loss does not keep the participants in the room. The N-way URI merge needs a phone (**needs hardware**).
- [ ] [S-35] `TestPresenceExtensions` in `internal/sip` passes. It fails if Event: reg, PUBLISH presence or an eventlist subscription is refused or returns another extension's data.
- [ ] [S-36] `TestRTCPXRCollector` in `internal/sip` passes. It fails if a vq-rtcpxr PUBLISH is not stored on the matching CDR or a malformed one is stored.
- [ ] [S-37] `TestWP822FileSequence` in `test/provclient` passes. It fails if the WP822 profile's request sequence does not end provisioned, or the Grandstream account ringtone does not follow the ring map's internal tone.
- [ ] [S-38] `TestWPPhonesAsVendors` in `test/integration` (`HELLO_DOCKER=1`) passes. It fails if the WP822 or WP836 provclient profile does not complete MWI, check-sync, park, pickup, DND sync, MESSAGE, a tagged ring and a door call. Every lab list item is printed as "confirmed" or "needs hardware".
- [ ] [S-6] [S-7] [S-42] [S-45] [S-48] [S-23] [S-28] [S-34] `procoder test` and `procoder lint` pass over `web/`. `Ringing.test.tsx`, `Door.test.tsx`, `Park.test.tsx`, `Messages.test.tsx`, `Conferences.test.tsx` and `Locations.test.tsx` fail if a validation error is not shown at its field, a write-only secret (door code, webhook secret, PSK) is displayed after save, or a viewer sees a write control.
- [ ] [S-1] [S-5] [S-8] [S-48] The existing OpenAPI conformance test in `internal/api` passes with every route of Interfaces in both `openapi.json` and `routes.go`. It fails if a route is missing from either.

## Traceability

Every gap row of both reports, with what this spec does and the PR (Phased delivery number) that closes it. "PB-x" means a phonebooks-ptt phase. Rows the reports mark supported or n/a with nothing to do are omitted.

### [R822] WP822

| Report section and row                       | Status in report              | Closed by                                   | PR             |
| -------------------------------------------- | ----------------------------- | ------------------------------------------- | -------------- |
| §1 Config file name sequence                 | supported, confirm            | S-37                                        | 2              |
| §1 Second account                            | missing                       | S-10                                        | 21             |
| §1 Codec order                               | missing                       | S-3, S-4                                    | 2, 3           |
| §1 DTMF mode                                 | partial                       | S-3                                         | 2              |
| §1 MWI subscribe flag                        | missing                       | S-1, S-3                                    | 1, 2           |
| §1 Voicemail access number                   | partial                       | S-3                                         | 2              |
| §1 Feature-code / DND / forward sync         | missing                       | S-7                                         | 7              |
| §1 Phonebook URL / interval                  | planned                       | PB-B, S-31                                  | PB-B, 18       |
| §1 Wallpaper / ringtone                      | missing                       | S-12, S-44                                  | 12, 15         |
| §1 Config file encryption / P1 auth          | missing                       | S-22                                        | 27             |
| §1 Speed dial keys                           | missing                       | S-12                                        | 15             |
| §2 SIP over TCP / TLS                        | missing                       | S-13, S-14                                  | 22, 23         |
| §2 SRTP                                      | missing                       | S-15                                        | 24             |
| §2 Keep-alive RFC 5626 / OPTIONS / TCP       | unverified                    | S-3, S-9                                    | 2, 9           |
| §2 DNS SRV / failover / failback             | partial                       | S-20                                        | 29             |
| §2 Session timers                            | missing                       | S-9                                         | 9              |
| §2 100rel / PRACK                            | missing                       | S-17                                        | 26             |
| §2 Authenticate incoming INVITE / proxy only | unverified                    | S-22                                        | 27             |
| §2 Trusted Domain Name list                  | missing                       | S-22                                        | 27             |
| §2 Factory reset via NOTIFY                  | missing                       | S-5 (never sent; Allow SIP Reset off), S-22 | 4, 27          |
| §2 RTCP-XR                                   | missing                       | S-36                                        | 35             |
| §3 Attended transfer before answer           | unverified                    | S-17                                        | 26             |
| §3 3-way conference                          | supported; server rooms built | S-34                                        | 33             |
| §3 Call waiting                              | unverified                    | S-18                                        | 26             |
| §3 Call forward (phone-local not synced)     | supported, partial            | S-7                                         | 7              |
| §3 DND                                       | partial                       | S-7                                         | 7              |
| §3 Call pickup                               | missing                       | S-50 (defaults 2 and 4)                     | 5              |
| §3 Call park                                 | missing                       | S-6                                         | 6              |
| §3 Intercom                                  | planned                       | PB-E, S-32                                  | PB-E, 27       |
| §3 Distinctive ring                          | missing                       | S-39 to S-44                                | 10, 11, 12     |
| §3 Caller name display                       | planned                       | PB-C                                        | PB-C           |
| §3 Emergency / E911                          | partial                       | S-28                                        | 30             |
| §3 Off-hook auto dial / hotline              | missing                       | S-12                                        | 15             |
| §3 Hot desking                               | n/a                           | documented unsupported (default 16), S-11   | 21             |
| §3 Anonymous outbound                        | unverified                    | S-19                                        | 26             |
| §4 BLF keys                                  | partial                       | S-2, S-3, S-12 (speed dials)                | 2, 15          |
| §4 BLF eventlist                             | missing                       | S-35                                        | 34             |
| §4 Presence PUBLISH                          | missing                       | S-35                                        | 34             |
| §4 SUBSCRIBE for registration                | missing                       | S-1 (489), S-35                             | 1, 34          |
| §5 MWI SUBSCRIBE message-summary             | missing                       | S-1                                         | 1              |
| §5 Unsolicited MWI                           | partial                       | S-1                                         | 1              |
| §5 Voicemail access                          | supported, template default   | S-3                                         | 2              |
| §5 Voicemail with wideband / Opus            | partial                       | S-4                                         | 3              |
| §5 SIP MESSAGE                               | missing                       | S-8                                         | 8              |
| §6 G.722                                     | partial                       | S-4                                         | 3              |
| §6 Opus                                      | partial                       | S-4                                         | 3              |
| §6 G.729 / iLBC                              | unverified                    | S-4 (relayed), S-3 (disabled in order)      | 2, 3           |
| §6 Jitter buffer / VoWLAN target delay       | n/a, optional                 | S-12                                        | 15             |
| §6 DSCP                                      | partial                       | S-21                                        | 28             |
| §7 XML phonebook, 500 cap                    | planned                       | PB-B, S-31                                  | PB-B, 18       |
| §7 LDAP                                      | missing by decision           | out of scope (decision)                     | —              |
| §7 Caller-name lookup                        | planned                       | PB-C                                        | PB-C           |
| §7 Local contacts read-only                  | planned                       | S-31                                        | 18             |
| §8 Multicast paging listen                   | planned                       | PB-F, S-30                                  | PB-F, 18       |
| §8 PTT button (no DSS keys)                  | planned, spec wrong           | S-30                                        | 18             |
| §8 Server paging groups                      | planned                       | PB-E                                        | PB-E           |
| §8 Intercom 1:1                              | planned                       | PB-E                                        | PB-E           |
| §8 Restrict auto-answer to server            | planned                       | S-32                                        | 27             |
| §8 Door system (GDS)                         | missing                       | S-45 to S-49                                | 13, 14, 16, 17 |
| §9 Remote resync / reboot                    | missing                       | S-5                                         | 4              |
| §9 Firmware confirmation bypass              | supported, optional           | S-22                                        | 27             |
| §9 Firmware downgrade guard                  | unverified                    | S-26                                        | 19             |
| §9 TR-069                                    | missing                       | S-22 (off)                                  | 27             |
| §9 GDMS                                      | partial                       | out of scope beyond provisioning spec       | —              |
| §9 Syslog                                    | missing                       | S-27                                        | 36             |
| §9 Keypad menu / SSH / web access            | missing                       | S-22                                        | 27             |
| §10 SSID provisioning                        | missing                       | S-23                                        | 31             |
| §10 802.11r/k/v                              | missing                       | S-24                                        | 31             |
| §10 Roaming threshold                        | missing                       | S-24                                        | 31             |
| §10 Power save                               | missing                       | S-12                                        | 15             |
| §10 Registration resilience on roam          | unverified                    | S-9                                         | 9              |
| §10 NAT keep-alive on Wi-Fi sleep            | unverified                    | S-3, S-9                                    | 2, 9           |
| §11 WP822 as known model                     | partial                       | S-2, S-3, S-38                              | 2              |
| §11 Per-phone extra settings                 | missing                       | S-12                                        | 15             |
| §11 Remote actions                           | missing                       | S-5                                         | 4              |
| §11 Paging / phonebook pages                 | planned                       | PB-B, PB-E                                  | PB             |

### [R8x6] WP816 / WP826 / WP836

| Report section and row                                      | Status in report                      | Closed by               | PR         |
| ----------------------------------------------------------- | ------------------------------------- | ----------------------- | ---------- |
| §1 Multiple accounts (3)                                    | partial                               | S-10                    | 21         |
| §1 Resync interval P193 clamp                               | supported, risk                       | S-2                     | 2          |
| §1 Check-sync push, P95030, P4428                           | missing                               | S-5, S-3                | 4          |
| §1 Firmware standardise                                     | supported, action                     | S-26 (default 13)       | 19         |
| §1 User Protection P9996                                    | missing                               | S-3                     | 2          |
| §1 Codec / DTMF / SIP options                               | missing                               | S-3                     | 2          |
| §1 Per-model template knowledge                             | partial                               | S-2, S-3                | 2          |
| §1 TR-069                                                   | missing                               | S-22 (off)              | 27         |
| §1 AES-encrypted config                                     | missing                               | S-22                    | 27         |
| §2 SIP over TCP                                             | missing                               | S-13                    | 22         |
| §2 SIP over TLS                                             | missing                               | S-14                    | 23         |
| §2 SRTP (SDES, DTLS)                                        | missing                               | S-15, S-16              | 24, 25     |
| §2 OPTIONS keep-alive                                       | supported, unverified                 | S-3, S-9                | 2, 9       |
| §2 NAT / ICE / STUN                                         | partial                               | S-16                    | 25         |
| §2 Session timers                                           | missing                               | S-9                     | 9          |
| §2 100rel / PRACK                                           | missing                               | S-17                    | 26         |
| §3 Music on hold                                            | missing                               | S-33                    | 32         |
| §3 Blind / attended transfer verify                         | supported, verify                     | S-17                    | 26         |
| §3 Server conference P2318                                  | rooms built; P2318 needs hardware     | S-34                    | 33         |
| §3 Call park P60069 / P2336                                 | missing                               | S-6, S-3                | 6          |
| §3 Call pickup                                              | missing                               | S-50                    | 5          |
| §3 Forward / DND (server vs local)                          | partial                               | S-7                     | 7          |
| §3 Auto answer / intercom (P298, P26019, P26071, P2356)     | planned                               | PB-E, S-3, S-32         | PB-E, 2    |
| §3 Distinctive ring P95026                                  | missing                               | S-39 to S-44            | 10, 11, 12 |
| §3 Emergency numbers P8385                                  | partial                               | S-28                    | 30         |
| §3 Alarm P22269 to P22602                                   | missing                               | S-29                    | 30         |
| §3 E911 / HELD / Geolocation, P26059                        | unverified                            | S-28                    | 30         |
| §4 BLF keys not supported                                   | supported server side, wrong template | S-2, S-3                | 2          |
| §4 PUBLISH presence P188                                    | missing                               | S-35 (P188 kept 0)      | 34         |
| §4 SUBSCRIBE for registration                               | missing                               | S-35                    | 34         |
| §5 MWI indicator                                            | partial                               | S-1                     | 1          |
| §5 SUBSCRIBE for MWI P99                                    | missing                               | S-1, S-3                | 1, 2       |
| §5 SIP instant messaging, P22650                            | missing                               | S-8, S-3                | 8          |
| §6 Codec list / order P57 to P64; G.722 voicemail           | partial                               | S-3, S-4                | 2, 3       |
| §6 Recording of wideband calls                              | unverified                            | S-4                     | 3          |
| §6 DTMF P2301 to P2303                                      | supported, provision                  | S-3                     | 2          |
| §6 QoS DSCP P1558 / P1559                                   | n/a, optional                         | S-21                    | 28         |
| §7 XML phonebook P330 to P332, 1000 cap                     | planned                               | PB-B, S-31              | PB-B, 18   |
| §7 Remove manual entries / import group                     | planned                               | S-31                    | 18         |
| §7 LDAP                                                     | missing by decision                   | out of scope (decision) | —          |
| §7 vCard phonebook                                          | missing                               | S-31                    | 18         |
| §8 Server paging                                            | planned                               | PB-E                    | PB-E       |
| §8 1-to-1 intercom P26072 / P26071                          | planned                               | PB-E, S-3               | PB-E, 2    |
| §8 Multicast paging P22208, P1566 to P1569, P22532          | planned                               | PB-F, S-30              | PB-F, 18   |
| §8 Grandstream PTT channels / group paging P22177 to P22194 | not covered                           | S-30                    | 18         |
| §8 Side key P82317                                          | planned (implicitly)                  | S-12                    | 15         |
| §9 Firmware management / pinning                            | supported, action                     | S-26                    | 19         |
| §9 Syslog P207 / P208                                       | missing                               | S-27                    | 36         |
| §9 TR-069                                                   | missing                               | S-22                    | 27         |
| §9 SCEP                                                     | missing                               | S-25                    | 37         |
| §9 Account sharing P22521 / P22522                          | n/a, document                         | S-11                    | 21         |
| §10 Wi-Fi profiles P83000 to P83003                         | missing                               | S-23                    | 31         |
| §10 Band selection P25438                                   | missing                               | S-24                    | 31         |
| §10 802.11k/r/v (docs)                                      | n/a, docs                             | S-24                    | 31         |
| §10 Roaming mode / threshold P82332 / P22232 / P82334       | missing                               | S-24                    | 31         |
| §10 802.1X / enterprise CA                                  | missing                               | S-25                    | 37         |
| §10 Fast failure detection after Wi-Fi drop                 | partial                               | S-9                     | 9          |
| §11 Model awareness                                         | partial                               | S-2                     | 2          |
| §11 Park / pickup codes in UI                               | missing                               | S-6, S-50               | 5, 6       |
| §11 Phonebook / paging / phone groups UI                    | planned                               | PB-B, PB-E, PB-F        | PB         |
| §11 Firmware version visibility                             | unverified                            | S-26                    | 19         |

## Phased delivery

Each item is one PR, small enough to review and deploy on its own, merged in order. Each PR updates `docs/provisioning.md` or the feature docs it touches, closes its lab-list items or marks them "needs hardware" in the PR, and keeps `TestProvisioningAsVendors` green. Phonebooks-ptt phases A to F proceed in parallel. PRs here that extend them (18, part of 27) wait for the phase they extend.

Daily use (P1):

1. **MWI standard** (S-1): message-summary SUBSCRIBE and unsolicited NOTIFY; MESSAGE push removed; 489 for unknown events.
2. **WP model capabilities and template** (S-2, S-3, S-37, S-38 profiles): capability table, BLF guard in API/CSV/UI, WP822 and WP8x6 template keys, provclient WP profiles.
3. **Codec-robust anchor and media endpoints** (S-4): any-PT relay; G.722 and Opus in voicemail, announcements and recording.
4. **Check-sync push** (S-5): NOTIFY with digest answer, resync/reboot API and buttons, auto push with debounce and rate.
5. **Call pickup**: pickup groups, group `*60` and directed `*61<ext>` actions, UI (S-50, defaults 2 and 4).
6. **Call park** (S-6): park lot, Valkey slots, `*70`/`*71`, REFER park, slot BLF, return and timeout, UI.
7. **DND/forward feature sync** (S-7): as-feature-event server and vendor keys; local DND off where unsupported.
8. **SIP MESSAGE delivery** (S-8): delivery, offline store, limits, Messages page.
9. **Session timers, keep-alive and roaming resilience** (S-9): RFC 4028, CRLF, dead-media teardown, address relearn; lab roam test.

Distinctive ringing and doorbell:

10. **Ring tags and Alert-Info** (S-39, S-40, S-41): tags, rules, the canonical header, CDR ring_tag.
11. **Ring map provisioning** (S-42, S-44): built-in tones per vendor for all five vendors, group overrides, Ringing page.
12. **Tone library** (S-43): upload, MinIO, token-served WAVs, custom tones in templates.
13. **Door station and Door group** (S-45, S-46): extension kind door, targets, Door ring group with the doorbell tag.
14. **Door-open DTMF** (S-47): sealed code, `*50`, web button, phone keys.
15. **Setting overrides and WP extras** (S-12): group and phone overrides, speed dials replacing BLF on WP, side key, hotline, wallpaper, power save, VoWLAN delay, band.
16. **Doorbell webhook** (S-48): HMAC trigger, rate limit, ring and chime-page actions, Door page.
17. **Door video preview** (S-49, optional, verify): video pass-through and vendor preview keys.

The rest:

18. **Phonebooks-ptt additions** (S-30, S-31): Grandstream PTT channels and group paging (phase F addition), model phonebook caps, P330 to P332 confirmation, vCard slot (phase B addition).
19. **Firmware pinning and visibility** (S-26): minimum versions, WP defaults, drift badge, upgrade window.
20. **Lab sweep for P1** (S-38): `TestWPPhonesAsVendors` covering PRs 1 to 9 and the hardware run of lab items 1 to 8.
21. **Multiple lines and account sharing** (S-10, S-11).
22. **SIP over TCP** (S-13).
23. **SIP over TLS and sips** (S-14), with the cert-manager certificate on kw.
24. **SRTP SDES** (S-15).
25. **DTLS-SRTP and ICE-lite** (S-16).
26. **PRACK, Replaces, call waiting, anonymous outbound** (S-17, S-18, S-19).
27. **Provisioning hardening** (S-22, S-32): TR-069 off, SSH off, proxy-only with authenticated INVITE, trusted domains, P1 auth, config encryption, firmware confirmation bypass.
28. **DSCP marking** (S-21).
29. **DNS SRV and backup servers** (S-20).
30. **Emergency and alarm** (S-28, S-29): bypasses, P8385, locations, HELD, Geolocation, callback tag, alarm `*55`.
31. **Wi-Fi profiles and roaming** (S-23, S-24).
32. **Music on hold** (S-33).
33. **Server conference** (S-34).
34. **Presence extensions** (S-35): reg event, PUBLISH, eventlist.
35. **RTCP-XR collector** (S-36).
36. **Syslog sink** (S-27).
37. **SCEP and 802.1X** (S-25).

## Open questions

<!-- None. The user's instruction of 2026-10-25 ("ALL") sets scope; design details were resolved by the defaults listed under "Defaults chosen for open points"; items marked verify are lab items, not questions. -->
