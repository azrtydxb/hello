# Completion plan (2026-11-09)

Sources: `.procoder/tracking/unbuilt.md`, the open PRs, the procoder backlog and the GitHub issues and milestones. GitHub has no open issues and no milestones. The procoder backlog has one open story, the vendor redirect live test, which waits on hardware. So the tracker and the open PRs are the whole picture.

Working method for every wave:

- Builder agents work in worktrees, three or four at a time, one item each, with tests that fail without the change.
- One merge captain merges serially. It renumbers migrations, requires integration, and merges migration-free PRs on go/web.
- After each wave, deploy to kw through Sync with 0 active calls, and update the tracker in the same commit.

## Wave 0: land what is already written (open PRs)

The merge captain is running this wave now, in this order:

| PR   | Item                                             |
| ---- | ------------------------------------------------ |
| #204 | Ring tags, Alert-Info (parity 10)                |
| #226 | Ring map, Ringing page (parity 11)               |
| #238 | Tone library (parity 12)                         |
| #225 | Door station, Door group (parity 13)             |
| #228 | Session timers on moved dialogs (parity 9b)      |
| #206 | Codec in call history                            |
| #232 | Firmware policy (parity 19)                      |
| #234 | Click-to-call (integrations P3)                  |
| #222 | Webhooks (integrations P2)                       |
| #220 | Voicemail groups (A6)                            |
| #219 | Backup and restore (A2)                          |
| #223 | Phone LDAP directory (A7)                        |
| #210 | Blocklist, anonymous reject, country limits (A1) |
| #211 | Messaging inbox core (messaging 1)               |
| #235 | Syslog sink (parity 36)                          |
| #236 | Boss/secretary (B2)                              |

Also in this wave:

- #255 jsdom 31: merge once web is green.
- Kamailio HA 2/4 to 4/4 (#207 draft, #215, #216): finish the VIP (192.168.10.109), re-point phones, chart and docs. This needs a phone re-provision, so deploy it in a quiet window.
- Re-pin Kamailio to the TLS-capable image (`kc-1cb144b…`). This is only needed when SIP TLS is switched on.
- Tracker hygiene: mark #224, #237, #243, #244, #256 and #257 done, and correct stale rows (queue callback, boss/secretary).

## Wave 1: phone parity, which unlocks the clients work

1. Door follow-ups:
   - Door-open DTMF `*50` (14)
   - Doorbell webhook (16)
   - Door video preview, code only (17)
2. Transport and security chain:
   - SIP over TCP, finishing the hello-sip side (22)
   - SRTP SDES (24)
   - DTLS-SRTP and ICE-lite (25)
3. Signalling:
   - PRACK/100rel and anonymous outbound (26)
   - Presence: reg event, PUBLISH, eventlist (34)
   - RTCP-XR collector (35)
4. Provisioning:
   - Setting overrides and WP extras (15)
   - vCard phonebook slot (18)
   - Backup/secondary servers (29)
   - Multiple lines and account sharing (21)
   - Wi-Fi profiles and roaming (31)
   - SCEP and 802.1X (37)
   - HELD, locations, alarm `*55` (30)
   - Vendor template keys left out
   - Provisioning hardening (27). TR-069 needs the user's decision first.
5. Lab sweep `TestWPPhonesAsVendors` (20).

## Wave 2: the remaining PBX gaps

- Follow-me
- Fax (T.38 and email delivery)
- Anything left of B2/B4 after review

## Wave 3: flow canvas

- P6: AI on the canvas, through go-ai-sdk
- P7: new node types (batch ops, trace, override badge)

## Wave 4: clients, WebRTC and video

Starts after waves 1.2 and 1.3.

- Web devices and credentials (3)
- coturn and ICE servers (4)
- Media foundation on pion (1)
- WSS on Kamailio (2)
- Web softphone (5)
- Web video (6)
- Video recording (6b)
- Conference-room video (11)
- Video phones and door stations (10)
- Teams SBC-peer profile and docs (13)

## Wave 5: integrations

- P4: recording links and lookup API
- P5: browser extension
- P6: CRM framework and HubSpot
- P7: Zapier
- P8 to P12: directory engine and LDAP/AD, Google/Entra, SCIM, OIDC/LDAP login, range auto-provisioning
- P13 to P21: the CRM, helpdesk and PSA connectors
- P22: Teams guide

Live checks need real accounts, so they go on the hardware/accounts list.

## Wave 6: messaging channels

- 2: Twilio
- 3: desk-phone and SIP-trunk texting
- 4: LAN chat widget
- 5: routing/ACD
- 6: Telnyx and sender registration
- 7: AI triage and drafts
- 8: integrations, reporting, DSAR and redaction

Live checks need Twilio/Telnyx accounts.

## Not now (user decisions)

- Messaging N1 to N3 (public listener, WhatsApp, polling)
- Mobile push, apps and video (7 to 9)
- Public reach (12)
- #105 to #107, which stay paused

## Waiting on hardware or outside accounts

The list at the end of the tracker stays as it is. Each wave adds its own live checks to it.
