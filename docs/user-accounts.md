# User accounts and extension self-service

Hello has one table of people who can sign in: `users`. A user has a login
(a username or an email address), a display name, an optional email address,
a role, an enabled flag and any number of linked extensions. Admins manage
them in **Platform → Users** or through `POST/PATCH/DELETE /api/v1/users`.
The `hello-control user add` command still works and is how the first admin is
made (`HELLO_BOOTSTRAP_ADMIN_PASSWORD` creates `admin` as before).

## Roles

| Role       | What it reaches                                                                                                                          |
| ---------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| `user`     | Only the `/api/v1/me/...` routes and `auth/me`, `auth/logout`, `auth/password`: the user's own extensions and nothing else (see below). |
| `viewer`   | Every read operation.                                                                                                                    |
| `operator` | Also every write operation.                                                                                                              |
| `admin`    | Also users, credentials, connected apps and secrets.                                                                                     |

`user` ranks below `viewer`: every route that asks for viewer or more refuses
it with `403 forbidden_role`. A user token or OAuth grant can hold `read` and
`write` at most, and only reaches the same `/me` routes. Service accounts
cannot have the role.

## Sign-in, passwords and the first login

Sign-in is local. Passwords are hashed with argon2id (19 MiB, t=2, p=1, PHC
format); hashes made before this change are bcrypt, still accepted and
replaced by an argon2id hash the next time that user signs in. A password has
10 to 128 characters. The API never returns a password or a hash, and neither
appear in logs or audit rows.

An admin sets the initial password (or resets it later). Unless the admin
turns **Must choose a new password** off, the account is flagged: after
signing in the user can do nothing but change the password (`403
password_change_required` everywhere else, and the console shows only the
change form). Changing it needs the current password, ends the user's other
sessions, and is possible only from a browser session, not with a token.

Disabling a user ends their sessions and makes their tokens and OAuth grants
stop working at once. Deleting a user removes their sessions, tokens and
personal contacts; their extension and its voicemail stay. The last enabled
admin cannot be demoted, disabled or deleted, and an admin cannot disable or
delete their own account (`409 last_admin`, `self_disable`, `self_delete`).

OIDC sign-in does not exist in Hello yet; `authMethod` is `local` for every
user, so a later method can be added without changing the table.

## Linking extensions

In the user dialog, tick the extensions the user manages. An extension belongs
to at most one user (a taken one is shown as such and refused with `409`). The
first extension is the primary one: the user's personal address book is
delivered to its phones (see `docs/provisioning.md`). `GET /api/v1/users`
returns `extensionId` (primary) and `extensionIds` (all).

Users that exist from before keep their link: migration `00030` copies
`users.extension_id` into the new `user_extensions` table.

## What a `user` can do

All of it is under `/api/v1/me` and acts on the caller's linked extensions:

- **Address book:** `/me/contacts` (unchanged): list, add, edit, delete, CSV
  import and export.
- **Voicemail:** list messages, play and download (`?download=1`), mark heard
  or unheard, delete; set the PIN (4 to 10 digits); upload or, in the
  console, record a greeting and an unreachable greeting (PCM WAV, 5 MiB,
  recorded as 8 kHz mono). The box's own notification address is not theirs to
  set.
- **Forwarding:** the extension's display label, do not disturb and the
  always, busy and no-answer forwards. Hello has no follow-me. A forward to an
  outside number is routed like a call from the extension would be, by the
  outbound routes the administrator configured.
- **Call history:** the calls where one of their extension numbers was the
  caller or callee, without routes, trunks or signalling detail.
- **Profile:** name, email address, voicemail email options and password.

They cannot see other extensions, devices, trunks, routes, SIP passwords or any
system setting.

### How it is enforced

Every `/me` handler takes the user from the authenticated credential and from
nothing else: no route has a user id. An extension or message id in a path is
looked up only through the caller's `user_extensions` rows, in the same SQL
statement, so an id that belongs to someone else answers `404` exactly like one
that does not exist; guessing ids finds nothing and mutates nothing. A
credential that is not a user (a service account) gets `403 no_user`. The
tests are `TestSelfServiceIsolation` (every non-self route refused for the
role, plus a sweep of ids) and `TestSelfServiceSQL` (the queries on a real
database).

## Voicemail to email

When a message lands in a box, every enabled user linked to the extension who
has an email address and has notifications on gets one email with the caller
ID, the time and the duration. The box's own notification address (set by an
admin on the extension) still gets one too; if it is the same address as a
user's, that user's options apply. Each user chooses:

- **Email me new voicemail** on or off,
- **Attach the recording** on or off (the attachment is the stored audio,
  WAV, or MP3 if that is what the box holds; nothing is transcoded),
- **After sending**: leave the message, mark it as read, or delete it. The
  action runs once every recipient of that message was mailed, and delete wins
  over mark as read.

Users edit these on **My phone → Profile**, admins in the user dialog.

### Delivery: asynchronous, retried, exactly one node

Nothing on the SIP path waits for email. Every hello-control replica runs a
worker over a queue in PostgreSQL (`voicemail_notifications`, one row per
recipient). A worker turns new messages into notification rows, then claims
due rows with `UPDATE ... FOR UPDATE SKIP LOCKED`, so exactly one node holds a
notification at a time; no second lock service is involved, because the
database is where the work lives. A failed send is retried with exponential
backoff (30 s doubling to 1 h, up to 6 attempts, and never later than 24 hours
after the message); a node that dies mid-send loses its claim after a few
minutes and another node sends it (so a crash can produce a duplicate, never a
loss). With no SMTP settings the notifications wait for them.

## System email settings

**Platform → Email** (admin; `GET/PUT /api/v1/settings/smtp`): host, port,
security (STARTTLS, TLS, or none), username, password and the from address.
The password is sealed with `HELLO_SECRET_KEY` like a trunk password, is never
returned (`passwordSet` says whether one exists) and is never logged. Leave it
empty to keep the stored one. Saved settings apply to the next send on every
node; no restart.

**Send test email** (`POST /api/v1/settings/smtp/test`) sends one message now
with the settings in force and shows the server's answer.

Deployments that set `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASS` and
`SMTP_FROM` keep working: they apply until settings are saved in the console,
with their old transport behaviour (STARTTLS if offered, implicit TLS on port
465). Saved settings win; saving an empty host removes them and brings the
environment back.
