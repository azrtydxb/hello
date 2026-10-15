# `TestKindRollingUpgrade` in test/kindupgrade, run by the kind-upgrade job in .github/workflows/ci.yaml, passes: fails if the call placed before helm upgrade is lost, cannot be hung up, or leaves a zombie, or (if kind cannot run) the PR states exactly why the job is dispatch-only.

Status: done 2026-10-15
Created: 2026-10-14
Epic: kubernetes
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

As an operator I need proof that a Helm upgrade keeps a live call, so that I can upgrade Hello without a maintenance window. Done: a CI job upgrades a kind install while a call is up and the call survives.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestKindRollingUpgrade` in test/kindupgrade, run by the kind-upgrade job in .github/workflows/ci.yaml, passes: fails if the call placed before helm upgrade is lost, cannot be hung up, or leaves a zombie, or (if kind cannot run) the PR states exactly why the job is dispatch-only.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- `TestKindRollingUpgrade` (test/kindupgrade) passed in the CI job kind-upgrade: https://github.com/azrtydxb/hello/actions/runs/38671536846/job/116215639158. It installed the chart on a kind cluster, connected a call through Kamailio, ran helm upgrade with a changed image tag (the call moved from hello-sip-0 to hello-sip-1 and was marked taken-over, never absent from the live view), then rolled Kamailio, and hung the call up with an answered CDR. Limit found and documented: on the pod network a replaced Kamailio loses far-side requests of established calls.
