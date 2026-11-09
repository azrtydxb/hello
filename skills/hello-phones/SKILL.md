---
name: hello-phones
description: Manage desk phones on a Kuvryn Hello PBX through its MCP server - auto-provisioning (phones, bulk import, onboarding queue, templates, DHCP and vendor redirect), phone groups with address books, multicast paging and push-to-talk, Wi-Fi profiles and roaming for Wi-Fi handsets, firmware pins, resync, and company and named phonebooks with CSV-style import. Use when someone asks to add or move many phones, to adopt phones that just appeared on the network, to give a department its own directory, to push a configuration change to handsets, or to find out why a phone did not pick up its settings.
license: Apache-2.0
metadata:
  hello-version: "v1"
---

# Hello phones, groups and phonebooks

A provisioned phone is bound to an extension and fetches its configuration
from Hello by MAC address. Its phone group decides which address books it
receives and which paging channels it listens on. The first sections are
for desk phones; `hello-setup` covers extensions, SIP devices and a single
phone's first setup.

## Connecting

- MCP server: `https://<hello host>/mcp` (on kw: `https://hello.kw.watteel.lab/mcp`).
- Scopes: `read` to inspect, `write` to change. Phones, templates, firmware,
  onboarding, resync and phonebook entries need `operator`. Phone groups,
  creating or renaming phonebooks and redirect accounts need `admin` (role
  and scope).
- The provisioning URL and its token are shown once in the console; tools
  never return them where they are secret. Tell the administrator to copy
  them there.
- Tools are named after operations; see [Tools](references/tools.md).

## Add phones

### One phone

`createPhone` with `{ "mac": "80:5e:c0:12:34:56", "vendor": "yealink",
"model": "T54W", "extensionId": 8, "label": "Bob's desk", "groupId": 2 }`.
Then `getProvSettings` to tell the user which DHCP option (66 or 43) to
set, or `listRedirectAccounts` to see whether the vendor redirect service
points new phones at Hello.

### Many phones: `importPhones`

1. `listExtensions` so every row names an existing extension number.
2. Dry run first: `importPhones` with `dryRun` `true` and

```json
{
  "rows": [
    {
      "mac": "80:5e:c0:12:34:56",
      "vendor": "yealink",
      "model": "T54W",
      "extension": "101",
      "label": "Alice"
    },
    {
      "mac": "00:15:65:aa:bb:cc",
      "vendor": "yealink",
      "model": "T46U",
      "extension": "102",
      "blf": ["101", "103"]
    }
  ]
}
```

At most 2000 rows. Every row is checked (MAC, vendor, model, extension,
duplicates in the request and against existing phones). The answer has
`ok` and the errors per row; a row's `line` is its position in `rows`. 3. Fix the rows, then run it again with `dryRun` `false`: all rows are
created or none (400 with the same rows).

### Phones that just appeared: onboarding

1. `listOnboardingCandidates`: phones that asked for a configuration in the
   last 30 days and are not adopted.
2. `adoptOnboardingCandidate` (path `mac`) with `{ "extensionNumber": "103",
"label": "Reception" }` (or `extensionId` or `userId`); vendor and model
   are prefilled from what the phone reported.
3. Several at once: `bulkAdoptOnboardingCandidates` with `{ "items": [ { "mac": "..." } ], "startNumber": "110" }`.
   Rows without an extension take consecutive numbers from `startNumber` in
   list order; if any row fails nothing is adopted.
4. A stranger's phone: `dismissOnboardingCandidate` with `{ "ignore": true }`
   puts the MAC on the ignore list (`listOnboardingIgnored`,
   `deleteOnboardingIgnored` to undo).

### Change or remove

`updatePhone` (only the sent fields change: `label`, `extensionId`,
`templateId`, `blf`, `enabled`, `groupId`, `pttChannelId`), `getPhone`,
`listPhones`, `deletePhone` (its SIP device stays).

## Push a change and check it

