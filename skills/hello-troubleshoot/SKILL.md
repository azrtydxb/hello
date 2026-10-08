---
name: hello-troubleshoot
description: Diagnose a Kuvryn Hello PBX through its MCP server - a call that failed (call records and routing trace), a phone that does not register (registrations, device diagnostics, auth-failure blocks), a trunk that is down, and what the cluster state means. Use when someone reports that a call did not go through, a phone shows "no service" or "registration failed", calls to or from a carrier fail, or Hello itself seems unhealthy.
license: Apache-2.0
metadata:
  hello-version: "v1"
---

# Hello troubleshooting

Hello explains its own behaviour: every call leaves a call detail record (CDR)
with a routing trace and a failure explanation, every REGISTER attempt is kept
for a while with its outcome, and every node publishes its state. Read those
first; change configuration only once the cause is clear, and then with the
hello-setup or hello-routing skill.

## Connecting

- MCP server: `https://<hello host>/mcp` (on kw: `https://hello.kw.watteel.lab/mcp`).
- Scopes: `read` covers everything here. The one change this skill may make,
  unblocking an IP with `clearAuthFailures`, needs `write` and the `operator`
  role; ask the user before doing it.
- Resources: the server also offers live state as resources (for example
  `hello://registrations`); read them when your client supports resources.

## Start with the cluster

`getCluster` returns every node, PostgreSQL and Valkey health and the
configuration revision. If PostgreSQL or Valkey is down, or a SIP node is
not READY, that is the first thing to report: most other symptoms follow
from it. See [cluster states](references/cluster.md).

## A call that failed

1. Find it: `listCDRs`, filtered by number and time if the user gave them.
   `countCDRs` tells you whether failures are unusual right now.
2. `getCDR` for the record: the final status, which side ended it, the
   failure reason in plain words, the route and trunk used, and the routing
   trace.
3. If the routing looks wrong, re-run the decision with `testRouting` (same
   caller and number) to see whether the saved configuration still decides
   the same way.
4. Explain the cause in one or two sentences and name the fix: a route
   (hello-routing), a device (hello-setup), or the carrier.

## A phone that does not register

1. `listRegistrations`: is the device registered at all, from which address
   and on which node?
2. `listDevices` to find the device id, then `getDeviceDiagnostics`. It
   returns the recent REGISTER attempts (source, user agent, response code,
   whether credentials were sent) and a verdict that says why it is not
   registered.
3. If the source IP is blocked for too many failed authentications,
   `listAuthFailures` shows it. A wrong password keeps failing after an
   unblock, so fix the credentials first (the administrator rotates the device
   secret in the console, or re-provisions the phone), then
   `clearAuthFailures` for the IP with the user's agreement.
4. A provisioned phone that never fetched its configuration:
   `listPhoneFetches` for the phone. No fetches means DHCP or the vendor
   redirect does not point at Hello (`getProvSettings` has the values).

See [registration verdicts](references/registration.md).

## A trunk that is down

`listTrunkStatus` shows each trunk's registration state (registered,
registering, failed, misconfigured, disabled) with the last response code,
and each destination's health from OPTIONS pings. `getTrunk` shows the
configuration (never the password). See [trunk states](references/trunks.md).

## Answering

Say what you found, what it means and what to do, in that order. Quote the
evidence (the CDR id, the response code, the verdict) so the user can check
it in the console.

## References

- [Tools](references/tools.md) - every tool this skill uses.
- [Cluster states](references/cluster.md)
- [Registration verdicts and SIP codes](references/registration.md)
- [Trunk states](references/trunks.md)
