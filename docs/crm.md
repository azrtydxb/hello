# Integrations: CRM connectors

Hello can write every external call into a CRM, show the caller's name from
the CRM on an incoming call, and attach a link to the recording. This page
covers the connector framework (`internal/crm`) and its connectors: HubSpot
(spec `.procoder/specs/integrations.md`, phase P6), Pipedrive (P14), Zoho CRM
(P15), Odoo and Bitrix24 (P21). Later connectors plug into the same framework.

| Connector | Sign-in                                  | Lookup by phone                        | A call is written as                         | Recording                                     |
| --------- | ---------------------------------------- | -------------------------------------- | -------------------------------------------- | --------------------------------------------- |
| HubSpot   | OAuth, the customer's app                | Contacts search                        | Call engagement                              | Link in `hs_call_recording_url`               |
| Pipedrive | OAuth, the customer's private app        | Persons search                         | Call log                                     | The audio file, uploaded to the call log      |
| Zoho CRM  | OAuth, the customer's API console client | Contacts, then Leads                   | Record in the Calls module                   | Link in the call's description                |
| Odoo      | API key of a user (no browser step)      | Partners (`phone_mobile_search`)       | Note in the partner's chatter                | Link in the note                              |
| Bitrix24  | Inbound webhook (no browser step)        | Contacts, companies and leads by phone | `telephony.externalcall` register and finish | Bitrix24 fetches a one-day link, keeps a copy |

## What it does