1. `previewPhoneFile` (query `file`) renders what the phone would get,
   secrets masked, without counting as a fetch. Check it after any template or
   group change.
2. `resyncPhone` queues a check-sync to one phone, `resyncPhoneGroup` to a
   group, `resyncAllPhones` to every bound, enabled phone (ask first: it is
   fleet-wide; it makes phones re-fetch, it does not reboot them).
3. `listPhonePushes` (one phone) or `listLatestPhonePushes` (all) show the
   result: ok, not_registered, timeout, auth_failed or failed.
4. `listPhoneFetches` shows whether and when the phone fetched its config.
   No fetches means DHCP, the redirect service or the network, not Hello.

Rebooting a handset and ringing it are deliberately console-only.

## Templates and firmware

- Templates: `listProvTemplates`, `getProvTemplate` (id, or a built-in's
  `builtinRef`), `copyProvTemplate` to start from a built-in, then
  `validateProvTemplate` (parses and renders, saves nothing),
  `createProvTemplate` or `updateProvTemplate` (the previous version is
  kept), `deleteProvTemplate`. After a change, `previewPhoneFile` and a resync.
- Firmware: `listFirmware`, `putFirmwarePins` (replaces every pin, so read
  the current pins from `listFirmware` and send them all back with your
  change), `deleteFirmware`. Uploading an image is a console action: images
  are tens of megabytes, far over the 1 MiB limit of an MCP request, so there is
  no tool for it. Ask the administrator to upload it in the console, then pin it.
- Vendor redirect accounts (admin): `listRedirectAccounts`,
  `checkRedirectAccount` to test the stored credentials, `putRedirectAccount`
  and `deleteRedirectAccount`. Credentials are never returned; have the
  administrator type them in the console rather than pasting them into chat.

## Phone groups (admin)

A group delivers address books and multicast settings to its phones.

1. `createPhoneGroup`: `{ "name": "warehouse", "includeExtensions": true,
"includeCompany": true }`.
2. Put phones in it with `updatePhone` `groupId`.
3. `setPhoneGroupBooks` with `{ "bookIds": [4, 5] }`: up to 5 named books,
   delivered after the extensions and company books.
4. `setPhoneGroupMulticast`: `{ "channels": [ { "channelId": 3, "role":
"both" } ], "options": { "allowInDnd": false } }` for multicast paging
   (channels come from `listMulticastChannels`, see `hello-call-handling`).
5. `setPhoneGroupPTT`: push-to-talk and group paging for Grandstream WP
   handsets (channels 1 to 25 PTT, 26 to 50 group paging).
6. `resyncPhoneGroup`, then `listLatestPhonePushes`.

`listPhoneGroups`, `getPhoneGroup`, `updatePhoneGroup` (replaces name and
switches; the named books stay), `deletePhoneGroup` (phones fall back to no
group).

## Phonebooks

Books: the computed extensions book (read-only), the company book and up to
100 named books. Personal address books of users are never reachable here.

- `listPhonebooks`, `getPhonebook`; `createPhonebook`, `updatePhonebook`,
  `deletePhonebook` need `admin` (the company and extensions books cannot be
  deleted).
- Entries: `listPhonebookEntries` (paged by `cursor`), `createPhonebookEntry`
  with `{ "firstName": "Ann", "lastName": "Lee", "numbers": [ { "label":
"work", "number": "+3212345678" } ] }`, `updatePhonebookEntry` (replaces the
  entry), `deletePhonebookEntry`. At most 5000 entries per book.
