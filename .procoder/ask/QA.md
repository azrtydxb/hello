# Questions procoder cannot answer for you

Written 2026-10-04 17:59 UTC.

Answer each one by writing a line beginning `Answer: ` under it, then
hand the file back with `procoder ask --file .procoder/ask/QA.md`.
Leave the `Key:` lines alone — they are what ties an answer to its question.

## Q1: [decision] decisions.md

Key: 5a10fd5e72c9
Question: Merge Phase 4 (PR #5) and roll out to kw

- Mark PR #5 ready, squash-merge, push images via publish, pin digests, add MinIO+SMTP+feature env to deploy/kuvryn-sync/kw, let Sync roll it out
- Hold PR #5 for review; kw stays on Phase 3 until reviewed

Answer: Merge + roll out to kw (done 2026-10-04: PR #5 squash-merged, images published, rollout commits 9832a84/492055b/290e473)

## Q2: [decision] decisions.md

Key: 10ecd9488531
Question: Phase 5 announcements

- In scope: named announcement sets played on demand (failure destinations, before transfer)
- Out of scope for Phase 5

Answer: In scope: named announcement sets played on demand (failure destinations, before transfer)

## Q3: [decision] decisions.md

Key: 6844d41d1251
Question: Phase 5 media anchoring policy

- Conditional: anchor only when a feature needs it (NAT-detected, recording, announcements); direct RTP otherwise (spec §4/§16)
- Always anchor: all calls traverse the media anchor

Answer: Conditional: anchor only when a feature needs it (NAT-detected, recording, announcements); direct RTP otherwise

## Q4: [decision] decisions.md

Key: 9f3be89ce05e
Question: Phase 5 recording

- On-demand: DTMF (*1) and per-extension API toggle; recordings to MinIO
- Auto-record all calls (compliance style), stored to MinIO

Answer: On-demand: DTMF (*1) and per-extension API toggle; recordings to MinIO
