# Trunk calls reach internal numbers

Status: open
Created: 2026-10-09

## Goal

Following the pivot (2026-10-09): the consuming product connects to Hello as an IP-authenticated SIP trunk and dials internal extensions by number — one `internal` inbound-route destination looks the normalised Request-URI user up in the extension namespace, a default-deny per-trunk `internal_dialing` policy (off / patterns like `1XX` / all) gates what each trunk may dial, loop guards stop a trunk call from leaving through its own trunk, and a caller-ID ladder (received → trunk default → `<trunk-name>`) keeps trunk-to-internal calls attributable on the phones. Seeded from `.procoder/specs/trunk-internal-numbers.md`.
