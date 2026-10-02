# Questions procoder cannot answer for you

Written 2026-10-02 10:26 UTC.

Answer each one by writing a line beginning `Answer: ` under it, then
hand the file back with `procoder ask --file .procoder/ask/QA.md`.
Leave the `Key:` lines alone — they are what ties an answer to its question.

## Q1: [decision] decisions.md

Key: 1a17adfb2c72
Question: hello-control readiness during a Valkey outage

- Ready stays green; live views return 503 and /readyz reports Valkey as degraded
- /readyz fails while Valkey is down (all management goes out of rotation)

Answer: Ready stays green; live views return 503 and /readyz reports Valkey as degraded
