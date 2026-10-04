# Questions procoder cannot answer for you

Written 2026-10-04 10:30 UTC.

Answer each one by writing a line beginning `Answer: ` under it, then
hand the file back with `procoder ask --file .procoder/ask/QA.md`.
Leave the `Key:` lines alone — they are what ties an answer to its question.

## Q1: [decision] decisions.md

Key: d8bbc86e5884
Question: Phase 4 ring/hunt group strategies

- All five now (ring-all, sequential, round-robin, longest-idle, weighted)
- Ring-all + sequential first, the rest later

Answer: All five now (ring-all, sequential, round-robin, longest-idle, weighted)

## Q2: [decision] decisions.md

Key: 2ad55e86b114
Question: Phase 4 voicemail audio storage

- S3-compatible object storage (MinIO) in the hello namespace
- PostgreSQL bytea/large objects
- Persistent-volume filesystem

Answer: S3-compatible object storage (MinIO) in the hello namespace

## Q3: [decision] decisions.md

Key: 6fee12142fca
Question: Phase 4 voicemail delivery

- Web + phone (MWI) only; email notification later
- Include SMTP email notification with audio attachment now

Answer: Include SMTP email notification with audio attachment now
