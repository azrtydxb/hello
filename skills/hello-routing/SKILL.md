---
name: hello-routing
description: Change how calls flow through a Kuvryn Hello PBX through its MCP server - SIP trunks, outbound and inbound routes, route order, ring groups and DTMF feature codes - and prove each change with the routing tester before and after. Use when someone asks why a number goes where it goes, to add a carrier or trunk, to route a DID to a person or group, to change dialling rules or caller ID, or to set up a ring or hunt group.
license: Apache-2.0
metadata:
  hello-version: "v1"
---

# Hello routing

Every call Hello places is decided by its saved configuration: extensions,
then outbound routes (calls from an extension to anything that is not an
extension) or inbound routes (calls arriving on a trunk). Routes are tried in
position order and the first match wins. The routing tester decides a call
against the saved configuration without placing it, and explains each step.

## Connecting

- MCP server: `https://<hello host>/mcp` (on kw: `https://hello.kw.watteel.lab/mcp`).
- Scopes: `read` to inspect and test, `write` to change. `testRouting` only
  needs `read` (it changes nothing).
- Role: `operator` or `admin` for changes.

## The rule: test, change, test

1. Before any change, run `testRouting` for the call the user cares about and
   keep the decision and trace.
2. Make the change.
3. Run the same `testRouting` again and show the user the before and after.
   Also re-test one call that should not have changed (an emergency number, a
   normal national call) so a broad pattern cannot slip through.

A routing change takes effect for the next call on every SIP node once the
configuration revision moves; nothing needs restarting.

## Workflows

### Why does a number go where it goes?

`testRouting` with the caller (an extension number, or `trunk:<id>` for an
inbound call) and the dialled number. The decision names the kind (internal,
outbound, inbound or reject), the matched route, the trunks in try order and
the rewritten number; the trace says why each earlier route did not match.
Read the trace back to the user in plain words.

### Add a carrier trunk

1. `listTrunks` to see what exists.
2. `createTrunk`: mode registration (Hello registers with a username and
   password) or ip (the carrier sends from known source ranges). Give the
   destinations (host, port, priority, weight). The password is accepted but
   never returned.
3. `listTrunkStatus` until the registration is registered (registration
   mode) and the destinations are up.
4. Nothing uses a new trunk until a route lists it: add or change an
   outbound route next.
5. `geolocation` (default false) makes an emergency call through the trunk
   carry the caller's location (a Geolocation header and a PIDF-LO body, RFC
   6442): the phone's own, else the location assigned under `hello-phones`
   (locations). Turn it on only for a carrier that accepts them; the request is
   larger than a usual SIP datagram.

### Outbound: send a pattern out of a trunk

1. `listOutboundRoutes` and `testRouting` for a sample number.
2. `createOutboundRoute` with a prefix or regex match, the trunk ids in try
   order and, if needed, a number transform (strip, prefix, regex and
   template) and a caller ID transform. New routes are appended last.
3. If it must win over an earlier, broader route, `reorderOutboundRoutes`
   with every id in the new order.
4. Test again.

Emergency routes are marked as such; never reorder or delete one without the
user saying so explicitly.

### Inbound: send a DID to a person, group or number

1. `listInboundRoutes`, `listRingGroups`, `listExtensions`.
2. `createInboundRoute` with the DID match (any, exact, prefix or regex),
   optionally the trunk, and a destination: an extension number, an external
   number, or a sip: URI. A ring group is reached through its extension.
3. `reorderInboundRoutes` if a broader route sits above it.
4. `testRouting` with `trunk:<id>` as the caller and the DID as the number.

### Ring and hunt groups

`createRingGroup` with members and a strategy: ring-all, sequential,
round-robin, longest-idle or weighted; ring timeout, member delay, whether
DND is ignored, and what happens when nobody answers (voicemail, external,
announcement or nothing). `updateRingGroup` replaces the members you send.

### Feature codes

`listFeatureCodes` then `putFeatureCodes` with the whole list (it replaces
every code). Codes are a star and two to four digits, or # and ##.

### Screen callers: the blocklist and a trunk's caller policy

A blocklist entry matches a caller number: `exact`, or `prefix` (a number
ending in `*`). Its `action` is `block` (the default) or `allow`, which lets a
number through an earlier block. Its `scope` is `global` (the default) or one
trunk by name. A blocked call is turned away before routing. The optional
`reason` is a note shown to administrators only.

1. `listBlocklist` to see the entries, and whether the number is already
   listed for that scope.
2. `createBlocklistEntry` with the number, action, match and scope. Sending a
   number that is already listed for its scope updates that entry. Use
   `putBlocklistEntry` with the entry id to change one, and
   `deleteBlocklistEntry` to remove it.
3. For many numbers, `importBlocklist` with `dry_run` true first. It takes
   the rows as a JSON body with the columns number, action, match, scope and
   reason. It is all or nothing: one failing row writes nothing.
4. Anonymous callers and country restrictions belong to the trunk.
   `updateTrunk` sets `rejectAnonymous`, `allowedCountryCodes`,
   `unknownCallerAction` and `rejectCode` (603 or 486) on that trunk.
5. `testRouting` with the number as the caller, and `trunk:<id>` as the
   source, to show the user the decision after the change.

## References

- [Tools for routing](references/tools.md) - every tool with its scope.
- [Worked examples](references/examples.md) - bodies for trunks, routes, groups and tests.
- [Failures](references/failures.md) - what errors and rejections mean.