| Feature            | How it works                                                                                                                                                                                                |
| ------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Call logging**   | When a call ends (`call.completed`) or nobody answers (`call.missed`), a durable job writes it into the CRM: direction, numbers, duration, outcome, the owner, and the contact found by phone number.       |
| **Caller name**    | An inbound call from a number the CRM knows rings with the contact's name as caller ID, unless the carrier already sent a real name. The name also reaches `/dial` and the extension through the events.    |
| **Recording link** | Off by default, per connector. A link to the recording is written into the CRM record and renewed while the call is recent. See [Recording links](#recording-links).                                        |
| **Sign-in**        | OAuth with the customer's own CRM app. Hello keeps the client secret and the tokens sealed (AES-256-GCM under `HELLO_SECRET_KEY`, bound to the connector) and refreshes the access token before it expires. |

Internal calls are never logged. A call that rings a ring group is logged
once, for the member whose event arrived first.

## Set up HubSpot

You need a HubSpot account and `HELLO_PUBLIC_URL` set on Hello (the OAuth
sign-in returns to it).

1. In your HubSpot developer account create an **app** (private to you, not
   listed on the marketplace). Under **Auth** note the **Client ID** and
   **Client secret**, add the scopes `crm.objects.contacts.read`,
   `crm.objects.contacts.write` and `crm.objects.owners.read`, and set the
   **Redirect URL** to `<HELLO_PUBLIC_URL>/api/v1/integrations/crm/oauth/callback`
   (the console shows the exact value under **Integrations, CRM**).
2. In Hello open **Integrations, CRM**, **Add a CRM**, pick HubSpot (and the
   data centre of your account, United States or Europe), paste the client ID
   and secret.
3. Press **Connect**. HubSpot asks you to choose the account and approve the
   app; it returns to the console. The connector shows **Connected** with the
   portal id.
4. Open **Users** and check who is mapped. A Hello user is matched to the
   HubSpot owner with the same email address (never by name). Set an id by
   hand when the emails differ, or set a **fallback owner** for the calls of
   users with no owner. Without either, those calls are skipped (visible in
   **Calls**).
5. Optional: **Attach recording links**.

The secret is write-only: it is never returned by the API, shown in the
console, logged or sent over MCP. Changing the client ID asks for the secret
again, so a secret is never sent to another app.

## Set up Pipedrive

1. In the Pipedrive developer hub create a **private app**. Set the
   **Callback URL** to `<HELLO_PUBLIC_URL>/api/v1/integrations/crm/oauth/callback`
   and grant the scopes Contacts (read), Calls (activities, full access) and
   Users (read). Pipedrive takes the scopes from the app, not from the sign-in
   address.
2. In Hello open **Integrations, CRM, Add a CRM**, pick Pipedrive, paste the
   client ID and secret and press **Connect**. The connector shows the company
   address (`api_domain`) as its account; Hello only ever sends the token to a
   `*.pipedrive.com` address.
3. **Users**: a Hello user is matched to the Pipedrive user with the same email
   (`/users/find` by email, compared exactly). One administrator's connection
   serves everyone: each call is logged with the `user_id` of its owner, so it
   appears under that person. Per-user sign-in is not needed.

Pipedrive specifics:

- A call is a **call log** (`POST /api/v1/callLogs`) with `outcome` (`connected`,
  `no_answer`, `busy`), `duration` in seconds, `from_phone_number`,
  `to_phone_number`, `start_time` and `end_time` in UTC, `user_id`, and the
  `person_id` and `org_id` found by phone. A missed call is `no_answer`.
- The person is found with `GET /api/v1/persons/search` on the phone field, by the full number's
  digits and then its last nine; a hit is accepted only when one of the
  person's own numbers ends in the same digits.
- Pipedrive cannot edit a call log and takes an audio **file**, not a link
  (`POST /api/v1/callLogs/{id}/recordings`). With **Attach recordings** on,
  Hello keeps the call log first, then uploads the audio from storage once.
  A recording larger than `HELLO_CRM_UPLOAD_MAX_MB` (default 50) is not sent;
  the call stays logged and the job shows why. No link is minted, so there is
  nothing to renew.

## Set up Zoho CRM

1. In the Zoho API console (the data centre of your account) add a
   **Server-based Application**; set the **Authorized Redirect URI** to
   `<HELLO_PUBLIC_URL>/api/v1/integrations/crm/oauth/callback`.
2. In Hello add a CRM, pick Zoho CRM, choose the **data centre** (United States,
   Europe, India, Australia, Japan, Canada, Saudi Arabia or China), paste the
   client ID and secret and press **Connect**. Hello asks for
   `ZohoCRM.modules.calls.CREATE`, `ZohoCRM.modules.calls.UPDATE`,
   `ZohoCRM.modules.contacts.READ`, `ZohoCRM.modules.leads.READ` and
   `ZohoCRM.users.READ`. The connector shows the email of the user who approved.
3. Users are matched by email (`/users/search`).

Zoho specifics:

- A call is a record in the **Calls** module (`POST /crm/v6/Calls`): `Subject`,
  `Call_Type` (`Inbound`, `Outbound`, `Missed`), `Call_Start_Time`,
  `Call_Duration` (`mm:ss`), `Description`, `Owner` and `Who_Id`.
- The caller is found with `GET /crm/v6/Contacts/search?phone=` and then
  `Leads`, as E.164 and as bare digits; each hit is checked against its own
  phone and mobile fields. A lead is attached as `Who_Id` like a contact.
- The **recording link** is a line in the call's description. Renewing the link
  rewrites the description (`PUT /crm/v6/Calls/{id}`), so text a person added
  there is replaced; keep notes in a note, not the description.
- Zoho answers a refused code or refresh token with status 200 and an `error`
  member; Hello treats that as a permanent refusal (the connection becomes
  `needs_reauth`), except its "Access Denied" rate limit, which is retried.
- Zoho's native **PhoneBridge** click-to-call is only reachable through the Zoho
  Telephony Marketplace, which Hello does not use (private apps only). Dial
  from Zoho with the browser extension.
- The data centre decides which host receives the client secret, so changing it
  asks for the secret again.

## Set up Odoo

Odoo 14 or later, through its external JSON-RPC API (`/jsonrpc`, `execute_kw`).

1. In Odoo open the user's preferences, **Account Security**, **New API Key**.
   The user needs read access to Contacts and the right to post notes.
2. In Hello add a CRM, pick Odoo and enter the **Odoo address**
   (`https://odoo.example.com`), the **database**, the **login** of the key's
   user and the **API key**. Press **Connect**: Hello authenticates with the key
   and shows the user id as the account. There is no browser step and
   `HELLO_PUBLIC_URL` is not needed.
3. Users are matched to the Odoo user whose login or email equals the Hello
   user's email.

Odoo specifics:

- Odoo has no call model every edition shares (`crm.phonecall` is gone since 17;
  the VoIP call model is Enterprise only), so a call is a **note in the chatter**
  (`message_post`, subtype Note) of the contact the caller number belongs to. A
  caller Odoo does not know gets the note on the **call owner's own partner**,
  so no call is lost. The note names the owner, direction, numbers, start,
  duration and result.
- The partner is found with the `phone_mobile_search` field (it ignores number
  formatting). An Odoo without that field falls back to a search on the last
  three digits, filtered here by the full number.
- The recording link is a line in the note; renewing it rewrites the note's body
  (`mail.message.write`).
- The API key is the credential and is sent only to the Odoo address, for that
  database and login. Changing the address, database or login asks for the key
  again. A self-hosted Odoo on a private address needs it inside
  `HELLO_INTEGRATIONS_ALLOWED_CIDRS` (RFC 1918 by default).
- The JSON-RPC endpoint is deprecated in Odoo 19 for removal in Odoo 20 (its
  replacement is `/json/2`); see the lab checklist.

## Set up Bitrix24

1. In Bitrix24 open **Developer resources, Other, Inbound webhook**; give it the
   permissions **CRM**, **Telephony** and **Users**.
2. In Hello add a CRM, pick Bitrix24 and paste the **webhook address**
   (`https://portal.bitrix24.com/rest/1/xxxxxxxx/`) as the secret. Press
   **Connect**: Hello calls `profile` and shows the portal as the account.
   The address contains the secret: it is sealed, never shown again and never
   written to a log or an error.
3. Users are matched by email (`user.get` filtered on `EMAIL`).

Bitrix24 specifics:

- The caller is found with `crm.duplicate.findbycomm` (contacts first, then
  companies, then leads), and read for its name. The call is **registered**
  (`telephony.externalcall.register`, bound to that entity, `SHOW = 0`, no
  lead is created for an unknown number) and **finished**
  (`telephony.externalcall.finish`: duration, `STATUS_CODE` 200 answered, 304
  missed, 486 busy, 503 failed).
- Bitrix24 has no idempotency key on register. If the finish step fails, a
  retry resumes with the registered call (kept in memory for a day) instead of
  registering again; a restart of `hello-control` in between can still leave
  one registered call that was never finished.
- The recording is attached with `telephony.externalCall.attachRecord` from a
  link Hello mints for **one day**. Bitrix24 fetches it once and keeps its own
  copy, so the link is not renewed.
- Bitrix24's own telephony REST screen-pop (`telephony.externalcall.show`) and
  its click-to-call are not part of this release.

## The logging pipeline

`call.completed` and `call.missed` events are turned into a row in
`crm_call_logs` inside the transaction that stores the event, unique per
connector and call, so a redelivered event never logs a call twice. A worker
claims due rows and writes them; every job is one of:

| Status    | Meaning                                                                                                             |
| --------- | ------------------------------------------------------------------------------------------------------------------- |
| `waiting` | The recording (or the HubSpot iframe's engagement) arrived before the call data. Not due.                           |
| `pending` | Due at `nextAttemptAt`.                                                                                             |
| `logged`  | In the CRM. `externalId` is the CRM's id (the HubSpot call id).                                                     |
| `update`  | In the CRM and a patch is due (a recording link, its renewal).                                                      |
| `dead`    | Out of attempts, or the CRM refused it for good (a 4xx other than 401, 408 and 429). Retry from the console.        |
| `skipped` | Deliberately not logged: no extension owns the call, or its user has no CRM user and the connector has no fallback. |

A failed attempt is retried after 10 s, 30 s, 2 min, 10 min, 1 h, 4 h and
12 h (each jittered by 20 percent): eight attempts over about 17 hours, the
same schedule as webhook deliveries. A CRM's `Retry-After` is honoured up to
an hour. When the CRM refuses the access token, Hello refreshes it once and
retries without spending an attempt.

When the CRM refuses the **refresh** token (the app was uninstalled, the
secret changed), the connection becomes `needs_reauth` once: a banner appears
in the console, the administrators with an email address are e-mailed (when
SMTP is configured), and no job of that connector runs or loses an attempt
until an administrator connects again. Then the waiting jobs are written.

### HubSpot specifics

- A call becomes a **call engagement** (`POST /crm/v3/objects/calls`) with
  `hs_call_source = INTEGRATIONS_PLATFORM`, `hs_call_duration` (milliseconds),
  `hs_call_from_number`, `hs_call_to_number`, `hs_call_direction`
  (`INBOUND`/`OUTBOUND`), `hs_call_status` (`COMPLETED`, `NO_ANSWER`, `BUSY`,
  `CANCELED`, `FAILED`), `hubspot_owner_id`, the outcome (`hs_call_disposition`,
  looked up from the portal's own call outcomes by label: Connected, No answer,
  Busy) and `hs_call_recording_url`. A missed call is `NO_ANSWER`.
- The contact is found by phone: `POST /crm/v3/objects/contacts/search` on
  `phone` and `mobilephone` (exact, in E.164) or the normalised
  `hs_searchable_calculated_phone_number` (ends with the last nine digits).
  The call is associated with it (association type 194). A call is logged
  without a contact rather than not at all.
- The owner is the HubSpot owner whose email equals the Hello user's.
- Updates are `PATCH /crm/v3/objects/calls/{id}`: they never rewrite the title
  or notes a person edited in HubSpot.
- The HubSpot calling iframe (click-to-call from HubSpot's phone icon) is not
  part of this release. The handshake it will use is: the iframe tells the
  server which engagement it created with `POST
/api/v1/integrations/hubspot/engagements` (`calls` scope; a user may only
  record their own calls), and the server then patches that engagement
  instead of creating a second one. If the server already logged the call, its
  engagement is kept and the answer says `adopted: false`.

## Caller names

An inbound call from a trunk looks the caller's number up in the CRM:

- the answer is cached in Valkey for 5 minutes, a miss for 60 seconds, so a
  repeat caller costs no CRM call;
- a CRM query is bounded to 1.5 seconds and runs in the background;
- the call waits at most **250 ms** for it on a cold cache (the CRM answer
  that comes later fills the cache for the next call), so a slow or
  unreachable CRM never holds a call. After three failures in a row a
  connector is not asked for 30 seconds;
- extension numbers and numbers under six digits are never sent to a CRM;
- the carrier's display name wins when it is a real name; a display name that
  is only a number is replaced. The CRM name is stripped of quotes, angle
  brackets and control characters and cut to 64 characters.

## Recording links

With **Attach recording links** on, a call's CRM record carries an `https`
link ending in `.wav` to the recording. The link is an opaque 256-bit token,
lives `linkTtlHours` (30 days by default), is never permanent, and anyone who
holds it can play the recording until it expires or is revoked. Hello renews
the links that expire within five days for calls younger than 180 days (an
hourly job), disconnecting or deleting the connector, disabling it or
switching recordings off revokes all its links. HubSpot only accepts `https`
links ending in `.mp3` or `.wav`; anything else is left out of the record.
Recording notices and consent stay governed by the media settings.

> The recording-link service itself ships with integrations phase P4.
> Until it is wired into `hello-control`, a connector with recording links
> switched on logs calls without a link.

## API and MCP

`GET/POST /api/v1/integrations/crm`, `GET/PATCH/DELETE .../{id}`,
`POST .../{id}/connect`, `DELETE .../{id}/connection`, `POST .../{id}/test`
(6 per minute), `GET .../{id}/logs`, `POST .../logs/{id}/retry`, `GET
.../{id}/users`, `PUT .../{id}/users/{userId}` and the public
`GET .../oauth/callback` are in `openapi.json`. The administration needs the
`admin` scope and role; the job list and retry need `operator`. The reads
(`listCRMConnectors`, `getCRMConnector`, `listCRMUsers`, `listCRMLogs`) are MCP
tools taught by the `hello-crm` skill; everything that handles credentials is
console only. Every change is audited (`create`, `update`, `delete`, `connect`,
`disconnect`, `test`, `retry`, `map_user`, `needs_reauth`), never with a
secret.

The callback needs no session (a cross-site redirect carries no `Strict`
cookie): the single-use, ten-minute state minted for the administrator who
pressed Connect is its only credential, and it always redirects to
`/integrations/crm`.

## Settings

| Setting                            | Default  | Meaning                                                                                                                                                 |
| ---------------------------------- | -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `HELLO_CRM_UPLOAD_MAX_MB`          | 50       | The largest recording uploaded to a CRM that stores the audio itself (Pipedrive). A larger one is not sent.                                             |
| `HELLO_INTEGRATIONS_ALLOWED_CIDRS` | RFC 1918 | Private ranges the CRM connectors may reach, read by `hello-control` and `hello-sip` (a self-hosted Odoo or Bitrix24). `none` allows only public hosts. |

## Metrics

| Metric                                          | Meaning                                                               |
| ----------------------------------------------- | --------------------------------------------------------------------- |
| `hello_crm_requests_total{connector,op,result}` | Calls to the CRM by operation (lookup, log, update, owner) and result |
| `hello_crm_lookup_seconds{connector}`           | Time of a contact lookup                                              |
| `hello_crm_logs{connector,status}`              | Jobs by status; alert on a growing `dead`                             |
| `hello_crm_connections{connector,status}`       | Connections by status; alert on `needs_reauth`                        |

## Adding a connector

Implement `crm.Connector` (`Lookup`, `LogCall`, `UpdateCallLog`,
`ResolveUser`, `Capabilities`), then either `crm.OAuth` or `crm.StaticAuth`
(a credential the CRM checks on Connect: an API key, a webhook address; the
stored client secret is handed to the connector as the access token), and
optionally `crm.ConfigValidator`, `crm.SecretBinder` (the settings that decide
where the secret is sent; changing one asks for the secret again),
`crm.Pinger` and `crm.Revoker`. `Capabilities.RecordingUpload` asks the worker
for the audio (`CallLog.Recording`) instead of a link; `RecordingCopy` gives a
one-day link that is never renewed. `internal/crm/httpx.go` has the shared
request and error handling. Register it with `crm.Register(kind, factory)`
and import its package from `cmd/hello-control` and `cmd/hello-sip`. The
framework supplies the sealed token storage and refresh, the cache and
timeouts, the retry schedule and dead letters, the owner mapping, recording
links, the API, the console page and the metrics. The factory receives the
SSRF-safe HTTP client (`internal/safehttp`); connectors never choose a host
from user input.

## Lab checklist (needs a real HubSpot account)

The tests mock the HubSpot API. These need a real account (a free developer
test account is enough) and are not blocking the merge:

- [ ] The app definition above is accepted; **Connect** completes and the
      connector shows the portal id and the granted scopes.
- [ ] The three scopes are enough: a call logs without 403, and
      `crm.objects.contacts.write` really is the scope the Calls object
      needs (HubSpot has no narrower one documented).
- [ ] A European account: `region = eu` sends the browser to `app-eu1` and
      the API calls on `api.hubapi.com` work with the token.
- [ ] A contact whose phone is `+32 11 22 33 44` in the national, international
      and `00` forms is found by the search; the `hs_searchable_calculated_phone_number`
      suffix filter (`CONTAINS_TOKEN *<last nine digits>`) is accepted and does
      not match a different number.
- [ ] An answered inbound call appears on the contact's timeline with the
      right duration, direction, numbers, owner and outcome (Connected).
- [ ] A missed call appears as No answer; an outbound call as outbound.
- [ ] A user with the same email as a HubSpot owner is mapped; one without
      is skipped, then logged after a fallback owner is set and **Retry**.
- [ ] With recording links on and the P4 link service wired: the call shows
      a playable recording in HubSpot (`hs_call_recording_url`), the link
      stops working after its lifetime, and renewal keeps a recent call
      playable.
- [ ] Revoking the app in HubSpot makes the connection `needs_reauth` once,
      shows the banner, e-mails the administrators, and **Connect again**
      writes the waiting calls.
- [ ] An inbound call from a contact's number rings the phone with the
      contact's name, and a second call from the same number is instant.
- [ ] 429 from HubSpot (many calls at once) slows the jobs without losing
      one.
- [ ] The calling iframe (not in this release): `onDialNumber`, the
      `outgoingCall` / `callEnded` / `callCompleted` messages and `incomingCall`
      are available to an unlisted app, and the engagement handshake adopts the
      iframe's engagement.

## Lab checklists for Pipedrive, Zoho, Odoo and Bitrix24

The tests mock each CRM's API. These need real accounts and are not blocking
the merge (they are on the tracker's accounts list):

Pipedrive (a free developer sandbox is enough)

- [ ] The private app accepts the callback URL; **Connect** completes and the
      connector shows the company address.
- [ ] `persons/search` on the `phone` field finds a person stored as
      `+32 11 22 33 44` from the full digits or from the last nine, and does
      not match another number.
- [ ] `callLogs` accepts `user_id` (the call shows under that user), `person_id`
      and `org_id`; an answered, a missed and a busy call show the right
      outcome.
- [ ] The recording upload is accepted, plays in Pipedrive, and the real size
      limit is at least the 50 MB default (the spec marks it **verify**).
- [ ] Revoking the app makes the connection `needs_reauth` once.

Zoho CRM (a developer sandbox on the account's data centre)

- [ ] The five scopes are enough; **Connect** completes on a non-US data centre.
- [ ] `Contacts/search?phone=` finds a contact in the E.164 or the digits form
      and `Leads/search` finds a lead; the Calls record with `Who_Id` of a lead
      is accepted (Zoho may want the module named).
- [ ] `Call_Duration` as `mm:ss`, `Call_Type` `Missed` and the offset in
      `Call_Start_Time` are accepted; the call shows on the contact's timeline.
- [ ] A user with the same email is found by `users/search`.
- [ ] A revoked refresh token (200 with `error`) makes the connection
      `needs_reauth`.

Odoo (14 to 19)

- [ ] `common.authenticate` accepts the API key as the password on each
      version tested; a wrong key is refused.
- [ ] `phone_mobile_search` exists on `res.partner`; where it does not, the
      fallback finds the partner.
- [ ] `message_post` with subtype `mail.mt_note` creates a note for the API
      user, and `mail.message.write` of its body is allowed to that user.
- [ ] Whether `/json/2` (Odoo 19) should replace `/jsonrpc` before Odoo 20.
- [ ] A self-hosted Odoo on a private address works with the default
      `HELLO_INTEGRATIONS_ALLOWED_CIDRS`.

Bitrix24

- [ ] A webhook with CRM, Telephony and Users permissions passes **Connect**.
- [ ] `crm.duplicate.findbycomm` finds a contact in the E.164 and the bare-digit
      form; the call registered with `SHOW = 0` and `CRM_CREATE = 0` appears in
      the entity's timeline after `finish`, with the right duration and status.
- [ ] `user.get` filtered on `EMAIL` finds the user.
- [ ] `attachRecord` with `RECORD_URL` fetches the recording from a Hello link
      and the player works after the link has expired.
- [ ] Bitrix24's rate limit (`QUERY_LIMIT_EXCEEDED`) slows the jobs without
      losing one.