- Bulk: `importPhonebook` with `dry_run` `true`, then `false`, and `mode`
  `append` or `replace` (replace deletes the book's entries first; ask
  first). Body: `{ "rows": [ { "first_name": "Ann", "last_name": "Lee",
"work": "+3212345678", "mobile": "" } ] }`, columns first_name, last_name,
  company, work, mobile, home, other, note; at most 5000 rows and 2 MiB. It is
  all or nothing: with failing rows nothing is written and the answer lists
  them (a row's number is its position in `rows` plus one).
- `lookupPhonebook` (admin) with a `number` (and optionally `callee`) shows
  which entry would name that caller, in the order extensions, named books,
  company. Use it to explain why a name shows or does not.
- Deliver a named book by adding it to a group with `setPhoneGroupBooks`,
  then resync.

## Ring tags

A ring tag names a ring tone (Alert-Info) that a phone plays for a call. The
built-in tags come with the server; custom tags are yours to add.

- `listRingTags`, `getRingTag`; `createRingTag` and `putRingTag` (path `name`)
  make a custom tag; `deleteRingTag` removes a custom one only.
- `listRingRules` and `putRingRules` (replaces all rules) decide which tag a
  call gets: by kind, by rule order, or the default.
- `evaluateRingRules` (`/ring-rules/evaluate`) answers which tag a given call
  would get. Use it to explain a ring before changing a rule.
- A ring group can name its own tag (`ringTag`); the rules can still override it.

## Ring map

The ring map turns a ring tag into a tone on each vendor's phones.

- `getRingMap` (`/ring-map`) shows the vendors' ringer lists, the default tone
  of every tag and vendor, the global choices and each phone group's overrides.
- `putRingMap` (`/ring-map`) replaces the global choices as a whole. The tone
  must be in that vendor's ringer list (silent only where the vendor has it).
  The change reaches the phones through the automatic resync.
- `putRingMapGroup` (`/ring-map/groups/{id}`) replaces one phone group's
  overrides; an empty list removes them. An override wins over the global choice
  for that group's phones.

## Wi-Fi handsets

A Grandstream Wi-Fi handset (WP810, WP822, WP825, WP816, WP826, WP836, WP856)
joins the networks Hello gives it. Other models and vendors get no Wi-Fi keys.

- A profile is a named network: `createWifiProfile` with `name`, `ssid` and
  `security` (`open`, `wpa2-psk`, `wpa3-sae`, `owe`, `wpa2-enterprise`,
  `wpa3-enterprise`), `psk` for a pre-shared key or `identity` and `password`
  for 802.1X (PEAP), plus `priority` (0 to 10), `hidden` and `preferAccount`.
  The key and password are write-only: `listWifiProfiles` and
  `getWifiProfile` show only `pskSet` and `passwordSet`. Never ask for or
  repeat them in chat; tell the administrator to type them in the console if
  they cannot pass them as tool arguments.
- `updateWifiProfile` (PATCH) keeps a secret that is left out and clears one
  sent empty. `deleteWifiProfile` is refused while a group or phone uses it.
- Assign with `putPhoneGroupWifi` (`/phone-groups/{id}/wifi`, at most 10
  profiles, replaces the whole list) or `putPhoneWifi` (`/phones/{id}/wifi`).
  A phone's profile with the same SSID replaces its group's.
  `getPhoneGroupWifi` and `getPhoneWifi` show the assignment.
- Safety gate: a bad Wi-Fi push strands a handset, so nothing is written
  until one profile of the phone's set has `verified: true`, set after it
  joined a test phone. `pushing` in the assignment says whether the gate is
  open. Walk the administrator through it: assign one profile to a test
  phone, resync it, confirm it joins, set `verified`, then assign the group.
- `putPhoneGroupWifi` also takes `radio`: `band`, `roamMode`, `threshold`
  (-100 to -30 dBm), `poorScan`, `goodScan`, `gain`, `dot11r`, `dot11v` and
  `prompt`. A field left out is not written. Each model takes only the keys its
  template has; 802.11k has no key anywhere and is set on the access points.
- A WP8x6 holds 10 networks, a WP810/822/825 17 and a WP856 one (the best
  priority it can take). Open hardware checks are in `docs/provisioning.md`.

## References

- [Tools](references/tools.md) - every tool with its scope.
- [Failures](references/failures.md) - what the refusals mean.
