# AI agent

Status: open
Created: 2026-10-08

## Goal

Phase 2 of the AI integration: Hello has a suggest-only agent of its own. An operator asks the console assistant about the PBX and gets answers grounded in live data, read through Hello's API as them; deterministic detectors find what is wrong (REGISTER floods, failing trunks, flapping nodes, exhausted capacity, broken configuration) and a private model explains and ranks it; every change the agent suggests waits in an inbox as a diff until a human applies it through Hello's own API, with roles and audit unchanged. Phase 3 (voice agents) builds on it.
