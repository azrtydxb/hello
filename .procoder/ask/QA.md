# Questions procoder cannot answer for you

Written 2026-10-02 08:14 UTC.

Answer each one by writing a line beginning `Answer: ` under it, then
hand the file back with `procoder ask --file .procoder/ask/QA.md`.
Leave the `Key:` lines alone — they are what ties an answer to its question.

## Q1: [decision] decisions.md

Key: 17469bfde72e
Question: Phase 1 automated SIP testing

- Go test user agents built on sipgo, run in CI
- SIPp scenarios in a container

Answer: Go test user agents built on sipgo, run in CI

## Q2: [decision] decisions.md

Key: 4852be1d915c
Question: Phase 1 call model

- B2BUA (signaling only, SDP passed through, media direct)
- Stateful record-routing proxy

Answer: B2BUA (signaling only, SDP passed through, media direct)

## Q3: [decision] decisions.md

Key: 8efe37eb2d42
Question: Phase 1 management API auth

- Local users with sessions and API tokens, no roles yet
- Single bootstrap admin token from env

Answer: Local users with sessions and API tokens, no roles yet

## Q4: [decision] decisions.md

Key: e776054b2093
Question: Phase 1 registration store

- Valkey from Phase 1 (cluster-wide, HA-ready)
- In-memory per node until Phase 3

Answer: Valkey from Phase 1 (cluster-wide, HA-ready)
