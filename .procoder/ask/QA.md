# Questions procoder cannot answer for you

Written 2026-10-05 06:07 UTC.

Answer each one by writing a line beginning `Answer: ` under it, then
hand the file back with `procoder ask --file .procoder/ask/QA.md`.
Leave the `Key:` lines alone — they are what ties an answer to its question.

## Q1: [decision] decisions.md

Key: 5dc2499014b1
Question: Phase 7 in-call HA scope

- In-call HA for anchored calls only; NAT'd direct-media calls stay best-effort (documented limitation)
- Always-on anchoring for every call so all calls get in-call HA (reverses the Phase 5 conditional decision)
- Defer Phase 7; stop at Phase 6 scope

Answer: Anchor everything + full HA (all calls anchor, LAN-to-LAN included; reverses Phase 5 conditional anchoring deliberately)

## Q2: [decision] decisions.md

Key: 011dba9bcb59
Question: Phase 7 in-call HA scope (after the anchored-vs-direct explanation)

- In-call HA for anchored calls only; NAT'd direct-media calls stay best-effort
- Always-on anchoring: every call anchored so all calls get in-call HA
- In-call HA for anchored calls + policy change: LAN-to-LAN calls also anchor (cheap on a LAN, makes every call survivable)
- Defer Phase 7; stop at the current scope

Answer: Anchor everything + full HA (all calls anchor, LAN-to-LAN included; reverses Phase 5 conditional anchoring deliberately)
