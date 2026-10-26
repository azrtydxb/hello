# Voicemail

Voicemail speaks in the AI voice you configured under **Platform, Voice (TTS)**
(see [voice-prompts.md](voice-prompts.md) for the key and the voice), and
walks callers and owners through everything by voice. Without a voice
configured, or when the voice service fails, voicemail falls back to tones and
recorded audio and keeps working.

## What callers hear

A caller who reaches a box (no answer, busy, do not disturb, unreachable)
hears, in order:

1. the box's own greeting for that situation, if it has one (recorded from the
   phone, uploaded, picked from an announcement or generated from text), else
2. the default greeting, spoken in the configured voice, then
3. a tone, and the recording (`#` finishes it).

The default greetings, in English:

| Situation             | Spoken                                                                                |
| --------------------- | ------------------------------------------------------------------------------------- |
| No answer, DND, other | "Jan Peeters is not available. Please leave a message after the tone."                |
| Busy                  | "Jan Peeters is busy at the moment. Please leave a message after the tone."           |
| Unreachable           | "Jan Peeters cannot be reached at the moment. Please leave a message after the tone." |

The name is the extension's display name; an extension without one is read as
"extension 2, 0, 1". If the box has a **recorded name**, that recording plays
instead of the spoken name, followed by the spoken rest of the sentence.

A box's own greeting wins per situation: the **busy** greeting for a busy
extension, the **unreachable** one for an unreachable extension, each falling
back to the general **greeting** (which every other situation plays) and then
to the spoken default. Uploading or recording one slot never changes the
others.

## Checking messages (`*97`)

