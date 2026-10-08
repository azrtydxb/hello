# AI agent

Status: open
Created: 2026-10-08
Milestone: ai-agent
Spec: ai-agent (draft; open questions 1-3 pending)

## Description

The in-product AI agent over a configurable private LLM endpoint (OpenAI-compatible or Anthropic, through go-ai-sdk): one bounded AI service (privacy guard, limits, daily budget, validator-retried structured output, untrusted-data blocks), a console assistant that reads through allowlisted read-only tools replayed in-process as the user and answers with a validated proposal, proposals (allowlisted OpenAPI operations, schema-validated, before/after diff, staleness, dedupe) applied or dismissed by an operator through the API with their own credentials, AIOps detectors with a findings lifecycle and model explanations, the console pages and status page, metrics, retention, kw configuration and docs.

Stories follow the spec's acceptance criteria, one each, written while the spec is still draft (procoder backlog seed refuses a spec with open questions); reseed or amend them when the open questions are answered.
