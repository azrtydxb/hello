# Unbuilt work tracker

Source: the 2026-10-26 audit of every spec, decisions.md and the conversation against origin/main. Every row ends as `done (#PR)` or `hw` (waits only on real hardware or an outside account). Status values: `todo`, `wip (#PR or branch)`, `done (#PR)`, `blocked (reason)`, `hw`, `not now` (user decision).

## phone-feature-parity

| #   | Item                                                                          | Status                      |
| --- | ----------------------------------------------------------------------------- | --------------------------- |
| 9   | Session timers, CRLF keep-alive, dead-media teardown, roaming relearn (S-9)   | wip (parity-session-timers) |
| 10  | Ring tags, Alert-Info rules, CDR ring_tag (S-39..41)                          | wip (ring-tags)             |
| 11  | Ring map provisioning, Ringing page (S-42, S-44)                              | todo (after 10)             |
| 12  | Tone library (S-43)                                                           | todo (after 11)             |
| 13  | Door station extension kind, Door group (S-45, S-46)                          | todo (after 10)             |
| 14  | Door-open DTMF `*50` (S-47)                                                   | todo (after 13)             |
| 15  | Setting overrides and WP extras (S-12)                                        | todo                        |
| 16  | Doorbell webhook (S-48)                                                       | todo (after 13)             |
| 17  | Door video preview (S-49)                                                     | todo (code), hw (test)      |
| 18  | vCard phonebook slot (S-31), partial                                          | todo                        |
| 19  | Firmware minimum versions, WP defaults, drift badge, upgrade window (S-26)    | todo                        |
| 20  | Lab sweep `TestWPPhonesAsVendors` (S-38)                                      | todo                        |
| 21  | Multiple lines and account sharing (S-10, S-11)                               | todo                        |
| 22  | SIP over TCP: provisioning the transport, hello-sip side (S-13), partial      | todo                        |
| 23  | SIP over TLS and sips (S-14)                                                  | wip (parity-tls)            |
| 24  | SRTP SDES (S-15)                                                              | todo (after 23)             |
| 25  | DTLS-SRTP and ICE-lite (S-16)                                                 | todo (after 24)             |
| 26  | PRACK/100rel, anonymous outbound (Privacy), partial                           | todo                        |
| 27  | Provisioning hardening (S-22, S-32), TR-069 off etc. (explicitly in spec)     | todo                        |
| 28  | DSCP marking (S-21)                                                           | todo                        |
| 29  | Phone backup/secondary servers (S-20), partial                                | todo                        |
| 30  | HELD, Geolocation, locations, callback tag, alarm `*55` (S-28, S-29), partial | todo                        |
| 31  | Wi-Fi profiles and roaming (S-23, S-24)                                       | todo                        |
| 34  | Presence: reg event, PUBLISH, eventlist (S-35)                                | todo                        |
| 35  | RTCP-XR collector (S-36)                                                      | todo                        |
| 36  | Syslog sink (S-27)                                                            | todo                        |
| 37  | SCEP and 802.1X (S-25)                                                        | todo                        |

## integrations

| #   | Item                                           | Status                |
| --- | ---------------------------------------------- | --------------------- |
| P1  | Call events: stream, integration_events, SSE   | wip (integrations-p1) |
| P2  | Webhooks: signing, retries, DLQ                | todo                  |
| P3  | Click-to-call: originate API, /dial, tel:      | todo                  |
| P4  | Recording links and lookup API                 | todo                  |
| P5  | Browser extension (MV3), fixed signing key     | todo                  |
| P6  | HubSpot and internal/crm framework             | todo                  |
| P7  | Zapier                                         | todo                  |
| P8  | Directory engine, AD/LDAP                      | todo                  |
| P9  | Google Workspace and Entra connectors          | todo                  |
| P10 | SCIM 2.0                                       | todo                  |
| P11 | OIDC SSO, LDAP bind login                      | todo                  |
| P12 | Auto-provisioning extensions from a range      | todo                  |
| P13 | Salesforce Open CTI                            | todo                  |
| P14 | Pipedrive                                      | todo                  |
| P15 | Zoho CRM                                       | todo                  |
| P16 | Zendesk, Freshdesk                             | todo                  |
| P17 | Slack, n8n, Make                               | todo                  |
| P18 | Outlook/Google contacts lookup, calendar DND   | todo                  |
| P19 | Dynamics 365 CIF                               | todo                  |
| P20 | ConnectWise, Autotask, HaloPSA, Clio           | todo                  |
| P21 | Odoo, Bitrix24                                 | todo                  |
| P22 | Teams Direct Routing guide (shared with WR 13) | todo                  |

## messaging-channels

