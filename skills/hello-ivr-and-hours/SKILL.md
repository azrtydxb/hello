---
name: hello-ivr-and-hours
description: Build the front door of a Kuvryn Hello PBX through its MCP server - IVR / auto-attendant menus, opening hours (schedules, holiday calendars, time conditions with a manual open/closed override), announcements and music on hold - and prove the result with the flow graph, the time-condition evaluator and the routing tester. Use when someone asks for "press 1 for sales", a menu that changes after hours or on holidays, to close the office early today, to upload or replace a recorded greeting, or to change the hold music.
license: Apache-2.0
metadata:
  hello-version: "v1"
---

# Hello IVR, opening hours, announcements and hold music

A caller reaches an IVR menu (or a time condition) from an inbound route, a
ring group's failover, a feature code or another menu. Every piece below is a
saved object that names the next one, so build from the leaves inward:

1. announcements (greeting, prompts) and music on hold classes,
2. calendars (holidays), then schedules (weekly hours) that consult them,
3. IVR menus and queues/ring groups that are destinations,
4. time conditions that pick a destination from a schedule,
5. an inbound route (`createInboundRoute`, see the `hello-routing` skill)
   whose `destinationKind` is `ivr`, `time_condition` or `queue` and whose
   `destination` is the name of the first object of the chain.

## Connecting

- MCP server: `https://<hello host>/mcp` (on kw: `https://hello.kw.watteel.lab/mcp`).
- Scopes: `read` to inspect, `write` to change. Role `operator` for everything
  here. An IVR menu or time condition with a paging destination needs `admin`.
- Tools are named after operations; see [Tools](references/tools.md).

## The rule: read the flow, change, read the flow

1. `getFlowGraph` (or `getFlowGraph` with `scope` `closure:<nodeId>`, for
   example `closure:inboundRoute:3`) before you change anything, to see what
   already reaches the object you are about to touch. Note `getFlowRevision`.
2. Make the change.
3. Read the graph again. A destination that does not exist shows as a
   `missing` node with a `dangling_reference` warning: fix that before you
   finish.
4. Prove a call with `testRouting` (caller `trunk:<id>`, the DID) and, for
   hours, `evaluateTimeCondition`.

## Announcements

Announcements are WAV files that greetings, prompts and hold music play.

- `listAnnouncements` first; names are unique (`A-Z a-z 0-9 . _ -`, 64 max).
- `createAnnouncement` with `{ "name": "welcome", "audioBase64": "<base64 WAV>" }`.
  The audio is limited to 700 KiB decoded because an MCP request is at most
  1 MiB: about 40 seconds of 8 kHz 16-bit mono or 20 seconds at 16 kHz. It must
  be an uncompressed PCM or float WAV (8, 16, 24 or 32 bit, mono or stereo);
  stereo is mixed to mono. If the user's file is longer, tell them to upload it
  in the console (the multipart upload there takes 64 MiB).
- `replaceAnnouncement` swaps the audio of an existing announcement under the
  same name (`{ "audioBase64": "..." }`). Calls already playing finish with the
  old audio.
- `deleteAnnouncement` is refused (409) while an IVR menu plays or names it.
- `saveAnnouncementPrompt` stores the authored text, voice and model of a
  generated prompt without spending credits. Generating audio from text
  spends paid credits and is done in the console only; an agent never
  generates it.

## Music on hold

`createMOHClass` with `{ "name": "lobby", "mode": "loop" | "shuffle",
"announcementIds": [..] , "isDefault": false }`. A class with no
announcements plays the built-in tone. `putMOHClass` replaces the whole class
including the playlist. Extensions, ring groups, queues and inbound routes
that name a deleted class fall back to the default class
(`listMOHClasses`, `getMOHClass`, `deleteMOHClass`).

## Calendars, schedules and time

1. `getTimeSettings` returns the time zone a new schedule gets when you name
   none. Pass `timeZone` (an IANA name such as `Europe/Brussels`) whenever
   the office is not in that zone.
