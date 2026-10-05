# Phone auto-provisioning

Status: open
Created: 2026-10-05

## Goal

A phone plugged into the network configures itself: an administrator enters (or imports by CSV) its MAC address and extension, and on boot the phone finds Hello's provisioning service, downloads its SIP account, line keys and BLF over HTTPS, and registers — no hand-typed credentials. Scheduled after Phase 7 (in-call HA).
