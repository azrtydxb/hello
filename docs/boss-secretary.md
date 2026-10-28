# Boss and assistant

A boss and an assistant share a line. A call to the boss rings the boss's
phones and the assistant's phones, and whoever answers first takes the call.
The links are managed under **Boss and assistant** in the console, through
`GET` and `PUT /api/v1/boss-links` (operators and above write, viewers read)
and through the matching MCP tools.

## Links

A boss has at most one assistant, and an assistant may serve many bosses. A
link has:

| Field            | Meaning                                                                                                                             |
| ---------------- | ----------------------------------------------------------------------------------------------------------------------------------- |
| `ringMode`       | `both` rings the boss's and the assistant's phones together. `assistant_first` rings the assistant alone for 10 seconds, then both. |
| `screening`      | The assistant who answers hears who the call is for and chooses (see below). Off by default.                                        |
| `enabled`        | A disabled link is kept and ignored.                                                                                                |
| `divertOn`       | The boss's own phones do not ring and only the assistant rings (see below).                                                         |
| `outboundAsBoss` | The assistant's outbound calls present the boss's caller ID. Off by default.                                                        |

`PUT /api/v1/boss-links` replaces the whole list, like the feature codes: a
link whose boss is absent from the list is removed. An assistant may present
the caller ID of one boss only, so two links of one assistant cannot both set
`outboundAsBoss`.

## How a call rings

- The boss's **forward always** wins: the call is forwarded and the assistant
  does not ring.
- **Do not disturb** on the boss silences the boss's phones. The assistant still
  rings.
- A boss who is in a call and has call waiting off is not rung either. The
  assistant is.
- If the assistant has no registered phone, the call rings the boss as if
  there were no link.
- The boss's **forward on no answer**, **forward on busy** and voicemail apply
  after the ring timeout, as for any extension.
- Each link rings only for its own boss. An assistant who is also the boss of
  someone else rings for that link when called as a boss, and for the first
  link as an assistant.
- A call the assistant (or the boss) places to the boss is not rung to the
  assistant (or the boss) again.
- When the assistant answers, the call is bridged to the assistant, the boss's
  phones stop ringing, the CDR's `answeredBy` is the assistant's extension and
  the call trace reads "Answered by assistant 300 for 200". The CDR in the API
  (`GET /api/v1/cdrs/{id}`) carries the extension in `answeredBy`.

A boss reached through an IVR menu option, a forward or a transfer rings the
same way (a menu's call always rings both sets of phones at once). A boss who is
a member of a ring group or an agent of a queue rings as an ordinary extension,
with no assistant. With `assistant_first`, an assistant who declines or whose
phones all fail sends the call to the boss at once.

## Screening

With `screening` on, an assistant who answers a call for the boss does not
speak to the caller. The assistant hears the prompt "Call for <boss name>.
Press 1 to put the caller through, or 2 to take a message." (a tone when no
text-to-speech voice is configured) and presses:

- **1**: the call is put through to the boss. The boss's phones ring at once
  (even before the 10 seconds of `assistant_first` have passed) and the
  assistant is dropped.
- **2**: the caller goes to the boss's voicemail, the boss's phones stop
  ringing and the assistant is dropped.

The prompt is repeated once. No key within 8 seconds after the second round,
or the assistant hanging up, is no choice: the assistant's leg ends and the
call goes on with the other phones that still ring. While the prompt plays the
caller keeps hearing ringback and the assistant does not hear the caller.
Screening works on a direct call, a forward and a menu's call. It needs the media anchor (always on); a call that cannot be anchored
is answered by the assistant as an ordinary call, and the trace says so.

Each choice is logged with the boss and assistant extensions only, and it
appears in the call trace.

## Divert (`*64`)

`*64` dialled from the boss's own phone toggles `divertOn` of the boss's link.
While it is on the boss's own phones do not ring and only the assistant rings,
and with the assistant not reachable the boss rings after all. The phone hears
the 200 confirmation tone, like the DND codes. `*64` dialled inside a call
toggles it too, and the current call is unaffected. The flag is stored in
PostgreSQL and reaches every SIP node through the configuration snapshot, so
a change is seen on all of them within a moment. `hello_boss_divert_total{result}`
counts the uses (`on`, `off`, `no_link`, `failed`).

The code is a feature code like the others (action `boss_divert`): it can be
renumbered under **System**, and it is refused if it clashes with another code
or a paging code. A fresh install has `*64`; an upgraded install gets it when
the code is free.

## Calling out as the boss

With `outboundAsBoss` on, the assistant's outbound calls use the boss's
caller ID: the external number of the boss's extension, through the outbound
route's caller-ID rule. The routes themselves are still chosen for the
assistant's own extension, so an assistant cannot dial where the assistant is
not allowed to. An assistant who serves several bosses with the flag on is
ambiguous, and the API refuses it.

## Failure behaviour

- PostgreSQL being down does not change how calls ring: links ride the
  configuration snapshot, and the SIP path never queries the database.
  Changing a link or the divert needs PostgreSQL.
- A `*64` whose change cannot be queued is answered 503 and counted
  `failed`.
