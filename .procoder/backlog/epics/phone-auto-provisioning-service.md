# Phone auto-provisioning service

Status: open
Created: 2026-10-05
Milestone: phone-auto-provisioning

## Description

Zero-touch phone setup at scale, using the industry-standard flow: phones discover a provisioning URL (DHCP option 66/160/43, or a vendor redirect service such as Yealink RPS / Poly ZTP / Grandstream GDMS), then fetch a common per-model file plus a per-MAC file over HTTPS. hello-control generates those files from Hello's database (device SIP credentials, server address, line keys/BLF from presence) through per-vendor templates. Administrators bind MAC → extension in the UI/API, or bulk-import a CSV. Security: HTTPS only (no TFTP for files carrying secrets), per-device authentication (vendor factory certificates where supported, else per-device credentials), and no device secret ever logged. Spec: .procoder/specs/phone-auto-provisioning-service.md (draft until its four open questions are answered; they are recorded in .procoder/ask/decisions.md). Plan: .procoder/plans/phone-auto-provisioning-service.md. Stories were added one per acceptance criterion with `procoder backlog story`, because `backlog seed` only takes a COMPLETE spec; once the open questions are answered, re-check them against the amended criteria.
