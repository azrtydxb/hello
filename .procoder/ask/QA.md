# Questions procoder cannot answer for you

Written 2026-10-02 11:13 UTC.

Answer each one by writing a line beginning `Answer: ` under it, then
hand the file back with `procoder ask --file .procoder/ask/QA.md`.
Leave the `Key:` lines alone — they are what ties an answer to its question.

## Q1: [decision] decisions.md

Key: 2de9213b4811
Question: Phase 2 media to carriers

- Direct media (phone to carrier) as the roadmap says; anchoring waits for Phase 5
- Pull a minimal media relay forward into Phase 2

Answer: Direct media (phone to carrier) as the roadmap says; anchoring waits for Phase 5

## Q2: [decision] decisions.md

Key: 70635c04fd67
Question: Phase 2 number rewriting syntax

- Regex match plus replacement template with capture groups, plus simple strip/prefix fields
- Only strip-N-digits and prefix fields

Answer: Regex match plus replacement template with capture groups, plus simple strip/prefix fields

## Q3: [decision] decisions.md

Key: 406a5e21d741
Question: Phase 2 trunk registration ownership

- One node registers each trunk at a time (Valkey lease, another node takes over on expiry)
- Every node registers the trunk (several contacts at the carrier)

Answer: One node registers each trunk at a time (Valkey lease, another node takes over on expiry)

## Q4: [decision] decisions.md

Key: 06e6d3e2170c
Question: Phase 2 trunk testing

- Simulated carrier container in the lab, plus a manual check against a real trunk
- Simulated carrier only

Answer: Simulated carrier container in the lab, plus a manual check against a real trunk
