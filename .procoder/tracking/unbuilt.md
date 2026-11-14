# Unbuilt work tracker

Source: the 2026-10-26 audit of every spec, decisions.md and the conversation against origin/main, re-checked against the merged PRs and the code on 2026-11-14. Each row has one status: `done (#PR)`, `wip (#open PR)`, `todo`, `partial (#PR): what is missing`, or `not now` (user decision). Live checks that wait only on real hardware or an outside account are in the list at the end, not in the rows.

## phone-feature-parity

| #   | Item                                                                        | Status                                                                                                                                                                                                                                                            |
| --- | --------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 9   | Session timers, CRLF keep-alive, dead-media teardown, roaming relearn (S-9) | done (#203)                                                                                                                                                                                                                                                       |
| 9b  | Session timers on parked/picked-up/transferred dialogs and trunk legs       | done (#228)                                                                                                                                                                                                                                                       |
| 10  | Ring tags, Alert-Info rules, CDR ring_tag (S-39..41)                        | done (#204)                                                                                                                                                                                                                                                       |
| 11  | Ring map provisioning, Ringing page (S-42, S-44)                            | done (#226)                                                                                                                                                                                                                                                       |
| 12  | Tone library (S-43)                                                         | done (#238)                                                                                                                                                                                                                                                       |
| 13  | Door station extension kind, Door group (S-45, S-46)                        | done (#225)                                                                                                                                                                                                                                                       |
| 14  | Door-open DTMF `*50` (S-47)                                                 | done (#283)                                                                                                                                                                                                                                                       |
| 15  | Setting overrides and WP extras (S-12)                                      | partial (#259): the first-class side key field (PTT, paging or alarm; P82317 is written as a fixed 0) and the speed-dial fields (WP "BLF" entries offered as speed dials) are not built; the niche keys are reachable only through overrides                      |
| 16  | Doorbell webhook (S-48)                                                     | done (#284)                                                                                                                                                                                                                                                       |
| 17  | Door video preview (S-49)                                                   | done (#285)                                                                                                                                                                                                                                                       |
| 18  | vCard phonebook slot (S-31)                                                 | done (#261)                                                                                                                                                                                                                                                       |
| 19  | Firmware minimum versions, WP defaults, drift badge, upgrade window (S-26)  | done (#232)                                                                                                                                                                                                                                                       |
| 20  | Lab sweep `TestWPPhonesAsVendors` (S-38)                                    | done (#322)                                                                                                                                                                                                                                                       |
| 21  | Multiple lines and account sharing (S-10, S-11)                             | done (#270, #321)                                                                                                                                                                                                                                                 |
| 22  | SIP over TCP: provisioning the transport, hello-sip side (S-13)             | done (#258)                                                                                                                                                                                                                                                       |
| 23  | SIP over TLS and sips (S-14)                                                | done (#202)                                                                                                                                                                                                                                                       |
| 24  | SRTP SDES (S-15)                                                            | done (#266, #274)                                                                                                                                                                                                                                                 |
| 25  | DTLS-SRTP and ICE-lite (S-16)                                               | done (#276)                                                                                                                                                                                                                                                       |
| 26  | PRACK/100rel, anonymous outbound (Privacy) (S-17, S-19)                     | partial (#265): an INVITE with Replaces is served only for an early dialog in the caller's pickup scope; Replaces of a confirmed dialog (S-17) and `TestPrackAndReplaces` are not built                                                                           |
| 27  | Provisioning hardening (S-22, S-32), TR-069 off                             | partial (#263, #264): Hello does not answer a phone's 401 on an INVITE it originates, so P2346 Authenticate Incoming INVITE stays unwritten and `TestAuthenticatedInviteToPhone` (S-22) is missing; TR-069/SSH-off keys for Poly, Snom and Fanvil are not written |
| 28  | DSCP marking (S-21)                                                         | done (#231)                                                                                                                                                                                                                                                       |
| 29  | Phone backup/secondary servers (S-20)                                       | partial (#262, #312): backup proxies come from the static `HELLO_PROV_SIP_BACKUP_SERVERS` list, not from the HA node list; no Snom or Fanvil backup keys; no SRV (DNS-NAPTR) mode for Yealink and Poly                                                            |
| 30  | HELD, Geolocation, locations, callback tag, alarm `*55` (S-28, S-29)        | done (#275)                                                                                                                                                                                                                                                       |
| 31  | Wi-Fi profiles and roaming (S-23, S-24)                                     | done (#278, #330)                                                                                                                                                                                                                                                 |
| 34  | Presence: reg event, PUBLISH, eventlist (S-35)                              | done (#268)                                                                                                                                                                                                                                                       |
| 35  | RTCP-XR collector (S-36)                                                    | done (#260)                                                                                                                                                                                                                                                       |
| 36  | Syslog sink (S-27)                                                          | done (#235)                                                                                                                                                                                                                                                       |
| 37  | SCEP and 802.1X (S-25)                                                      | done (#279, #330)                                                                                                                                                                                                                                                 |

## integrations

| #   | Item                                           | Status            |
| --- | ---------------------------------------------- | ----------------- |
| P1  | Call events: stream, integration_events, SSE   | done (#205)       |
| P2  | Webhooks: signing, retries, DLQ                | done (#222)       |
| P3  | Click-to-call: originate API, /dial, tel:      | done (#234)       |
| P4  | Recording links and lookup API                 | done (#281, #304) |
| P5  | Browser extension (MV3), fixed signing key     | done (#327, #345) |
| P6  | HubSpot and internal/crm framework             | done (#289, #333) |
| P7  | Zapier                                         | done (#304)       |
| P8  | Directory engine, AD/LDAP                      | done (#306)       |
| P9  | Google Workspace and Entra connectors          | todo              |
| P10 | SCIM 2.0                                       | todo              |
| P11 | OIDC SSO, LDAP bind login                      | todo              |
| P12 | Auto-provisioning extensions from a range      | todo              |
| P13 | Salesforce Open CTI                            | done (#331)       |
| P14 | Pipedrive                                      | done (#329)       |
| P15 | Zoho CRM                                       | done (#329)       |
| P16 | Zendesk, Freshdesk                             | done (#328)       |
| P17 | Slack, n8n, Make                               | done (#335)       |
| P18 | Outlook/Google contacts lookup, calendar DND   | done (#335)       |
| P19 | Dynamics 365 CIF                               | done (#331)       |
| P20 | ConnectWise, Autotask, HaloPSA, Clio           | done (#328)       |
| P21 | Odoo, Bitrix24                                 | done (#329)       |
| P22 | Teams Direct Routing guide (shared with WR 13) | done (#326)       |

## messaging-channels

| #     | Item                                                | Status            |
| ----- | --------------------------------------------------- | ----------------- |
| 1     | Inbox core                                          | done (#211)       |
| 2     | Twilio sending                                      | done (#305)       |
| 3     | Desk-phone texting and SIP-trunk channel into inbox | done (#211, #307) |
| 4     | LAN chat widget                                     | done (#309)       |
| 5     | Message routing and ACD                             | done (#311)       |
| 6     | Telnyx, sender registration                         | done (#346)       |
| W     | Wiring between items 2 to 6, one schedule           | wip (#352)        |
| 7     | AI triage, drafts, auto-reply                       | todo              |
| 8     | Integrations, reporting, DSAR export, redact        | todo              |
| N1-N3 | Public listener, WhatsApp, polling                  | not now           |

## clients-webrtc-mobile-video

| #   | Item                                        | Status                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| --- | ------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | Media foundation (pion)                     | done (#324, #341)                                                                                                                                                                                                                                                                                                                                                                                                                           |
| 2   | WSS on Kamailio                             | done (#326)                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| 2b  | Kamailio to hello-sip TLS hop (S-12)        | done (#340)                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| 2c  | Browsers on kw: WSS, TLS hop, WebRTC media  | done (#334)                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| 3   | Web devices and credentials                 | partial (#282, #343): the Diagnostics "Clients" tab, `GET /api/v1/webrtc/status` and the S-52 client metrics (`hello_client_registrations`, `hello_client_auth_failures_total`, `hello_webrtc_legs`, `hello_webrtc_ice_state_total`, `hello_webrtc_dtls_handshake_seconds`, `hello_webrtc_dtls_failures_total`, `hello_srtp_auth_failures_total`, `hello_webrtc_candidate_type_total`, `hello_turn_credentials_issued_total`) are not built |
| 4   | coturn, ICE servers                         | done (#282, #334)                                                                                                                                                                                                                                                                                                                                                                                                                           |
| 5   | Web softphone, audio                        | done (#343)                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| 6   | Web video                                   | done (#343)                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| 6b  | Video recording                             | done (#344)                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| 10  | Video phones and door stations              | done (#344)                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| 11  | Conference-room video                       | done (#344)                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| 13  | Teams SBC-peer trunk profile, docs/teams.md | done (#326)                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| 7-9 | Push gateway, mobile apps, mobile video     | not now                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| 12  | Public reach                                | not now                                                                                                                                                                                                                                                                                                                                                                                                                                     |

## flow-canvas

| #   | Item                                                      | Status            |
| --- | --------------------------------------------------------- | ----------------- |
| P2  | Read-only canvas, canvases, layout API                    | done (#191)       |
| P3  | Batch backend                                             | done (#214)       |
| P4  | Editing UI                                                | done (#227)       |
| P5  | traceFlow, trace mode                                     | done (#224)       |
| P6  | AI on the canvas                                          | done (#280)       |
| P7  | New node types: batch ops, trace, override badge          | done (#277)       |
| D3  | Every call output accepts every target kind               | done (#196)       |
| -   | Routes form `conference` destination chooser              | done (#198)       |
| -   | Canvas usability: Add everything, Arrange, open on create | done (#233, #241) |

## kamailio-ha

| #   | Item                                                                             | Status                        |
| --- | -------------------------------------------------------------------------------- | ----------------------------- |
| -   | Choose option A or B, VIP IP (A, 192.168.10.109)                                 | done (#207)                   |
| 1-4 | Second instance, VIP, re-point phones, chart/docs (VIP go-live on kw on request) | done (#201, #207, #215, #216) |

## Competitor gaps (spec pbx-gaps, #199)

| Item                                              | Status      |
| ------------------------------------------------- | ----------- |
| Backup and restore (A2)                           | done (#219) |
| Follow-me (B1)                                    | done (#272) |
| Blocklist, anonymous reject, country limits (A1)  | done (#210) |
| Queue callback (B4)                               | done (#237) |
| 2FA (A3)                                          | done (#209) |
| Dial-by-name (B3)                                 | done (#229) |
| Voicemail groups (A6)                             | done (#220) |
| Fax (B5, B6)                                      | done (#273) |
| Boss/secretary (B2)                               | done (#236) |
| Multi-language prompts, Dutch option per box (A5) | done (#208) |
| Codec in call history                             | done (#206) |
| Phone LDAP directory (A7)                         | done (#223) |

## Other

| Item                                                                                                      | Status             |
| --------------------------------------------------------------------------------------------------------- | ------------------ |
| Deploy main to kw (#203, #213, #214, #218, #221, #227, #231; flow canvas editing, batch backend)          | done (sha-abdf747) |
| MCP skills, JSON bodies                                                                                   | done (#189)        |
| Ring-group voicemail box                                                                                  | done (#194)        |
| Transfer-to-voicemail phone key                                                                           | done (#200)        |
| Vendor template keys left out (Poly caps/pickup, record keys, Fanvil CA, Snom/Yealink EXP, pickup values) | done (#264)        |
| Competitor report into the repo (docs)                                                                    | done (#199)        |
| Voicemail box after an extension forward stage uses the forwarded target                                  | done (#213)        |
| Ring groups form: `announcement` failure kind chooser                                                     | done (#213)        |
| Integration flakes on main: TestFlowCanvasAPI teardown, TestMessageDeliveredOnce                          | done (#212)        |
| Messages claimed by another node fail permanently on edge-proxy 403 when the peer set is briefly lost     | done (#217)        |
| CI: kind-upgrade job fails on transient kind API/RBAC errors                                              | done (#218)        |
| Integration flakes: queue HA BYE, takeover voicemail, queue timeout, conference two nodes, MinIO bucket   | done (#221)        |
| Remove MOH test queue 8888 (when the user is done with it)                                                | todo               |
| Call heartbeat leak, trunk slot race, Opus decoder rebuild                                                | done (#243)        |
| Phone resync/reboot: answer digest challenges in the phone realm                                          | done (#257)        |
| Data race on homed calls (ivrHomeCallee)                                                                  | done (#267)        |
| Opus codec stack pin (decoder failures, TestOpusSessionLoopback)                                          | done (#269)        |
| HT813 FXO disconnect supervision                                                                          | done (#244)        |
| Onboarding sends the adopted extension id as a number                                                     | done (#256)        |

## Waiting on hardware or outside accounts

- Vendor redirect live test (SRAPS, RPS/YMCS, GDMS credentials)
- Real carrier trunk check; load test window
- Phone checks: Grandstream P4428, P22650, P2325; WP822 PTT; BLF lamps; voicemail audio and DTMF; key sync and messaging keys on Poly/Fanvil/Snom/Yealink; vendor phonebook keys; every "(verify)" template key
- WP836 text message retest after reboot
- Parity lab items 1-7 and 14
- Hello MCP "Connected apps" end-to-end (after #189)
- Real-account lab lists for integrations, messaging, clients (after those phases)
- Salesforce (Developer Edition) and Dynamics 365 (trial) live checks: docs/crm.md, "Lab checklist: Salesforce" and "Lab checklist: Dynamics 365"
- CRM connector live checks on real accounts: Pipedrive, Zoho CRM, Odoo, Bitrix24 (docs/crm.md lab checklists)
- Telnyx and Twilio accounts, messaging item 6 (docs/inbox.md, "Sending through Telnyx", "Sender registration"): a real Telnyx send, delivery receipt and the 40300 STOP block; the field names of Telnyx's 10DLC, campaign-assignment and toll-free reads and of Twilio's UsAppToPerson and Tollfree reads; what a limited key answers for each registration read; a Twilio account with several Messaging Services; a US long code and a toll-free number in each state (approved, pending, rejected, unregistered)
- Clients 5 and 6: a real browser (Chrome, Edge, Firefox, Safari) calling a G.711 desk phone, a G.722 phone and another browser on the LAN; H.264 and VP8 between browsers with and without hardware encoding and the REMB fallback's quality; DTLS re-handshake and ICE restart on a re-INVITE; a Playwright run with Chromium fake media devices through the lab stack; video capacity per node (the number behind `HELLO_VIDEO_MAX_SESSIONS`)
- Clients 6b, 10 and 11: a real video desk phone and a real door station (H.264 on RTP/AVP) calling each other, a browser and a conference room, with the picture checked on both ends, the keyframe wait on a speaker switch and REMB behaviour; MP4 (H.264) and WebM (VP8) recordings playing in Chrome, Edge, Firefox and Safari with the video slaved to the audio; the real storage per hour of a typical video call (the number behind the 200 GB quota and the 30-day retention); a node killed during a video recording on kw (the sweeper and finalizer); the vendor video preview keys of `phone-feature-parity` S-49
- Teams Direct Routing run (item 13, P22): a Microsoft 365 tenant with Teams Phone, a certified SBC or DRaaS contract, and a public SBC name and certificate; the cmdlet steps in docs/teams.md are unverified until then
- WSS in a browser through the kw Ingress (items 2 and 2c): the kw manifests came with #334: a real browser registering at wss://hello.kw.watteel.lab/sip and calling a desk phone, a `hello_tls` entry in Kamailio's metrics, and whether Kamailio sees ingress-nginx's pod address (10.42.x) so its X-Forwarded-For is believed
