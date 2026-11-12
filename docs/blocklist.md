# Blocklist, anonymous reject and country restriction

Hello can turn unwanted callers away before any phone rings. Three controls
work on **inbound calls from a trunk**; calls between your own extensions and
calls from phones are never screened.

| Control                   | Where                     | Scope             |
| ------------------------- | ------------------------- | ----------------- |
| Blocklist (block / allow) | **Call flow → Blocklist** | all trunks or one |
| Reject anonymous callers  | trunk form                | that trunk        |
| Country restriction       | trunk form                | that trunk        |

A screened call is answered with the trunk's reject code and is never forked
or rung. The default is `603 Decline`; a trunk can answer `486 Busy Here`
instead. Nothing is configured by default: a trunk with no settings and an
empty blocklist behaves as before.

## The order of checks

For each inbound trunk call, in this order:

1. An **allow** entry that matches the caller lets the call through and skips
   every check below.
2. **Reject anonymous** (if the trunk has it on).
3. A **block** entry that matches the caller.
4. The trunk's **country restriction** (if it has a country list).
5. Routing, as usual.

An allow entry always beats a block entry. Among entries of the same action,
an exact number is more specific than a prefix, and a longer prefix than a
shorter one, which is the entry the call's trace names.

## Blocklist entries

An entry is a number or a number prefix, an action (`block` or `allow`), a
scope (every trunk, or one trunk), an optional reason and an optional
announcement.

- Numbers are written in any usual form (`+41 22 555 00 00`,
  `0041225550000`, `022 555 00 00`) and stored as one normalised key, using the
  same rules as the address books: `HELLO_DIAL_INTL_PREFIX` (default `00`)
  turns a dialled international prefix into `+`, and `HELLO_DIAL_COUNTRY_CODE`
  (for example `41`) turns a national number with the trunk prefix `0` into an
  international one. Set it, or national spellings stay national and will not
  match international callers.
- A **prefix** such as `+44` matches every caller whose number starts with it.
  It may be shorter than a whole number.
- A number needs at least four digits (shorter ones are service and feature
  codes). Anonymous cannot be put on the list; use the trunk's anonymous reject.
- The **reason** (up to 120 characters) is a note for administrators. Other
  roles never see it.
- With an **announcement**, a blocked caller hears it and the call then ends,
  instead of being rejected at once. The call is answered for that, so use it
  sparingly.
- A disabled entry is kept but not applied.
- The list holds up to 10,000 entries, and the same number can be listed once
  per scope.

A blocked call's call record has the failure reason `blocked: blocklist` (or
`blocked: anonymous`, `blocked: country`). **Call history** has a **Blocked** tab for it (the API filter is
`GET /api/v1/cdrs?disposition=blocked`, also on the CSV export). The metric
`hello_blocked_calls_total{reason}` counts them by reason; the audit log
records entry ids, never numbers.

### Import from a CSV

**Import CSV** takes a file with a header row. Only `number` is required:

```
number,action,match,scope,reason
+44*,block,,,UK spam
+41 22 555 00 00,allow,exact,,our bank
0900*,block,prefix,carrier-a,premium numbers
```

- `action` is `block` (default) or `allow`.
- `match` is `exact` (default) or `prefix`; a number ending in `*` is a prefix.
- `scope` is `global` (default) or a trunk name.
- A number already listed for its scope is updated.

**Check** reads every row and reports each problem by row and column without
writing anything. An import applies every row or none. The limits are 2 MiB
and 10,000 rows.

## Anonymous reject

Turn on **Reject anonymous callers** in the trunk form. A caller is anonymous
when the From user is `anonymous` (or `restricted`, `withheld`, `unavailable`,
`unknown`, `private`), when the call carries `Privacy: id`, or when there is no
caller number at all.

An allow entry cannot rescue an anonymous call (its number is unknown), so the
check is the trunk's alone. A phone with a withheld caller ID is never
rejected; this applies only to calls from a trunk.

## Country restriction

List the calling codes callers may come from in **Allowed country codes**
(for example `41, 49`). Leave it empty for no restriction.

The caller's number is normalised as above. An international number must
begin with an allowed code. A number that cannot be placed (a national number
when `HELLO_DIAL_COUNTRY_CODE` is not set, a number with letters, a short
number, or a withheld one) follows **Caller number cannot be placed**: `block`
by default, or `allow`. A call blocked for its country is answered with the
trunk's reject code.

## API

| Method and path                 | Role     | Purpose                                 |
| ------------------------------- | -------- | --------------------------------------- |
| `GET /api/v1/blocklist`         | viewer   | list; `q`, `action`, `scope`, `after`   |
| `POST /api/v1/blocklist`        | operator | add an entry                            |
| `PUT /api/v1/blocklist/{id}`    | operator | replace an entry                        |
| `DELETE /api/v1/blocklist/{id}` | operator | delete an entry                         |
| `POST /api/v1/blocklist/import` | operator | CSV import; `?dry_run=true` checks only |

The trunk settings are the fields `rejectAnonymous`, `allowedCountryCodes`,
`unknownCallerAction` and `rejectCode` of `/api/v1/trunks`. Blocklist and
trunk rules reach every `hello-sip` node as part of the configuration
snapshot; a call never waits for the database.
