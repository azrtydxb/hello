# IVR / auto-attendant

An IVR menu answers a call, plays a greeting, collects a key press and sends
the caller where that key points. Menus are managed under **IVR** in the
console, or through `/api/v1/ivr-menus` (and the matching MCP tools).

## Building a menu

A menu has:

| Field                            | Meaning                                                                                                                        |
| -------------------------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| Name                             | Unique, 1-64 of `A-Z a-z 0-9 . _ -`. Other settings name a menu by it.                                                         |
| Number                           | Optional, 2-16 digits. Dialling it from a phone runs the menu. It must not be an extension number or ring group name.          |
| Greeting                         | An announcement. Upload a WAV under Announcements, or use one generated there.                                                 |
| Invalid / timeout prompt         | Optional announcements played before the greeting repeats after a wrong key or after silence. Empty replays the greeting only. |
| Wait for a key                   | Seconds after the prompt before the caller counts as silent (1-60, default 5).                                                 |
| Between digits                   | Seconds between the digits of a dialled extension (1-30, default 3).                                                           |
| Repeats before giving up         | How often the menu repeats after silence or a wrong key (0-10, default 3). The counter is shared by both.                      |
| Allow dialling an extension      | Digits that are not a key dial an extension number.                                                                            |
| Keys                             | `0`-`9`, `*` and `#`, each once, each with a destination.                                                                      |
| After silence / after wrong keys | The destination that applies once the repeats are used up. Default: hang up.                                                   |

### Destinations

Every key, the silence exit and the wrong-key exit use the same kinds:

| Kind         | Target                    | What happens                                                                                                                                                                                  |
| ------------ | ------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Hang up      | none                      | The PBX ends the call.                                                                                                                                                                        |
| Extension    | an extension number       | Rings it like a call to it: forward always, DND, forward on busy or no answer and voicemail apply.                                                                                            |
| Ring group   | a group name              | Rings the group with its strategy; its failure destination (voicemail, announcement, external, IVR menu) applies.                                                                             |
| Voicemail    | an extension number       | Leaves a message in that extension's box.                                                                                                                                                     |
| Announcement | an announcement name      | Plays it, then hangs up.                                                                                                                                                                      |
| Paging group | a page code such as `*60` | Pages the group one way, as if the caller had dialled the code. The group's allowed callers apply to the caller's number.                                                                     |
| IVR menu     | a menu name               | Continues in that menu (nesting).                                                                                                                                                             |
| External     | a number                  | Routed through the outbound routes as if the menu dialled it (the menu's number, else its name, is the source "extension"; add it to a route's source extensions if the route is restricted). |

A destination that does not answer returns the caller to the menu, which says
the invalid-choice prompt and repeats; it does not end the call.

### Direct extension dialling

With **Allow dialling an extension** on, digits that are not a key are
collected as an extension number. A number that matches an extension and
cannot be longer rings at once; otherwise the menu waits the between-digits
time, or `#`, for more digits. A key whose digit is also the start of
extension numbers waits the same way and fires when nothing follows.

### Loops

A menu may be reached from itself through keys ("0 for the main menu"): each
pass needs a key press. Exits that need none (silence, wrong keys) must not
lead back to the menu through other menus: the API refuses it, and at run
time a call is ended if it passes through the same menu twice on such a chain.
A call is also ended after 32 menu entries.

## Getting calls into a menu

- Dial the menu's number from a phone.
- An inbound route with destination kind **IVR menu**.
- A ring group's failure destination **IVR menu**.
- A feature code with the action `ivr`; the argument names the menu.
- A key, silence or wrong-key exit of another menu.

A menu that something names cannot be deleted or renamed. What a menu names
cannot be removed from under it: an extension (also as a voicemail box), ring
group, paging group or announcement that a key or an exit points at cannot be
deleted or renumbered (409, listing the menus), and a menu's own greeting and
prompts cannot be deleted while it plays them.

## Call flow

The menu answers through the media relay (so any codec the relay handles
works) and plays the greeting. A key press stops the prompt at once
(barge-in) and is the first digit. Keys arrive as RFC 4733 telephone events
through the relay and as SIP INFO `application/dtmf-relay` in the dialog. A
missing or unreadable announcement is skipped with a trace step, like every
announcement.

When a key sends the call to a phone or trunk, a leg is rung whose offer is
the relay's callee port; the caller's dialog does not change. When the
destination answers the call is an ordinary anchored call: hold, transfer,
recording, `*1` and the rest work, and billing runs from the moment the menu
answered. Voicemail destinations record through the relay.

The log shows each step at INFO (`ivr menu entered`, `ivr option chosen`,
`ivr direct dial`, `ivr timeout`, `ivr invalid`, `ivr exit`) with the menu,
correlation id and caller, the CDR trace repeats them, and
`hello_ivr_total{event}` counts them.

## High availability and drain

An IVR call is a call like voicemail: it counts as an active call (so a drain
waits for it or hands it off), and it replicates as a one-dialog call. A
survivor that takes it over restarts the menu from its greeting. See
[ha.md](ha.md). A menu a survivor restarted can also dial out (extension, ring group,
external number), take voicemail and route to announcements and menus. A
paging destination is not available there; the menu's invalid-choice exit
applies instead. See [ha.md](ha.md).

## Requirements

The menu needs the media relay (a G.711 offer) and the announcement store
(MinIO). A call without an SDP offer is refused with 488.
