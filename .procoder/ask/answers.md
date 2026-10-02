# What a human decided

Written 2026-10-02 08:14 UTC. procoder reads this
file to avoid asking a question twice; edit an answer here to change what
it believes. Reword the question and it will be asked again.

## [decision] decisions.md

Key: 07cc11272355
Question: Migration library

- goose
- golang-migrate

Answer: goose

## [decision] decisions.md

Key: 17469bfde72e
Question: Phase 1 automated SIP testing

- Go test user agents built on sipgo, run in CI
- SIPp scenarios in a container

Answer: Go test user agents built on sipgo, run in CI

## [decision] decisions.md

Key: 1fc2454e9927
Question: UI package manager

- npm
- pnpm

Answer: pnpm

## (no longer asked)

Key: 2a0645102517
Question: OPEN: UI package manager — npm or pnpm?

Answer: pnpm

## [decision] decisions.md

Key: 34cff7a7f99a
Question: Merge PR #1

- Squash-merge now and start the Phase 1 spec
- Hold for your own review

Answer: Squash-merge now and start the Phase 1 spec

## [decision] decisions.md

Key: 4852be1d915c
Question: Phase 1 call model

- B2BUA (signaling only, SDP passed through, media direct)
- Stateful record-routing proxy

Answer: B2BUA (signaling only, SDP passed through, media direct)

## [decision] decisions.md

Key: 4c87c8814e53
Question: First build scope

- Phase 0 only (foundation), then a separate Phase 1 spec
- Phase 0 + Phase 1 in one milestone

Answer: all — build the full roadmap (Phases 0-7) in phase order; Phase 0 first, one spec + milestone per phase.

## (no longer asked)

Key: 5470505a44c0
Question: OPEN: Scope of the first build — Phase 0 only, or Phase 0 plus Phase 1 (SIP/UDP REGISTER, digest auth, internal calls between two phones) in the same milestone?

Answer: all — build the full roadmap (Phases 0-7) in phase order; Phase 0 first, one spec + milestone per phase.

## (no longer asked)

Key: 61fa8fc32239
Question: OPEN: Migration library — goose, or golang-migrate?

Answer: goose

## [decision] decisions.md

Key: 8efe37eb2d42
Question: Phase 1 management API auth

- Local users with sessions and API tokens, no roles yet
- Single bootstrap admin token from env

Answer: Local users with sessions and API tokens, no roles yet

## [decision] decisions.md

Key: 8f478deba27c
Question: Phase 0 delivery

- Commit on branch phase-0-foundation and open a PR to main
- Commit on the branch only, no PR yet
- Hold — leave uncommitted for review

Answer: Commit on branch phase-0-foundation and open a PR to main

## [decision] decisions.md

Key: e776054b2093
Question: Phase 1 registration store

- Valkey from Phase 1 (cluster-wide, HA-ready)
- In-memory per node until Phase 3

Answer: Valkey from Phase 1 (cluster-wide, HA-ready)
