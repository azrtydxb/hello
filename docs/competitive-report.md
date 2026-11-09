# Hello vs Asterisk/FreePBX, 3CX V20, Grandstream UCM6300: competitor facts

- **Source report:** competitive gap report dated 2026-10-25 (baseline `origin/main` at `8ac01c2`). This version keeps the facts and citations and drops the ranking, plan and commentary.
- **Trimmed:** 2026-10-26, for `.procoder/specs/pbx-gaps.md`.
- **Hello status column:** checked against the checkout at `b64cc56` on 2026-10-26 by code search. Where the 2026-10-25 status differs, the difference is noted.

## Method

- Competitor claims were re-checked against official documentation. Reference IDs such as `[3-FM]` resolve to the URLs in the References section.
- `UNVERIFIED` means no official page was confirmed. `not found` means the official pages searched did not show the feature.
- Hello statuses come from code paths (`internal/`, `cmd/`, `web/`, `migrations/`) and from `.procoder/specs/`.

## Legend

- **Y:** has the feature.
- **Y$:** paid add-on or a higher edition (edition given).
- **P:** partial.
- **N:** does not have it, or not found.
- **U:** unverified.

## Features covered by pbx-gaps

| #   | Feature                                             | Asterisk / FreePBX             | 3CX V20                                    | UCM6300                                | Hello at `b64cc56` (code search)                                                                                                                                                       | pbx-gaps item |
| --- | --------------------------------------------------- | ------------------------------ | ------------------------------------------ | -------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------- |
| 1   | Backup and restore, scheduled                       | Y [F-backup]                   | Y [3-BK]; scheduled restore AI only [3-FM] | Y [U-M]                                | Not found: no backup code under `internal/`                                                                                                                                            | S-2 to S-4    |
| 2   | Follow-me, simultaneous or sequential external ring | Y [A-FollowMe][F-findmefollow] | Y forwarding rules, mobile [3-EXT]         | Y Follow Me, Ring Simultaneously [U-M] | Not found. Ring groups (`internal/sip/groups.go`) have `ring-all` and `sequential` over extensions only                                                                                | S-5, S-6      |
| 3   | Number blocklist                                    | Y [F-blacklist]                | Y CID blacklist [3-ADV]                    | Y blacklist/whitelist [U-M]            | Not found                                                                                                                                                                              | S-7           |
| 3a  | Anonymous-caller reject                             | U                              | Y [3-ADV] (UNVERIFIED wording)             | Y [U-M]                                | Not found                                                                                                                                                                              | S-8           |
| 3b  | Allowed country codes                               | U                              | Y [3-ADV]                                  | P country IP defence [U-M]             | Not found                                                                                                                                                                              | S-8           |
| 4   | Queue callback (keep position)                      | Y$ VQ Plus [F-QCB]             | Y$ PRO/AI/SMB [3-FM][3-Q]                  | Y Virtual Queue [U-M]                  | Not found. Queues are on the checkout (`internal/sip/queue.go`, migration `00040`)                                                                                                     | S-9, S-10     |
| 5   | 2FA (TOTP)                                          | N/U (forum only [F-2FA])       | Y [3-EXT]                                  | Y [U-M]                                | Not found                                                                                                                                                                              | S-11, S-12    |
| 6   | Dial-by-name directory                              | Y [A-Directory][F-directory]   | U                                          | Y "Dial By Name" [U-M]                 | Not found                                                                                                                                                                              | S-13          |
| 7   | Voicemail groups / broadcast                        | Y [F-vmblast]                  | U                                          | Y Voicemail Group [U-M]                | Not found                                                                                                                                                                              | S-14          |
| 8   | Fax (T.38 or G.711, fax-to-email)                   | Y [A-ReceiveFAX][F-fax]        | Y$ not SMB [3-FM][3-FAX]                   | Y [U-M]                                | Not found                                                                                                                                                                              | S-15 to S-17  |
| 9   | Boss/secretary                                      | U                              | Y$ AI/PRO [3-FM][3-EXT]                    | U                                      | Not found                                                                                                                                                                              | S-18, S-19    |
| 10  | Multi-language prompts                              | Y [F-languages][F-soundlang]   | Y [3-EXT][3-IVR][3-Q]                      | Y 17 languages [U-M]                   | Partial. `internal/voiceprompt` has `en` and `nl` tables and `Languages()`. Voicemail prompts use one global `PromptLanguage` (`internal/api/voicemail_voice.go`). No per-box language | S-20, S-21    |
| 11  | Codec per leg in call history                       | not in the report              | not in the report                          | not in the report                      | Partial. Negotiated codecs are tracked per call (`internal/sip/media.go`, `applyNegotiated`). The CDR has no codec field (`internal/store/cdr.go`)                                     | S-22, S-23    |
| 12  | Phone LDAP directory (phones query an LDAP server)  | not in the report              | not in the report                          | not in the report                      | Directory sync (AD/LDAP client) is SPEC-ONLY in `.procoder/specs/integrations.md`, a client that pulls users. No LDAP server code                                                      | S-24 to S-26  |

