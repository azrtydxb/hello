# Phone auto-provisioning service

Status: open
Created: 2026-10-05
Milestone: phone-auto-provisioning
Spec: phone-auto-provisioning-service @ 38d6a327ab40

## Description

Zero-touch phone setup at scale, using the industry-standard flow: phones discover a provisioning URL (DHCP option 66/160/43, or a vendor redirect service such as Yealink RPS / Poly ZTP / Grandstream GDMS), then fetch a common per-model file plus a per-MAC file over HTTPS. hello-control generates those files from Hello's database (device SIP credentials, server address, line keys/BLF from presence) through per-vendor templates. Administrators bind MAC → extension in the UI/API, or bulk-import a CSV. Security, as specified: HTTPS for anything carrying secrets (no TFTP), a per-device token in the URL path plus a MAC allowlist, trust on first use for DHCP-discovered phones, rate limiting, a fetch audit, token rotation, and no secret ever logged.

Seeded from .procoder/specs/phone-auto-provisioning-service.md — one story per acceptance criterion.
