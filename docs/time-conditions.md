# Time and date routing

Time conditions send a call to one place during business hours, another after
hours and a third on holidays. They are managed under **Time conditions** in
the console (tabs Time conditions, Schedules and Calendars), through
`/api/v1/time-conditions`, `/api/v1/schedules` and `/api/v1/calendars`, and
through the matching MCP tools.

The pieces:

- A **schedule** holds the weekly open hours in a time zone.
- A **calendar** holds holidays and exceptions. A schedule consults the
  calendars it references.
- A **time condition** is the routable object: a schedule, a destination when
  open, a destination when closed and, optionally, a separate destination for
  holidays. It also carries the manual day/night override.

## Schedules

A schedule has a name, an IANA time zone and open-hours ranges per weekday.
Several ranges per day are allowed (09:00-12:00 and 13:00-17:00), and they must
not overlap. A range whose end is before its start crosses midnight: Friday
22:00-06:00 stays open until 06:00 on Saturday. `24:00` is the end of the day
(`00:00-24:00` is the whole day).

The time zone defaults to the phones' time zone setting (`HELLO_PROV_TIMEZONE`,
UTC if unset); `GET /api/v1/time-settings` returns it. Ranges and calendar dates
are wall clock in the schedule's zone, so daylight saving needs no special
handling: an hour skipped in spring simply is not there, and a repeated hour in
autumn is inside the range both times.

## Calendars (dates)

A calendar entry is a single date or a range of dates (the end is the last
day), optionally yearly, covering whole days or part of each day. Its mode is:

- `closed` (the default): the covered time is closed. A time condition treats it
  as a holiday and uses the holiday destination.
- `open`: the weekly hours of the covered days are replaced by the entry's own
  hours (Christmas Eve 09:00-13:00, a shopping Sunday 10:00-18:00). Outside those
  hours the day also counts as a holiday.

Yearly entries use `MM-DD` and repeat every year; a range may wrap the new year
(`12-31` to `01-02`). A closed entry beats an open one for the same day, and
holidays beat the weekly schedule.

### Importing dates

`POST /api/v1/calendars/{id}/import` (the Import button in the console) reads a
list of dates, either one entry per line or an iCalendar file (a public-holiday
`.ics`). Nothing is stored when any line is bad; the errors name the lines.

```
# date or range, optional words, then the label
2026-12-25 Christmas Day
2026-12-24..2026-12-26 Christmas break
07-21 National day                         # no year: yearly
2026-06-02 14:00-15:00 Staff meeting        # part of the day
2026-11-29 open=10:00-13:00,14:00-18:00 Shopping Sunday
2026-05-01 yearly Labour Day
```

With `replace` the calendar's entries are replaced, otherwise the new ones are
added. A calendar holds up to 1000 entries.

## Time conditions

A destination is one of the IVR destination kinds: extension, ring group,
voicemail box, announcement, IVR menu, paging group, external number, call
queue, hang up and another time condition. An announcement can name a voicemail
box to take the call after it plays ("after hours: announcement, then voicemail").

Where a time condition can be used:

- an **inbound route** (destination kind `time_condition`);
- an **IVR** option, or its timeout / invalid-choice exit;
- the **failover** of a ring group, or of a call queue (timeout, full, no agents);
- a **feature code** with the action `time_condition` (the argument names it).

The example: inbound route -> time condition -> open: ring group `sales`;
closed: announcement `closed-msg`, then the voicemail of extension 200;
holiday: announcement `holiday-msg`.

A time condition cannot lead back to itself, directly or through other time
conditions; the API refuses it (400). A loop through a different feature (a
ring group failing over into the condition that rang it) is caught at call time:
the call is rejected with 482 and counted as `loop`.

A time condition, schedule or calendar that something references cannot be
deleted (409), and a time condition that something names cannot be renamed.

## The day/night override

Each time condition is `auto` (follow the schedule), `open` (force the open
destination) or `closed` (force the closed destination). A forced state beats the
schedule and the holidays. Set it:

- in the console (the segmented toggle on the time condition);
- through `PUT /api/v1/time-conditions/{id}/override` with `{"override":"closed"}`;
- from a phone with the feature code **`*28<n>`**, where `<n>` is the time
  condition's _toggle number_ (1-4 digits, set on the time condition). `*28`
  is the default `tc_override` code (argument `toggle`); the argument can also
  be `open`, `closed` or `auto` for a code that always sets that state (add such
  a code under Feature codes with the action `tc_override`, for example `*29`
  with argument `closed`, and dial `*29<n>`). `toggle` returns a forced condition
  to automatic and forces an automatic one to the opposite of what the schedule
  says right now. The phone hears the confirmation tone of a 200.

The override is stored in the database, in the same revision as the rest of
the configuration, so it survives a restart and an HA failover. Every change is
audited (resource `time_condition`, action `override`).

### BLF on the toggle

A line key set to `*28<n>` as a BLF (Event: dialog) is accepted. The lamp is off
while the schedule decides (`auto`) and lit while a manual override is in force
(`open` or `closed`). It follows changes from the console and API as well as
from the phone. Lamps need a phone that subscribes to a non-extension number;
this has not been tried on real hardware yet.

## What happened, and what would happen

- Every decision is written to the call's trace in the CDR, for example
  `Time condition office: closed (outside the weekly hours on Sunday) -> announcement closed-msg then voicemail 200`,
  and logged at INFO (`time condition decided`, with the branch, reason and
  destination).
- `hello_time_condition_total{result}` counts decisions: `open`, `closed`,
  `holiday`, `forced_open`, `forced_closed`, `loop`, `missing` and `toggled`.
- `GET /api/v1/time-conditions/{id}/evaluate?at=...` answers what the time
  condition would do now or at another time (`at` is RFC 3339, or
  `YYYY-MM-DDTHH:MM` in the schedule's zone; `ignoreOverride=true` evaluates the
  schedule alone). The console shows it on the time condition, with a date-time
  picker.

## Notes

- A call already in progress keeps the destination it was given; a change applies
  to the next call.
- A paging destination cannot take a call that is already ringing, so as a
  ring group failover (through a time condition) it rejects the call.
- Migrations `00035_time_conditions.sql` and `00036_time_condition_codes.sql` are additive, so a rolling upgrade works.
