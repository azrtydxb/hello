# Ring tags and distinctive ringing

A ring tag names the kind of call that is ringing an internal phone: a colleague
(`internal`), a customer on a trunk (`external`), a ring group, a page, a door,
an alarm. Hello sends the tag to the phone in an `Alert-Info` header, and the
phone maps it to a tone, so a door rings differently from a customer. They are
managed under **Ringing** in the console (tabs Tags, Rules, Ring map and Test a
call), through `/api/v1/ring-tags`, `/api/v1/ring-rules` and `/api/v1/ring-map`,
and through the matching MCP tools.

This page covers the tags, the rules, the header Hello sends and the ring map
that tells each vendor's phones which tone to play for a tag.

## Tags

There are seven built-in tags:

| Tag                  | Bellcore slot | Used for                                                        |
| -------------------- | ------------- | --------------------------------------------------------------- |
| `internal`           | 1             | a call from another extension                                   |
| `external`           | 2             | a call that entered on a trunk                                  |
| `ringgroup`          | 3             | a call through a ring group                                     |
| `page`               | 1             | a page or intercom call                                         |
| `doorbell`           | 4             | a door station call                                             |
| `alarm`              | 5             | an alarm call                                                   |
| `emergency-callback` | 1             | a callback to an extension that just called an emergency number |

You can add up to 32 custom tags. A name is 1 to 32 characters of `a-z`, `0-9`
and `-`. A built-in tag cannot be renamed or deleted; you can change its
description and its Bellcore slot. A tag that a rule or a ring group names cannot
be deleted.

The **Bellcore slot** (1 to 5) is the `N` in the `Bellcore-drN` URL of the header
below. Phones that can only map the five Bellcore distinctive rings use it; give
two tags the same slot only when they should ring the same on those phones.

## Which tag a call gets

On every INVITE Hello sends to an internal phone it chooses one tag. The first
match wins, in this order:

1. **The call's own kind**: a page or intercom call is `page`, an alarm call is
   `alarm`, a door call is `doorbell`, a callback within 30 minutes of an
   emergency call is `emergency-callback`.
2. **Your rules**, in the order shown (at most 100).
3. **The ring group's tag**, when the call came through a ring group: the group's
   own **Ring tag** setting (Ring groups page), or `ringgroup` when it has none.
4. `external`, for a call that entered on a trunk.
5. `internal`, for everything else.

The choice is made from the configuration snapshot every SIP node holds, so it
adds no database read to call setup.

### Rules

A rule has an optional name, conditions and a tag. Every condition that is set
must match; at least one must be set.

- **Caller number pattern**: an exact number, an X pattern (`971XXXXXXXXX`) or a
  regular expression, in the dial-plan pattern syntax of routes and trunks. A
  leading `+` is matched either way.
- **Dialled number pattern**: the same syntax, for the number the call was sent
  to (the DID of an inbound call).
- **Inbound trunk**: the trunk's name.
- **Caller kind**: an extension, a trunk or a door station.
- **Time window**: weekly ranges in a time zone, written like a schedule's open
  hours. A range whose end is before its start runs past midnight.

Save replaces the whole list; the order on the page is the order of evaluation,
and the arrows move a rule up or down.

**Test a call** asks Hello which tag a described call would get and shows the
header it would send, using the saved tags and rules. The same question is
`GET /api/v1/ring-rules/evaluate`.

## The header Hello sends

On a leg to an internal phone Hello first drops any `Alert-Info` the caller sent
(a phone or a trunk cannot choose its own ring tag, and cannot ask a phone to
auto-answer), then adds exactly one:

```
Alert-Info: <http://127.0.0.1/Bellcore-dr4>;info=hello-doorbell
```

The `info` text, `hello-<tag>`, serves phones that match on text; the Bellcore
URL serves phones that only map `Bellcore-dr1` to `Bellcore-dr5`. Where the
phone's vendor is known from its registration `User-Agent`, a different form can
replace it. Today that is Snom, which gets `info=alert-internal`,
`alert-external` or `alert-group` for the `internal`, `external` and `ringgroup`
tags (the three classes its settings name) and the form above for the rest.