Dial `*97` from your phone, then follow the voice. (`*97` followed by an
extension number, which a phone's transfer-to-voicemail key dials, leaves a
message in that extension's box instead.)

1. **PIN.** "Please enter your PIN, followed by the pound key." Three wrong
   attempts end the call.
2. **Counts.** "You have 2 new messages and 1 saved message."
3. **Messages**, oldest first, new ones first. Each is introduced: "Message 1,
   from Jan Peeters, today at 14:32." and then played. Then the menu.

Who a message is from is the first of: the extension's display name, the
address-book contact (extensions, named and company books), the caller-ID name
the call carried, the number read digit by digit ("0, 4, 7, 1, ..."), or "an
unknown caller". When it arrived is worded in the system time zone: "today at
14:32", "yesterday at 14:32", "on Monday at 14:32" within the last six days,
"on 3 March at 14:32" beyond, with the year when it is not this year.

### Key layout

| Where           | Key        | Does                                        |
| --------------- | ---------- | ------------------------------------------- |
| After a message | `1`        | replay it                                   |
|                 | `2`        | next message (the message is kept as saved) |
|                 | `3` or `7` | delete it                                   |
|                 | `9`        | save it                                     |
|                 | `*`        | previous message                            |
|                 | `#`        | skip it, leaving it as it was               |
|                 | `0`        | mailbox options                             |
| Main menu       | `1`        | hear new messages                           |
|                 | `2`        | hear saved messages                         |
|                 | `0`        | mailbox options                             |
| Mailbox options | `1`        | record your unavailable greeting            |
|                 | `2`        | record your busy greeting                   |
|                 | `3`        | record your name                            |
|                 | `4`        | change your PIN                             |
|                 | `5`        | go back to the default (spoken) greetings   |
|                 | `*`        | back                                        |
| After recording | `1`        | keep it                                     |
|                 | `2`        | re-record                                   |
|                 | `3`        | cancel                                      |

Any key interrupts a prompt, so you can press a key as soon as you know it.
The full menu is spoken after the first message; later messages get a short
reminder. Silence repeats the menu once and then ends the call with "Goodbye".

A message played to the end is kept as **saved** (heard); interrupting it with
`#` leaves it new. Playing, saving and deleting update the message-waiting
light as they happen.

### Recording a greeting

Options (`0`) then `1`, `2` or `3`: "After the tone, record your greeting. Press
pound when finished." The recording plays back, then "Press 1 to keep it, 2 to
re-record, or 3 to cancel." A take under one second is refused. Option `5`
reverts the unavailable, busy and unreachable greetings to the spoken defaults;
the recorded name is kept (remove it in the console).

### Changing the PIN

Options, `4`: the new PIN (at least four digits, then `#`), then again to
confirm. A mismatch or a short PIN changes nothing.

## In the console

**Voicemail, Box settings** shows each slot (Greeting, Busy greeting,
Unreachable greeting, Recorded name) as **Default (AI voice)** or **Custom**.
Per slot you can upload a WAV, pick an announcement or generate from text
(greetings), **Preview default greeting** (speaks the default sentence for the
box in the configured voice and plays it), and **Revert to default** (drops the
box's own audio). The slot reads **Default (no voice set)** while no
text-to-speech key is configured; callers then hear no greeting before the tone.

## Voicemail groups

A voicemail group shares one dial number between several mailboxes. A message
left for the group is copied to **every member's box**, so each member hears
it, deletes it and gets the message-waiting lamp on their own.

- **Leaving a message.** Dial the group's number, or send a call there from an
  IVR menu, a queue exit, a time-condition branch or an inbound route (the
  destination kind **Voicemail group**, target the group number). The caller
  hears "Please leave a message for _name_ after the tone", the tone, then
  records. `#` ends the take, `*` records it again, and a take under a second
  is discarded.
- **Broadcasting.** A member dials `*63` and the group number (`*63700`) from
  their own extension, hears "After the tone, record your message for the
  group", records, and hears "Your message has been sent". Nothing rings. A
  caller who is not a member gets **403 Forbidden**; an unknown or disabled
  group gets 404. Any caller may still leave a message by dialling the number.
- **Copy on delivery.** One WAV is stored and each member box gets its own row
  that points at it, so a long message costs one object, not one per member.
  Deleting one member's copy keeps the audio while another member still has a
  row for it; the last delete removes it. The message header names the caller
  (for a broadcast, the member's extension).
- **Members** need a voicemail box. A member whose extension is deleted drops
  out of the group. A box removed after the group was loaded is skipped; the
  others keep their copy.
- **Limits.** At most 200 members and 100 groups. A message is 180 seconds by
  default; set **Longest message** up to 600 seconds per group.
- **A disabled group** takes no message (callers are refused with 404).
- **Dial space.** The number cannot equal an extension, ring group, IVR menu,
  conference room, queue or park slot, and none of them may later take it. A
  group an IVR menu, queue, time condition or inbound route sends calls to can
  be neither deleted nor renumbered until they are changed.

Manage groups under **Media, Voicemail groups** (administrators write, viewers
read). The feature code `*63` is a default code with the action
`voicemail_broadcast`; existing installs get it on upgrade when `*63` is free.
The prompts (`vmgroup.leave`, `vmgroup.broadcast`, `vmgroup.sent`) are in the
English and Dutch tables and are spoken in the configured voicemail language.
The metric `hello_voicemail_group_deliveries_total{result}` counts messages
(`delivered`, `failed`, `refused`, `discarded`).

## Languages

The prompts live in one catalogue (`internal/voiceprompt`): a key and a
template per prompt, one table per language. English (`en`) and Dutch (`nl`)
ship. Pick the language with `HELLO_VOICEMAIL_LANGUAGE` (Helm
`global.voicemailLanguage`) on both hello-control and hello-sip, and the zone
message times are worded in with `HELLO_TIMEZONE` on hello-sip (Helm
`global.timezone`; it defaults to `HELLO_PROV_TIMEZONE`, then UTC). To add a
language, add a table in `internal/voiceprompt` and list it in `tables`;
`TestCatalogueComplete` fails until it has every key and placeholder.

## How it speaks

Spoken audio is generated through OpenRouter with the model and voice saved in
the TTS settings, and **cached in the voicemail bucket** (`hello-voicemail`,
MinIO), so the whole cluster shares it:

| Object key                          | Holds                                                                                                                                    |
| ----------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| `tts/hd/<sha256>.wav`               | one spoken sentence, 16-bit mono WAV at the voice's own rate (up to 48 kHz), resampled per call, so G.722 and Opus callers hear it in HD |
| `box/<box id>/<slot>-<unix ns>.wav` | a box's own greeting, busy greeting, unreachable greeting or name (`<slot>` is `greeting`, `busy`, `unreachable` or `name`)              |
| `box/<box id>/<unix>-<call id>.wav` | a message                                                                                                                                |

`<sha256>` is the hex SHA-256 of the model, a NUL byte, the voice, a NUL byte
and the text. A different voice or model is a different key, so a change never
plays stale audio; the old objects are simply no longer used (remove `tts/`
objects from the bucket whenever you want the space back).

- **Static prompts** (the PIN prompt, the menus, the recording instructions...)
  are generated in the background when the TTS settings are saved, and
  otherwise the first time they are needed.
- **Dynamic sentences** (the counts, a message header, a greeting that names
  its owner) are generated when first needed and cached by their text. While a
  message plays, the next message's header and audio are prepared, so there is
  no wait between messages.
- **If speech is unavailable** (no key, the service down, no credits), a
  prompt that cannot be spoken becomes a short tone, and `*97` without speech
  at all is the plain flow: a tone, the PIN, then the unheard messages played
  in turn. After a failure the service is left alone for 30 s, so a dead
  provider costs one timeout, not one per prompt. A warning is logged without
  the key.
- The key never leaves the control plane and hello-sip's memory: it is sealed
  in the database and is never logged.

## High availability

A voicemail call replicates like any other. If the node dies the taker
re-INVITEs the caller and restarts the application: a caller leaving a message
hears the greeting again; a caller checking messages hears the PIN prompt again.

## API

| Route                                                      | Role     | Purpose                                                             |
| ---------------------------------------------------------- | -------- | ------------------------------------------------------------------- |
| `PUT /api/v1/extensions/{id}/voicemail`                    | operator | PIN, email, and uploads (`greeting`, `busy`, `unreachable`, `name`) |
| `PUT /api/v1/extensions/{id}/voicemail/greeting`           | operator | a greeting from an announcement or text                             |
| `POST /api/v1/extensions/{id}/voicemail/greeting/preview`  | operator | the default spoken greeting, as WAV                                 |
| `DELETE /api/v1/extensions/{id}/voicemail/greeting/{slot}` | operator | revert a slot to the default                                        |
| `GET /api/v1/voicemail-groups`                             | viewer   | the voicemail groups with their members                             |
| `POST /api/v1/voicemail-groups`                            | admin    | create a group (number, name, members, `maxSeconds`, `enabled`)     |
| `PUT /api/v1/voicemail-groups/{id}`                        | admin    | replace a group, member list included                               |
| `DELETE /api/v1/voicemail-groups/{id}`                     | admin    | delete a group (refused while something sends calls to it)          |
