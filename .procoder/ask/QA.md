# Questions procoder cannot answer for you

Written 2026-10-02 21:48 UTC.

Answer each one by writing a line beginning `Answer: ` under it, then
hand the file back with `procoder ask --file .procoder/ask/QA.md`.
Leave the `Key:` lines alone — they are what ties an answer to its question.

## Q1: [decision] decisions.md

Key: a29492463871
Question: Phase 3 production SIP load balancer

- Kamailio dispatcher as the SIP-aware balancer, shipped and configured in deploy/
- A Hello-built Go balancer (hello-lb) shipped as a production component
- Envoy UDP proxy (L4) with active health checks

Answer: Kamailio dispatcher as the SIP-aware balancer, shipped and configured in deploy/ (the user requires a production-grade balancer, not lab tooling)
