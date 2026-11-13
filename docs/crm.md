# Integrations: CRM connectors

Hello can write every external call into a CRM, show the caller's name from
the CRM on an incoming call, and attach a link to the recording. This page
covers the connector framework (`internal/crm`) and its connectors: HubSpot
(spec `.procoder/specs/integrations.md`, phase P6), Salesforce (P13) and
Dynamics 365 (P19). Later connectors (Pipedrive, Zoho, ...) plug into the same
framework.

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

## Set up Salesforce

Salesforce has two halves that you can use together or apart. Both need
`HELLO_PUBLIC_URL`.

**The softphone (no Salesforce credentials).** Hello serves an Open CTI
softphone at `/integrations/salesforce/softphone`, which Salesforce shows in
the utility bar of Lightning. It runs in the user's own Salesforce session, so
Hello holds no Salesforce token for it. A user clicks a phone number in
Salesforce (or types one) and the call is placed through Hello: the user's own
desk phone rings first, then the number is dialled. An incoming call pops the
caller's contact or lead (`searchAndScreenPop`), and every answered call is
written as a Task (`saveLog`) that Hello later patches with the final duration
and the recording link.

1. In Hello open **Integrations, CRM, Add a CRM**, pick **Salesforce** and
   save (nothing else is needed).
2. On the connector, **Set up Salesforce** shows the **Call Center
   definition**. Download it. In Salesforce open **Setup, Call Centers,
   Import** and import the file, then **Manage Call Center Users** and add the
   people who dial through Hello.
3. In **Setup, CSP Trusted Sites** add the Hello address shown (the
   **CSP Trusted Site** value) so Salesforce may frame and call it.
4. Add the softphone to the Lightning app's **utility bar** (App Manager, edit
   the app, Utility Items, Open CTI Softphone).
5. Each person opens the softphone once and presses **Sign in to Hello**. A
   popup asks them to approve "Hello for Salesforce" (scopes: place calls,
   read call events). The token stays in the softphone's own session storage;
   signing out forgets it.

Without server-side logging the softphone also logs **missed** calls for the
user whose softphone is open.

**Server-side logging (a connected app).** Turn on **Also log calls from the
server** to log what no browser was open for: missed calls, calls made on a
desk phone, and to look callers up by phone number for the caller name on the
phone. It also lets Hello add recording links to the Tasks.

1. In Salesforce create an **External Client App** or Connected App with the
   OAuth scope `api` and **Enable Client Credentials Flow**, and set its
   **Run As** user to an integration user that may read Contacts, Leads and
   Users, and create and edit Tasks.
2. In Hello set **My Domain** (for example `acme.my.salesforce.com`), the
   **Client ID** and **Client secret** of that app, and save.
3. Press **Connect**. Hello signs in with the client credentials at once (no
   browser step) and shows the org.
4. Open **Users**: a Hello user is matched to the Salesforce user with the same
   email address (active users only; an address that two users share is
   skipped, never guessed). Set an id by hand or a **fallback owner** as for
   HubSpot.

### Salesforce specifics

