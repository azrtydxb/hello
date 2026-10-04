# Pre-PR review rubric

A fresh-context reviewer (a subagent, not the author) reads the full
branch diff against this list BEFORE the PR is opened. The author fixes
Critical/Important findings first; downstream reviewers are the fallback,
not the net. Findings name file:line, what breaks, and the fix.

Check every hunk for:

- User-supplied strings reaching a path, command, or query — validated as
  the plain value they claim to be (no separators, no dot-dot, quoted)?
- Error paths: any error swallowed, any unreadable input silently
  skipped, any failure reported as success? Honesty beats convenience.
- State computed twice that must agree (time.Now called twice across a
  boundary, a value re-derived instead of passed).
- Loops doing per-iteration work that belongs outside (regex compilation,
  allocations, file opens).
- Temp files and permissions: CreateTemp over predictable names; modes no
  wider than needed.
- New surface wired everywhere it must appear: dispatch, usage text,
  canonical lists, docs, tests that pin them together.
- Parsers and scanners against hostile shapes: empty input, binary input,
  the terminator variants, the case the happy path skips.
- Test fixtures that trip our own scanners: assemble marker/secret-like
  content at runtime, never as a literal.
- Prose and markdown: code spans unbroken, lists formatted, wording that
  says what the code actually does.

Then, for services and state (added 2026-10-02 from escaped findings in
.procoder/github/LESSONS.md):

- Errors built from secret-bearing values: replaced with a static message
  naming the key, never wrapped or echoed.
- Config: every new key has a syntax check and a test for its malformed,
  zero and negative forms, not only its absence.
- Every guarantee stated in a comment has a test that creates its
  condition (a request in flight, a race, a lost packet).
- Authentication paths: what stops a captured credential being replayed?
- Redaction: folded headers and every credential-bearing parameter.
- Readiness: each dependency is classified required or optional, with
  metrics and a test either way.
- State changes that must hold together (value plus expiry, check plus set)
  run atomically, and limits are enforced from that atomic result.
- Background writers are ordered against the final delete of what they
  write.

Added 2026-10-03 (Phase 2 escapes):

- Runtime environment (time zones, CA certificates, DNS) proven in the shipped
  image by a lab test, not only on the developer machine.
- Shared state that can vanish (outage, restart, TTL) has a recovery path on
  refresh, tested by deleting it mid-life.
- Limits enforced on every path that consumes the resource, each tested.
- Derived state rebuilt for one reason keeps markers set for another; test the
  sequence.
- UI responses applied onto the latest state by id; actions disabled until a
  recovery reload lands.
- Proxies re-resolve upstream names.

Added 2026-10-03 (Phase 3 escapes):

- Operator requests persisted in shared state are withdrawn on every exit
  path; tested with a later exit after the request.
- A state machine's effects are serialised with its transitions.
- Guards over eventually-consistent state count in-flight intent, and
  check-and-write is atomic under concurrency, tested with parallel callers.
- A forced hangup races call setup: the answer path re-checks the abort flag
  under the same lock, tested with an injected delay.
- Edge dependencies are addressed by fixed IPs (or proven resolvable), and
  the missing-member case is exercised.
- Timing bounds in tests are absolute deadlines from the onset the spec
  names; a late success fails.

End with a verdict line: findings counted by severity, or exactly
"Nothing found — open the PR."