Notes for the rows above:

- Row 3a and 3b competitor cells are taken from the matrix of the source report. Their wording is marked UNVERIFIED where the source marked it so.
- Row 11 and row 12: the source report did not compare competitors on these. The competitor cells are blank by design, not a claim of absence.

## Other facts from the source report (context only)

- **3CX pricing and editions.** Queues, barge, wallboard, CRM and hot-desking need PRO or AI [3-FM]. A flat annual fee is charged per simultaneous-call tier, for example 8 SC at $300, $395 and $575 for BASIC, PRO and AI [3-PR].
- **FreePBX commercial modules.** Callback, wallboard, hot-desk, transcription and HA are paid modules [F-QCB][F-WB][F-HD][F-Scribe][F-AR].
- **HA.** 3CX Inbuilt failover is AI edition only at 16 SC and above, active/passive [3-FM][3-BK]. FreePBX Advanced Recovery is a commercial active/standby module [F-AR]. UCM active/standby needs two identical units with a heartbeat link [U-HA].
- **Hello HA.** Active-active with in-call takeover (`internal/sip/takeover.go`, `takeoverLoop`, `takeoverPass`), per `docs/ha.md`.
- **Hello API.** `internal/api/openapi.json`, 137 paths and 190 operations. 3CX's configuration API is AI edition only [3-FM][3-XAPI].
- **Hello MCP.** `internal/mcp` and `docs/ai-access.md`. No MCP or AI-admin surface was found in any of the three vendors' docs.
- **Hello provisioning.** Five vendors built in (`internal/prov/builtin/{yealink,poly,snom,fanvil,grandstream}`), per `docs/provisioning.md`.
- **Hello TTS prompts.** Migration `00017_tts_prompts.sql` and `docs/voice-prompts.md`. UCM has AI TTS announcements [U-M] and FreePBX has a `tts` module [F-tts], so this is parity.

## References

**Asterisk** (base `https://docs.asterisk.org/Latest_API/API_Documentation/Dialplan_Applications/`):

- [A-Directory] .../Directory/
- [A-FollowMe] .../FollowMe/
- [A-ReceiveFAX] .../ReceiveFAX/
- [A-Queue] .../Queue/

**FreePBX open-source modules.** `[F-<m>]` = `https://github.com/FreePBX/<m>`, existence verified through the GitHub API in the source report:

- backup, blacklist, findmefollow, fax, languages, soundlang, directory, vmblast, callback, queues, userman, tts.

**FreePBX commercial and other:**

- [F-QCB] https://sangomakb.atlassian.net/wiki/spaces/PG/pages/35193846/PBX+GUI+-+Queue+Callback+Module
- [F-WB] https://sangomakb.atlassian.net/wiki/spaces/PG/pages/35292431/Queue+Wallboard
- [F-HD] https://sangomakb.atlassian.net/wiki/spaces/Phones/pages/19628157
- [F-Scribe] https://sangomakb.atlassian.net/wiki/spaces/PG/pages/488898594/PBX+GUI+-+Scribe
- [F-AR] https://www.freepbx.org/?p=51745
- [F-2FA] https://community.freepbx.org/t/two-factor-auth-for-freepbx/66683 (community forum, not official, so UNVERIFIED)

**3CX:**

- [3-FM] https://www.3cx.com/ordering/pricing/features/ (edition matrix, updated 2026-08-28)
- [3-PR] https://www.3cx.com/ordering/pricing/
- [3-BK] https://www.3cx.com/docs/manual/backup/
- [3-ADV] https://www.3cx.com/docs/manual/advanced-features/
- [3-EXT] https://www.3cx.com/docs/manual/create-extensions/
- [3-Q] https://www.3cx.com/docs/manual/call-center-queues/
- [3-FAX] https://www.3cx.com/docs/manual/fax-server/
- [3-IVR] https://www.3cx.com/docs/manual/ivr/
- [3-ROLES] https://www.3cx.com/docs/manual/access-roles/
- [3-XAPI] https://www.3cx.com/docs/configuration-rest-api/

**Grandstream:**

- [U-M] https://documentation.grandstream.com/knowledge-base/ucm630x-series-user-manual/ (sections named in the cells)
- [U-HA] https://documentation.grandstream.com/knowledge-base/ucm630x-a-high-availability/

**Hello evidence (checkout `b64cc56`, 2026-10-26):**

- Code and migrations as cited in the table. The spec that uses them is `.procoder/specs/pbx-gaps.md`.