- A call becomes a **Task** of subtype Call: `Subject`, `Status = Completed`,
  `CallType` (Inbound/Outbound), `CallDurationInSeconds`, `CallDisposition`
  (Connected, No answer, Busy, ...), `CallObject` (Hello's call id),
  `ActivityDate`, the `OwnerId`, the `WhoId` (a contact or lead) and a
  `Description` with the numbers and, when switched on, a `Recording: <link>`
  line.
- The contact is found with a SOSL phone-field search on the last nine digits
  (contacts first, then unconverted leads).
- Updates rewrite only the duration, the outcome and the `Recording:` line; a
  description a person edited keeps their text, and a renewed link replaces
  the old line.
- The softphone and the server never log a call twice: when the softphone
  saves a Task it tells Hello (`POST /api/v1/integrations/salesforce/engagements`)
  and the server patches that Task instead of creating its own. If the server
  logged first, its Task is kept (`adopted: false`).
- The client-credentials token has no refresh token: Hello signs in again when
  it expires (15 minutes are assumed, Salesforce does not say) or when
  Salesforce refuses it. When Salesforce refuses the credentials themselves
  (secret rotated, app disabled) the connection becomes `needs_reauth` once,
  with a banner and an email, exactly as for HubSpot.
- A 403 `REQUEST_LIMIT_EXCEEDED` (the org's API limit) is retried like a 429.

## Set up Dynamics 365

You need a Dynamics 365 environment (Customer Service or Sales), the right to
register an app in Microsoft Entra, and `HELLO_PUBLIC_URL`. Dynamics 365 in
sovereign clouds (US Government DoD, China) is not supported; the commercial
and GCC environments on `*.dynamics.com` are.

1. In **Microsoft Entra** register an application, create a **client secret**,
   and note the **Application (client) ID** and the **Directory (tenant) ID**.
2. In the **Power Platform admin center** add the app as an **application
   user** of your environment and give it a security role that can read
   Contacts, Leads and Users and create and write Phone Calls (a copy of
   _Salesforce / Sales Person_ with Activity create rights is enough).
3. In Hello add a **Dynamics 365** connector: the **Environment URL** (for
   example `https://contoso.crm4.dynamics.com`), the **Directory (tenant)
   ID**, the **Client ID** and the **Client secret**. Press **Connect**: Hello
   signs in with the client credentials and shows the environment.
4. Open **Users**: a Hello user is matched to the enabled Dynamics user whose
   primary email is the same (an address two users share is skipped).
5. For the in-Dynamics widget, **Set up Dynamics 365** shows the values of a
   **Channel Integration Framework** channel provider. In the **Customer
   Service admin center** open **Channel Providers**, **New**, and enter the
   name, the **Channel URL** (it carries your environment address), the
   **Trusted domain**, **Channel provider API version 2.0** and **Enable
   outbound communication**. Add the provider to the app profile of the apps
   your users dial from.
6. Each person opens the Hello panel once and presses **Sign in to Hello**
   (popup, scopes: place calls, read call events; the app is "Hello for
   Dynamics 365").

With the widget open, a click on any phone number in Dynamics (click-to-act)
rings the user's own phone and then dials the number, and an incoming call
expands the panel and opens the caller's contact, or lead.

### Dynamics specifics

- A call becomes a **Phone Call** activity: `subject`, `phonenumber`,
  `directioncode` (outgoing or not), `actualstart`, `actualend`,
  `actualdurationminutes` (whole minutes, at least one for a talked call), the
  owner, the activity parties (sender and recipient: the Dynamics user and the
  contact or lead), the regarding record and a `description` with the numbers,
  the outcome and the `Recording: <link>` line. It is created and then closed
  as **Made** (outbound) or **Received** (inbound). A missed call is a
  **Received** call whose subject says "Missed call from" and whose description
  says "No answer": Dynamics has no "missed" status reason.
- A closed activity is read-only in Dynamics, so an update (a recording link,
  its renewal) reopens it, patches it and closes it again. If closing the new
  call fails, Hello does not create it a second time: the next update closes
  it.
- Dataverse stores phone numbers as typed and has no normalised phone column,
  so the lookup queries the spellings a stored number is likely to have in
  `telephone1`, `mobilephone` and `telephone2` (leads: the first two): the last
  eight digits run together, and grouped as pairs, 2-3-3, 3-3-2, 4-4, the last
  six in pairs and the last seven as 3-4, each with a space, dash, dot or slash
  between the groups. The national and the international spelling of a line
  share their last eight digits, so `011 22 33 44` and `+32 11 22 33 44` find
  each other. The server then compares the digits of every candidate (the same
  last eight), so a different number that shares a fragment is never returned.
  A number written with mixed separators (`011/22.33 44`) or grouped in an
  unusual way is found only when its last six digits happen to be grouped
  alike; the widget's screen pop uses the same list but cannot verify the
  candidates.
- As for Salesforce there is no refresh token: Hello signs in again when the
  token expires, and `needs_reauth` follows refused credentials (an expired
  client secret is the usual cause).

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

### Connectors without a browser sign-in

Salesforce and Dynamics 365 sign in with the OAuth client-credentials grant of
the customer's own app, so **Connect** connects at once and there is no
redirect. Their jobs, owner mapping, caller names and recording links work
exactly as HubSpot's.

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
console only. `GET .../{id}/setup` lists what to configure in the CRM (the Salesforce call
center definition, the Dynamics channel provider values; administrator only, no
secrets, also an MCP read as `getCRMSetup`). Every change is audited (`create`, `update`, `delete`, `connect`,
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
`ResolveUser`, `Capabilities`), optionally `crm.OAuth` (a browser sign-in) or
`crm.DirectAuth` (client credentials, no browser), `crm.ConfigValidator`,
`crm.Pinger`, `crm.Revoker`, `crm.ServerSide` (settings that turn server-side
work off), `crm.RefValidator` and `crm.SetupProvider` (what the customer
configures in the CRM). `crm.REST` is the shared HTTP client, register it with `crm.Register(kind, factory)`
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

## Lab checklist: Salesforce (needs a real org, a Developer Edition is enough)

The tests fake Salesforce. These are not blocking the merge:

- [ ] The call center definition imports; the softphone appears in the
      utility bar; adding the Hello address as a CSP Trusted Site is enough
      for it to load and to call Hello.
- [ ] `https://<org>/support/api/62.0/lightning/opencti_min.js` is the right
      path for the `sfdcIFrameOrigin` Salesforce passes (it is `*.lightning.force.com`
      in Lightning; check Experience Cloud and sandboxes), and the page's frame
      and script allow-lists in the web server's configuration cover the
      origins in use.
- [ ] The popup sign-in completes inside the utility bar (Safari and Chrome
      with third-party storage blocked), and the token survives a page reload
      of the softphone.
- [ ] `onClickToDial` fires for a phone field and passes `number`,
      `recordId` and `recordName`; the call rings the desk phone and dials.
- [ ] An inbound call pops the single matching contact; several matches show
      Salesforce's result list; `saveLog` creates the Task with the contact as
      `WhoId` and a second `saveLog` with the `Id` updates it.
- [ ] The Task `TaskSubtype = Call` and `CallType`, `CallDisposition` and
      `CallObject` are accepted by the org's fields and picklists (a
      restricted picklist would reject a value).
- [ ] The SOSL `FIND {*<last nine digits>} IN PHONE FIELDS` finds a contact
      stored as `+32 11 22 33 44`, `011/22.33.44` and `0032...`, and does not
      match another number. A leading wildcard may not be allowed in SOSL: if
      Salesforce rejects it, drop the `*` and search the full normalised number.
- [ ] The client-credentials flow works against the My Domain host with the
      integration user; the token's real lifetime is not shorter than the 15
      minutes Hello assumes.
- [ ] A missed call with the softphone closed appears as "No answer" on the
      right owner; the user with a shared email is skipped.
- [ ] With recording links on, the Task description gets one `Recording:` line
      that is replaced, not repeated, by a renewal.
- [ ] Rotating the client secret makes the connection `needs_reauth` once.

## Lab checklist: Dynamics 365 (needs a real environment, a trial is enough)

The tests fake Entra and Dataverse. These are not blocking the merge:

- [ ] The application user with the security role can read Contacts, Leads,
      Users and create Phone Calls; **Connect** shows the environment.
- [ ] The channel provider accepts the channel URL with the `?org=` query, the
      page is allowed to be framed by the environment (`*.dynamics.com`), and
      `/webresources/Widget/msdyn_ciLibrary.js` loads from the environment
      address.
- [ ] `Microsoft.CIFramework.setClickToAct(true)` and the `onclicktoact`
      handler receive the number (the shape of the event data may differ from
      `{ "value": ... }`; the widget also accepts a bare string).
- [ ] `setMode(1)` expands the panel (the mode numbers are the ones to
      verify), and `searchAndOpenRecords("contact", ..., false)` opens a single
      match and lists several.
- [ ] A Phone Call is created with the activity parties, owner and regarding
      contact or lead; closing it as Made (outbound) or Received (inbound) is
      accepted; reopening a closed call to add the recording link works with
      the role's privileges.
- [ ] The `contains` filter with about 75 clauses is accepted (URL length) by
      Dataverse and the CIF `searchAndOpenRecords` call, and finds a contact
      stored as `+32 11 22 33 44`, `011/22.33.44` and `0032 11-22-33-44`.
- [ ] Letting the client secret expire makes the connection `needs_reauth`
      once, with the banner and the email.
