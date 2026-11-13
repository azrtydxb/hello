# Integrations: CRM connectors

Hello can write every external call into a CRM, show the caller's name from
the CRM on an incoming call, and attach a link to the recording. This page
covers the connector framework (`internal/crm`) and the first connector,
HubSpot (spec `.procoder/specs/integrations.md`, phase P6). Later connectors
(Salesforce, Pipedrive, Zoho, ...) plug into the same framework. Connectors that
every user signs in to with their own account (Outlook and Google contacts and
calendar, phase P18) are described in
[Outlook and Google](contacts-calendar.md).

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

## Metrics

| Metric                                          | Meaning                                                               |
| ----------------------------------------------- | --------------------------------------------------------------------- |
| `hello_crm_requests_total{connector,op,result}` | Calls to the CRM by operation (lookup, log, update, owner) and result |
| `hello_crm_lookup_seconds{connector}`           | Time of a contact lookup                                              |
| `hello_crm_logs{connector,status}`              | Jobs by status; alert on a growing `dead`                             |
| `hello_crm_connections{connector,status}`       | Connections by status; alert on `needs_reauth`                        |

## Adding a connector

Implement `crm.Connector` (`Lookup`, `LogCall`, `UpdateCallLog`,
`ResolveUser`, `Capabilities`), optionally `crm.OAuth`, `crm.ConfigValidator`,
`crm.Pinger` and `crm.Revoker`, register it with `crm.Register(kind, factory)`
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
