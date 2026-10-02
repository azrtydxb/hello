# Lessons — findings that escaped our own gates

One entry per finding caught downstream (bot review, human review,
production) — the escape is the bug; the finding is its symptom. Every
entry names which layer should have caught it and the adaptation that now
does. `procoder lessons` flags entries with no adaptation.

Entry shape (unindented in real entries):

    ## <date> <where caught> — <one-line finding>

    - Class: mechanical | judgment | taste
    - Missed by: linter | rubric | controller | test | ci
    - Adaptation: <the concrete change that catches this class from now on>

== then register the commit template (once per clone):
git config commit.template .procoder/github/COMMIT_TEMPLATE.md

## 2026-10-02 PR #1 Copilot — a malformed DSN's parse error was echoed at startup; pgx's password redaction is best effort

- Class: mechanical
- Missed by: rubric
- Adaptation: REVIEW.md: an error built from a secret-bearing value is replaced with a static message naming the key, never wrapped.

## 2026-10-02 PR #1 Copilot — HELLO_VALKEY_ADDR was checked for presence only, so a malformed address left a node unready forever

- Class: mechanical
- Missed by: test
- Adaptation: REVIEW.md: every new config key gets a syntax check and a TestLoad* case for its malformed form, not only its absence.

## 2026-10-02 PR #1 Copilot — the graceful-shutdown test never held a request in flight, so the drain guarantee was untested

- Class: judgment
- Missed by: test
- Adaptation: REVIEW.md: a guarantee stated in a comment needs a test that creates its condition (here: a request in flight across shutdown).

## 2026-10-02 PR #2 Copilot — digest accepted a reused (nonce, cnonce, nc), so a captured REGISTER could be replayed to hijack a binding

- Class: judgment
- Missed by: rubric
- Adaptation: REVIEW.md: authentication paths are checked for replay — what stops a captured credential from being sent again?

## 2026-10-02 PR #2 Copilot — log redaction missed folded Authorization continuation lines and hflow tokens

- Class: mechanical
- Missed by: test
- Adaptation: REVIEW.md: redaction tests cover folded headers and every credential-bearing parameter, not only the first line.

## 2026-10-02 PR #2 Copilot — hello-control readiness ignored Valkey although the live views depend on it

- Class: judgment
- Missed by: rubric
- Adaptation: REVIEW.md: every dependency a service gains is classified as required or optional in readiness, with metrics and a test either way.

## 2026-10-02 PR #2 Copilot — HELLO_SESSION_TTL=0 passed validation and made every login expire at once

- Class: mechanical
- Missed by: test
- Adaptation: REVIEW.md: duration settings get a zero/negative case in their TestLoad* test.

## 2026-10-02 PR #2 Copilot — PutBinding pipelined HSET and HPEXPIRE non-atomically, so a binding could exist without a TTL

- Class: mechanical
- Missed by: rubric
- Adaptation: REVIEW.md: multi-command state changes that must hold together (value plus expiry, check plus set) run as one script or transaction.

## 2026-10-02 PR #2 Copilot — an in-flight heartbeat could republish an ended call after its delete

- Class: judgment
- Missed by: rubric
- Adaptation: REVIEW.md: a background goroutine that writes shared state is ordered against the final delete (joined, or serialized with an ended flag).

## 2026-10-02 PR #2 Copilot — the failed-auth throttle decided from a pre-increment read, so concurrent attempts slipped past the limit

- Class: judgment
- Missed by: test
- Adaptation: REVIEW.md: limits are enforced from the atomic operation's own result, with a concurrent test.
