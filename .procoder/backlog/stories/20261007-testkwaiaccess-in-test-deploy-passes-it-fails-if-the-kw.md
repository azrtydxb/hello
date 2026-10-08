# `TestKwAIAccess` in `test/deploy` passes. It fails if the kw manifest lacks the `hello-tls` certificate from `cluster-ca` on the `hello` Ingress, `HELLO_PUBLIC_URL` on hello-control, `HELLO_OAUTH_DCR=true`, or the nginx template lacks the `/mcp`, `/oauth/` and `/.well-known/oauth-` locations.

Status: done 2026-10-08
Created: 2026-10-07
Epic: ai-external-access
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

An MCP client on the LAN reaches Hello at https://hello.kw.watteel.lab/mcp: the hello Ingress terminates TLS with hello-tls from cluster-ca, hello-control runs with HELLO_PUBLIC_URL and DCR, and hello-ui proxies /mcp, /oauth/ and the OAuth well-knowns while serving the unframeable consent page itself (spec S-19; plan Task 7).

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestKwAIAccess` in `test/deploy` passes. It fails if the kw manifest lacks the `hello-tls` certificate from `cluster-ca` on the `hello` Ingress, `HELLO_PUBLIC_URL` on hello-control, `HELLO_OAUTH_DCR=true`, or the nginx template lacks the `/mcp`, `/oauth/` and `/.well-known/oauth-` locations.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

Fingerprint: sha256:7e347cf0eda5e1f11a8dee407c9ec2ea5eade2b2951010716e8175d12df67976
Produced: 113 bytes, exit 0
Command: go test -count=1 -run ^TestKwAIAccess$ -v ./test/deploy/

Mutation-checked: a wrong TLS secret name and frame-ancestors 'self' each fail it.
