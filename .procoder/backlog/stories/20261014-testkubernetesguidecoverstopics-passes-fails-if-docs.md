# `TestKubernetesGuideCoversTopics` passes: fails if docs/kubernetes.md lacks any of hostNetwork, NodePort, the RTP range, anti-affinity, CloudNativePG, Sentinel or the upgrade procedure.

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

- [x] `TestKubernetesGuideCoversTopics` passes: fails if docs/kubernetes.md lacks any of hostNetwork, NodePort, the RTP range, anti-affinity, CloudNativePG, Sentinel or the upgrade procedure.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- `TestKubernetesGuideCoversTopics` (test/deploy/helm_test.go) passed in the CI job chart, which installs helm and kubeconform and sets HELLO_REQUIRE_HELM=1: https://github.com/azrtydxb/hello/actions/runs/38671536846/job/116215638964. Also run locally: HELLO_REQUIRE_HELM=1 go test ./test/deploy -run 'TestChart|TestKubernetesGuide'.
