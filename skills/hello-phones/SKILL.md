---
name: hello-phones
description: Manage desk phones on a Kuvryn Hello PBX through its MCP server - auto-provisioning (phones, bulk import, onboarding queue, templates, DHCP and vendor redirect), phone groups with address books, multicast paging and push-to-talk, firmware pins, resync, and company and named phonebooks with CSV-style import. Use when someone asks to add or move many phones, to adopt phones that just appeared on the network, to give a department its own directory, to push a configuration change to handsets, or to find out why a phone did not pick up its settings.
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

## References

- [Tools](references/tools.md) - every tool with its scope.
- [Failures](references/failures.md) - what the refusals mean.