Page and intercom legs keep the auto-answer headers of
[paging and intercom](provisioning.md#paging-and-intercom), which take
precedence. A leg to a trunk carries no `Alert-Info`.

## Where the tag shows up

- The call-flow trace of the call (the call history detail) has a line such as
  `Ring tag vip (rule:1 boss)`. The text in brackets says what chose the tag:
  `kind:<kind>`, `rule:<position> <name>`, `group`, `trunk` or `default`.
- The call record (CDR) carries `ringTag` and `ringRule`, empty for a call that
  rang no internal phone.
- The metric `hello_ring_tag_total{tag}` counts calls that rang an internal phone
  by tag.

## The ring map

The ring map says which tone a phone plays for each tag. Hello writes it into
the provisioned configuration of every phone, so nothing is set on the phone by
hand: for each tag the phone gets a rule that matches the tag's `Alert-Info`
text (`hello-<tag>`; Snom's three named classes; the `Bellcore-drN` URL on a
Grandstream WP8x6) and the tone to play.

Open the **Ring map** tab on the Ringing page. The columns are the five
vendors (Yealink, Poly, Grandstream, Snom, Fanvil) and the rows are the tags.
Each cell is a list of that vendor's built-in ringers; **Default** is the
ringer of the tag's Bellcore slot, so a new install rings distinctively without
any choice. Poly and Snom also offer **Silent** (visual only).

- **All phones** is the global map. Pick a **phone group** in _Applies to_ to
  override it for the phones of that group; a cell left on _Same as all phones_
  follows the global choice. Saving an empty group list removes the overrides.
- The line's own ring tone (Yealink `account.1.ringtone.ring_type`, Snom
  `ring_sound`, Fanvil `SIP1 Ring Type`, Grandstream WP8xx `P104`, Poly
  `se.rt.default.ringer`) follows the **internal** tag's tone.
- A change reaches the phones through the automatic resync: Hello notices that
  a phone's configuration changed and sends it a check-sync, limited to the
  push rate (see [provisioning.md](provisioning.md)).
- A tag the map names cannot be deleted until the choice is removed.

How many tags a phone can map is a limit of the phone: Yealink 10, Fanvil 10,
Grandstream WP810/820/822/825 10, Poly 23, Snom 3 (internal, external and ring
group only) and Grandstream WP8x6 5 (the Bellcore slots, so tags that share a
slot ring the same; the page warns). Past the limit the first tags in the list
(the built-in tags first, then custom tags by name) are written. A Grandstream
WP856 and the GRP/GXP desk phones get no ring map until their key is confirmed
on hardware. A tag whose name is the start of another tag's (`door`,
`doorbell`) is written after it, so the phone's match cannot take the wrong one.

Custom tones from the tone library and the Grandstream ring tone format are not
produced yet; only the vendors' built-in ringers can be chosen. Every key and
value is from the vendor's administrator guide and has not been checked on a
phone yet; check one phone of each vendor before relying on it.

## API

| Operation                                 | What it does                                      |
| ----------------------------------------- | ------------------------------------------------- |
| `GET /api/v1/ring-tags`                   | the tags, with how many rules and groups use each |
| `POST /api/v1/ring-tags`                  | add a custom tag                                  |
| `GET/PUT/DELETE /api/v1/ring-tags/{name}` | read, change slot and description, delete         |
| `GET /api/v1/ring-rules`                  | the ordered rules                                 |
| `PUT /api/v1/ring-rules`                  | replace the ordered rules                         |
| `GET /api/v1/ring-rules/evaluate`         | which tag a described call would get              |
| `ringTag` on `/api/v1/ring-groups`        | a group's own tag; empty keeps `ringgroup`        |
| `GET /api/v1/ring-map`                    | the ringer lists, the defaults and the choices    |
| `PUT /api/v1/ring-map`                    | replace the global choices                        |
| `PUT /api/v1/ring-map/groups/{id}`        | replace a phone group's overrides                 |

Reads need the read scope and the viewer role; changes need the write scope and
the operator role.