| #     | Item                                                         | Status                |
| ----- | ------------------------------------------------------------ | --------------------- |
| 1     | Inbox core                                                   | wip (messaging-inbox) |
| 2     | Twilio sending                                               | todo                  |
| 3     | Desk-phone texting and SIP-trunk channel into inbox, partial | todo                  |
| 4     | LAN chat widget                                              | todo                  |
| 5     | Message routing and ACD                                      | todo                  |
| 6     | Telnyx, sender registration                                  | todo                  |
| 7     | AI triage, drafts, auto-reply                                | todo                  |
| 8     | Integrations, reporting, DSAR export, redact                 | todo                  |
| N1-N3 | Public listener, WhatsApp, polling                           | not now               |

## clients-webrtc-mobile-video

| #   | Item                                        | Status                       |
| --- | ------------------------------------------- | ---------------------------- |
| 1   | Media foundation (pion)                     | blocked (parity 23-25)       |
| 2   | WSS on Kamailio                             | blocked (parity 23)          |
| 3   | Web devices and credentials                 | todo                         |
| 4   | coturn, ICE servers                         | todo                         |
| 5   | Web softphone, audio                        | blocked (1-4)                |
| 6   | Web video                                   | blocked (5)                  |
| 6b  | Video recording                             | blocked (6)                  |
| 10  | Video phones and door stations              | todo (code), hw (test)       |
| 11  | Conference-room video                       | blocked (6)                  |
| 13  | Teams SBC-peer trunk profile, docs/teams.md | todo                         |
| 7-9 | Push gateway, mobile apps, mobile video     | not now                      |
| 12  | Public reach                                | not now (user decides later) |

## flow-canvas

| #   | Item                                             | Status                  |
| --- | ------------------------------------------------ | ----------------------- |
| P2  | Read-only canvas, canvases, layout API           | wip (#191)              |
| P3  | Batch backend                                    | todo (after P2)         |
| P4  | Editing UI                                       | wip (#PR)               |
| P5  | traceFlow, trace mode                            | todo (after P3)         |
| P6  | AI on the canvas                                 | todo (after P4)         |
| P7  | New node types: batch ops, trace, override badge | todo (after P5)         |
| D3  | Every call output accepts every target kind      | wip (#196)              |
| -   | Routes form `conference` destination chooser     | wip (routes-conference) |

## kamailio-ha

| #   | Item                                              | Status                   |
| --- | ------------------------------------------------- | ------------------------ |
| -   | Choose option A or B, VIP IP                      | done (A, 192.168.10.109) |
| 1-4 | Second instance, VIP, re-point phones, chart/docs | wip (kamailio-ha)        |

## Competitor gaps (chosen 2026-10-26; spec first)

| Item                                          | Status                         |
| --------------------------------------------- | ------------------------------ |
| Backup and restore                            | wip (spec #199)                |
| Follow-me                                     | wip (spec #199)                |
| Blocklist, anonymous reject, country limits   | wip (spec #199)                |
| Queue callback                                | wip (spec #199)                |
| 2FA                                           | wip (#209)                     |
| Dial-by-name                                  | wip (spec #199)                |
| Voicemail groups                              | wip (spec #199)                |
| Fax                                           | wip (spec #199)                |
| Boss/secretary                                | wip (spec #199)                |
| Multi-language prompts (Dutch option per box) | wip (spec #199)                |
| Codec in call history                         | wip (spec pbx-gaps; cdr-codec) |
| Phone LDAP directory                          | wip (spec #199)                |

## Other

| Item                                                                                                      | Status                |
| --------------------------------------------------------------------------------------------------------- | --------------------- |
| Deploy main to kw (#187, #188, #190, #192, #193, #194, #195, #197)                                        | done (sha-542ef38)    |
| MCP skills, JSON bodies (#189)                                                                            | wip (#189)            |
| Ring-group voicemail box (#194, deploy pending)                                                           | done (#194)           |
| Transfer-to-voicemail phone key                                                                           | wip (transfer-vm-key) |
| Vendor template keys left out (Poly caps/pickup, record keys, Fanvil CA, Snom/Yealink EXP, pickup values) | todo                  |
| Competitor report into the repo (docs)                                                                    | wip (spec-pbx-gaps)   |
| Remove MOH test queue 8888                                                                                | todo (when user done) |

## Waiting on hardware or outside accounts

- Vendor redirect live test (SRAPS, RPS/YMCS, GDMS credentials)
- Real carrier trunk check; load test window
- Phone checks: Grandstream P4428, P22650, P2325; WP822 PTT; BLF lamps; voicemail audio and DTMF; key sync and messaging keys on Poly/Fanvil/Snom/Yealink; vendor phonebook keys; every "(verify)" template key
- WP836 text message retest after reboot
- Parity lab items 1-7 and 14
- Hello MCP "Connected apps" end-to-end (after #189)
- Real-account lab lists for integrations, messaging, clients (after those phases)
