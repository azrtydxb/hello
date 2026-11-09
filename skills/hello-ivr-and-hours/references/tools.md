# Tools for IVR, hours, announcements and hold music

| Tool                       | Scope | What it does                                                               |
| -------------------------- | ----- | -------------------------------------------------------------------------- |
| `listAnnouncements`        | read  | Uploaded announcements with their ids.                                     |
| `createAnnouncement`       | write | Upload an announcement as JSON `{name, audioBase64}` (WAV, 700 KiB).       |
| `replaceAnnouncement`      | write | Replace an announcement's audio (`{audioBase64}`); the name cannot change. |
| `saveAnnouncementPrompt`   | write | Save the authored text, voice and model without generating audio.          |
| `deleteAnnouncement`       | write | Delete one; refused while an IVR menu names it.                            |
| `listMOHClasses`           | read  | Music on hold classes with their playlists.                                |
| `getMOHClass`              | read  | One class.                                                                 |
| `createMOHClass`           | write | Create a class from announcement ids.                                      |
| `putMOHClass`              | write | Replace a class including its playlist.                                    |
| `deleteMOHClass`           | write | Delete a class; users fall back to the default.                            |
| `getTimeSettings`          | read  | The default time zone of a new schedule.                                   |
| `listCalendars`            | read  | Holiday calendars.                                                         |
| `getCalendar`              | read  | One calendar with its entries.                                             |
| `createCalendar`           | write | Create a calendar with entries.                                            |
| `putCalendar`              | write | Replace a calendar.                                                        |
| `importCalendar`           | write | Add or replace entries from dated lines or iCalendar text.                 |
| `deleteCalendar`           | write | Delete; refused while a schedule consults it.                              |
| `listSchedules`            | read  | Weekly schedules.                                                          |
| `getSchedule`              | read  | One schedule.                                                              |
| `createSchedule`           | write | Create a weekly schedule.                                                  |
| `putSchedule`              | write | Replace a schedule.                                                        |
| `deleteSchedule`           | write | Delete; refused while a time condition uses it.                            |
| `listTimeConditions`       | read  | Time conditions.                                                           |
| `getTimeCondition`         | read  | One time condition.                                                        |
| `createTimeCondition`      | write | Create a schedule-driven switch between destinations.                      |
| `putTimeCondition`         | write | Replace its schedule and destinations; keeps the override.                 |
| `setTimeConditionOverride` | write | Force open or closed, or return to auto.                                   |
| `evaluateTimeCondition`    | read  | What it does now or at a given time.                                       |
| `deleteTimeCondition`      | write | Delete; refused while something names it.                                  |
| `listIVRMenus`             | read  | IVR menus with options.                                                    |
| `getIVRMenu`               | read  | One menu.                                                                  |
| `createIVRMenu`            | write | Create a menu.                                                             |
| `putIVRMenu`               | write | Replace a menu including options.                                          |
| `deleteIVRMenu`            | write | Delete; refused while something names it.                                  |

Also used: `getFlowGraph`, `getFlowRevision`, `testRouting`,
`createInboundRoute` (see the `hello-flow-graph` and `hello-routing` skills).
