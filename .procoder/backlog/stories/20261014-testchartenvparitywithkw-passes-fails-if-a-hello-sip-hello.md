# `TestChartEnvParityWithKw` passes: fails if a hello-sip, hello-control or Kamailio environment variable in the kw manifests is missing from the chart's render (other than a documented kw-only one).

Status: done 2026-10-15
Created: 2026-10-14
Epic: kubernetes
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

As an operator installing Hello on Kubernetes I need the chart and guide to hold the promise this test names, so that a later change cannot silently break it. Done: the test passes in CI against the rendered chart.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestChartEnvParityWithKw` passes: fails if a hello-sip, hello-control or Kamailio environment variable in the kw manifests is missing from the chart's render (other than a documented kw-only one).

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- `TestChartEnvParityWithKw` (test/deploy/helm_test.go) passed in the CI job chart, which installs helm and kubeconform and sets HELLO_REQUIRE_HELM=1: https://github.com/azrtydxb/hello/actions/runs/38671536846/job/116215638964. Also run locally: HELLO_REQUIRE_HELM=1 go test ./test/deploy -run 'TestChart|TestKubernetesGuide'.
- Per-condition audit 2026-10-15: `TestChartEnvParityWithKw` is in test/deploy and fails when any env var of the kw hello-sip, hello-control (and migrate), Kamailio or hello-ui containers is missing from the chart, except the eight HELLO_AI_* tuning vars listed with reasons in `kwOnlyEnv` (confirmed by deleting HELLO_MEDIA_FORCE_ANCHOR from the chart).
