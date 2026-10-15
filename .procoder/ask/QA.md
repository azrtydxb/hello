# Questions procoder cannot answer for you

Written 2026-10-15 06:23 UTC.

Answer each one by writing a line beginning `Answer: ` under it, then
hand the file back with `procoder ask --file .procoder/ask/QA.md`.
Leave the `Key:` lines alone — they are what ties an answer to its question.

## Q1: [decision] decisions.md

Key: a01db5934cd6
Question: How to stop the assistant's reasoning overruns (2026-10-14)

The go-ai-sdk v0.6.0 → v0.8.0 commits do not explain the slow answers. The cause is the reasoning model sometimes thinking past Hello's 8192 `max_tokens`: the call ends on length and Hello retries it at double the budget, so the user waits for both attempts (seen: 1m17s, 2m19s; normal 2–17s).

- Limit the model's thinking for the assistant (reasoning effort / thinking budget sent per request), keeping 8192 max_tokens.
- Raise the first-attempt budget to 16384 so an overrun rarely needs a retry (no change to thinking).
- Both.
- Leave as is.

Decision: turn thinking off for Hello's model calls (2026-10-14).

Answer: Turn thinking off for Hello's model calls (user, 2026-10-14).

## Q2: [decision] decisions.md

Key: f50182032be6
Question: Re-verify the fastllm-backed checks after the trunk rollout? (2026-10-14)

The phase-2 built-in agent is live, but the assistant-reply and tool-calling checks never completed because fastllm (vLLM) was down when work paused; the check cron was cancelled.

- Yes: re-run one assistant reply and one tool call through the deployed stack once the trunk rollout lands.
- No: leave the checks unverified until the next fastllm change.

  Decision: yes — verify right after the trunk rollout (2026-10-14).

Answer: Yes, verify right after the trunk rollout (user, 2026-10-14).

## Q3: [decision] decisions.md

Key: feef6f80a909
Question: Thinking off makes the assistant skip its tools (2026-10-14)

Live on kw with thinking off: answers start in ~1s and finish in ~6s (0 reasoning tokens), but the model often says "I will check the registrations" and never calls the tool, so answers miss live data. Likely cause: in prompt structured-output mode the "answer as JSON" instruction is present on every step, and without thinking the model answers instead of calling tools.

- Split the answer: let the model call tools with no JSON instruction, and ask for the structured answer only in a final step. Keeps thinking off.
- Turn thinking back on: reliable tool use, occasional 1–2 min answers.
- Turn thinking back on for the assistant only; keep it off for detector explanations (no tools there).

Decision: split tool calling from the structured answer, thinking stays off (2026-10-14).

Answer: Split tool calling from the structured answer; thinking stays off (user, 2026-10-14).