2. `createCalendar` with entries: single dates, ranges (`start`, `end`),
   `yearly` dates (`12-25` style), all-day or part-of-day (`startTime`,
   `endTime`), each `closed` or `open` with its own `hours`.
   For a list of holidays use `importCalendar` with `{ "text": "2026-12-25 Christmas\n12-26 yearly Boxing day\n2026-12-24 13:00-17:00 Christmas eve", "replace": false }`
   or an iCalendar file as text; nothing is stored when any line is bad and
   the errors name the lines.
3. `createSchedule`: `ranges` of `{day, start, end}` (day 0 is Sunday to 6 Saturday, `HH:MM` 24-hour, `24:00` ends the day;
   several per day allowed; an end before the start crosses midnight) and
   `calendarIds`. Overlapping ranges on a day are refused.
4. `putSchedule` and `putCalendar` replace the whole object: read it with
   `getSchedule` or `getCalendar`, change it, send all of it back.
5. `deleteSchedule` is refused while a time condition uses it,
   `deleteCalendar` while a schedule consults it.

## Time conditions

`createTimeCondition`: a `scheduleId`, an `open` destination and a `closed`
destination, optionally a `holiday` destination and a `toggleNumber`.
Destinations are `{ "kind": ..., "target": ... }` with kind one of hangup,
extension, ring_group, voicemail, announcement, paging, ivr, external, queue or
time_condition (a chain that loops back is refused).

- `evaluateTimeCondition` answers what it does now, or at `at` (RFC 3339 or
  `YYYY-MM-DDTHH:MM` in the schedule's zone): the branch, why, whether the
  override forced it and the final destination. Test an ordinary weekday, a
  weekend and a holiday before you finish. `ignoreOverride=true` shows the
  schedule alone.
- `setTimeConditionOverride` with `auto`, `open` or `closed`: the answer to
  "close the office early today". Always set it back to `auto` afterwards, and
  tell the user it is persistent until then.
- `listTimeConditions`, `getTimeCondition`, `putTimeCondition` (the override
  is kept) and `deleteTimeCondition` (refused while a route, ring group,
  feature code, IVR menu or time condition names it).

## IVR menus

`createIVRMenu`:

```json
{
  "name": "main",
  "number": "800",
  "greetingId": 4,
  "digitTimeout": 5,
  "maxRetries": 2,
  "allowDirectDial": false,
  "options": [
    { "digit": "1", "kind": "queue", "target": "sales" },
    { "digit": "2", "kind": "ring_group", "target": "support" },
    { "digit": "0", "kind": "extension", "target": "100" }
  ],
  "timeout": { "kind": "extension", "target": "100" },
  "invalid": { "kind": "announcement", "target": "sorry" }
}
```

- `greetingId` is an announcement id from `listAnnouncements`; digits are
  unique; referenced announcements and destinations must exist; the automatic
  exits (`timeout`, `invalid`) must not loop back to the menu.
- Change it with `putIVRMenu` (whole object, options included; a menu that
  something names cannot be renamed) and remove it with `deleteIVRMenu`
  (409 while a route, ring group, feature code or menu names it).
- `listIVRMenus` and `getIVRMenu` to read.

## Recipes

### Office hours with a holiday greeting

1. `createAnnouncement` "closed" and "holiday".
2. `createCalendar` with the holidays; `createSchedule` Monday to Friday
   08:00-12:00 and 13:00-17:00 with that calendar.
3. `createIVRMenu` "main" for the open branch.
4. `createTimeCondition` with `open` the IVR, `closed` the "closed"
   announcement, `holiday` the "holiday" announcement.
5. `createInboundRoute` for the DID with `destinationKind` `time_condition`
   and `destination` the condition's name (see `hello-routing`).
6. `evaluateTimeCondition` at a Tuesday 10:00, a Saturday and 25 December;
   `testRouting` for the DID; `getFlowGraph` to confirm no dangling node.

### Close early today

`setTimeConditionOverride` `closed`, confirm with `evaluateTimeCondition`,
and remind the user to set `auto` tomorrow (offer to do it).

## References

- [Tools](references/tools.md) - every tool with its scope.
- [Failures](references/failures.md) - what the refusals mean.
