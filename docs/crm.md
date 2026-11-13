# Integrations: CRM connectors

Hello can write every external call into a CRM, show the caller's name from
the CRM on an incoming call, and attach a link to the recording. This page
covers the connector framework (`internal/crm`) and its connectors: HubSpot
(spec `.procoder/specs/integrations.md`, phase P6), Salesforce (P13), Dynamics 365 (P19), Pipedrive (P14), Zoho CRM
(P15), Odoo and Bitrix24 (P21), the helpdesks Zendesk and Freshdesk (P16) and the PSA and legal tools ConnectWise, Autotask, HaloPSA and Clio (P20). The Outlook and Google connectors (P18), which every user signs in to with their own account for contacts and calendar, are described in [Outlook and Google](contacts-calendar.md). Later connectors plug into the same framework.

| Connector    | Sign-in                                                                          | Lookup by phone                        | A call is written as                         | Recording                                     |
| ------------ | -------------------------------------------------------------------------------- | -------------------------------------- | -------------------------------------------- | --------------------------------------------- |
| HubSpot      | OAuth, the customer's app                                                        | Contacts search                        | Call engagement                              | Link in `hs_call_recording_url`               |
| Pipedrive    | OAuth, the customer's private app                                                | Persons search                         | Call log                                     | The audio file, uploaded to the call log      |
| Zoho CRM     | OAuth, the customer's API console client                                         | Contacts, then Leads                   | Record in the Calls module                   | Link in the call's description                |
| Odoo         | API key of a user (no browser step)                                              | Partners (`phone_mobile_search`)       | Note in the partner's chatter                | Link in the note                              |
| Bitrix24     | Inbound webhook (no browser step)                                                | Contacts, companies and leads by phone | `telephony.externalcall` register and finish | Bitrix24 fetches a one-day link, keeps a copy |
| Salesforce   | Client credentials of a connected app (optional: the softphone alone needs none) | SOSL phone search, contacts then leads | Task of subtype Call                         | `Recording:` line in the Task's description   |
| Dynamics 365 | Client credentials of an Entra app                                               | Contacts and leads, phone spellings    | Phone Call activity                          | `Recording:` line in the description          |

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
- Bitrix24 has no idempotency key on register. Hello saves how far a call got
  on the job (`crm_call_logs.state`: the registered call id and whether it was
  finished), so when the finish or the recording attach fails, the retry
  continues with that step instead of registering the call again, also after a
  restart of `hello-control`.
- The recording is attached with `telephony.externalCall.attachRecord` from a
  link Hello mints for **one day**. Bitrix24 fetches it once and keeps its own
  copy, so the link is not renewed.
- Bitrix24's own telephony REST screen-pop (`telephony.externalcall.show`) and
  its click-to-call are not part of this release.

## Helpdesks, PSA tools and Clio

| Connector              | Sign-in                                          | A call becomes                                                         | Caller found by                                                   |
| ---------------------- | ------------------------------------------------ | ---------------------------------------------------------------------- | ----------------------------------------------------------------- |
| **Zendesk**            | Agent email and API token (direct)               | An internal ticket (first comment private), tags `hello`, `call_<dir>` | `type:user phone:+E164` search                                    |
| **Freshdesk**          | API key (direct)                                 | A phone-source ticket, recording as a private note                     | Contact `phone` / `mobile` filter                                 |
| **HaloPSA**            | API application, client credentials (direct)     | A ticket for the caller's client; recording as a hidden action         | User search on the last four digits, then compared digit by digit |
| **ConnectWise Manage** | API member keys and integrator clientId (direct) | A Call activity of the contact's company, assigned to the member       | Contact communication items containing the last four digits       |
| **Autotask PSA**       | API user, secret and integration code (direct)   | A company note on the contact's company, assigned to the resource      | Contact phone fields containing the last four digits              |
| **Clio Manage**        | OAuth 2 with your own Clio application           | A phone communication between the contact and the Clio user            | Contact query on the last four digits                             |

### Direct sign-in

Zendesk, Freshdesk, HaloPSA, ConnectWise and Autotask have no browser consent:
the administrator types the credentials and Hello signs in with them.
**Connect** (the console presses it after **Add connector**) proves the
credentials against the system and stores the connection; a refused credential
is shown on the secret field and nothing is stored. Saving a new secret on a
connected connector signs in with it first, so a typo never replaces a working
credential. HaloPSA's client-credentials token expires and is requested again
before that; the others are static, and a refusal after a working period makes
the connection `needs_reauth` once, like a revoked OAuth app. The secret (API
token, key, private key) is sealed like an OAuth client secret and never
returned. Changing a setting that decides where the secret is sent (a
subdomain, host, company, region or the identity) asks for the secret again.

### Where a call goes

- **Zendesk, Freshdesk, HaloPSA** (capability `ticket`): one ticket per call.
  A missed call is a new or open ticket; the status of an answered call is a
  setting (`answeredStatus`). Zendesk's first comment is private, so the
  customer is never e-mailed about their own call. A caller Zendesk does not
  know becomes a new end user with the number when `createContacts` is on
  (international numbers only); Freshdesk creates the contact itself from the
  phone number; Halo needs a client, so an unknown caller is logged only when
  the connector has a `fallbackClientId`.
- **ConnectWise, Autotask, Clio**: the record needs a contact, so a call from
  a number that is no contact is **skipped** with the reason in the console
  (**Calls**, status `skipped`) and can be retried once the contact exists.
  The owner (assigned member, resource or Clio user) is found by email like
  HubSpot's owner; without one the fallback owner is used, else the call is
  skipped.
- The recording link is added when the recording arrives: as a second private
  comment or note on tickets (never twice: Hello reads the ticket first), or by
  rewriting the activity notes, note text or communication body.
- Phone matching: every system stores numbers as typed, so Hello asks for the
  last four digits (or the exact E.164 form for Zendesk and Freshdesk) and then
  compares digits itself (last nine digits, never fewer than seven).

### Setup values

| Connector   | Settings (`config`)                                                                                                      |
| ----------- | ------------------------------------------------------------------------------------------------------------------------ |
| Zendesk     | `clientId` (agent email), `subdomain`, `createContacts`, `answeredStatus`                                                |
| Freshdesk   | `subdomain`, `answeredStatus`                                                                                            |
| HaloPSA     | `clientId`, `host`, `tenant` (hosted instances), `ticketTypeId`, `fallbackClientId`                                      |
| ConnectWise | `clientId` (integrator client id), `host`, `companyId`, `publicKey`, `codebase` (v4_6_release), `activityType` (Call)    |
| Autotask    | `clientId` (API user), `integrationCode`, `host` (zone, found from the user when empty), `actionType` (found when empty) |
| Clio        | `clientId` (application key), `region` (us, eu, ca, au); redirect URI as shown in the console                            |

The API creates and reads them with `kind` and `config` as for HubSpot; the
console form lists the fields of each kind.

### Sidebar apps (Zendesk, Freshdesk)

`GET /api/v1/integrations/crm/{id}/app` (administrator) downloads a zip of a
ticket-sidebar app for the customer to upload to their own account (no
marketplace listing). It shows the requester's phone numbers with a **Call**
button and their latest tickets. It holds no Hello credential: Call opens
Hello's dialer (`/dial?n=`) in a new tab, where the agent is signed in and
click-to-call applies. The Zendesk app is a ZAF 2 app (upload the zip as a
private app); the Freshdesk app is an FDK 2.3 app (unzip, `fdk pack`, upload;
it asks for the subdomain and an API key to list tickets, kept as a secure
installation parameter). Needs `HELLO_PUBLIC_URL` (https). A live pop on an
incoming call needs a Hello token inside the helpdesk, which is not shipped:
the incoming-call pop stays in `/dial` and the events stream.

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
- A call placed from HubSpot's phone icon goes through the calling iframe
  (below). The iframe tells the server which engagement HubSpot created with
  `POST /api/v1/integrations/hubspot/engagements` (`calls` scope; a user may
  only record their own calls), and the server then patches that engagement
  (duration, status, the recording link) instead of creating a second one. If
  the server already logged the call, its engagement is kept and the answer
  says `adopted: false`.

## The HubSpot calling iframe

Hello serves a calling widget at `/integrations/hubspot/iframe` (HubSpot's
Calling Extensions SDK, `@hubspot/calling-extensions-sdk`). HubSpot frames it
as its phone widget. Desk phones are not WebRTC, so it only steers:

- `onDialNumber` places a Hello click-to-call (`POST /api/v1/calls/originate`):
  the user's own phone rings first, and Hello dials the number when it is
  answered. The widget then tells HubSpot `outgoingCall` with
  `createEngagement`, `callAnswered`, `callEnded` and `callCompleted` as the
  call's events arrive on Hello's event stream.
- When HubSpot creates the engagement (`onCreateEngagementSucceeded`) the
  widget posts it to the handshake route above, so the server patches that
  record. The recording link (`hs_call_recording_url`, minted by the
  recording-link store, see "Recording links") lands in the same engagement
  when the recording is ready, and is renewed there before it expires.
- An inbound call on one of the user's extensions pops as `incomingCall` while
  the widget is open (whether HubSpot offers it to an unlisted app is on the
  lab checklist).
- Sign-in is a popup. HubSpot frames the widget, where the console's session
  cookie is not sent, so the widget signs the user in with Hello's own OAuth
  client `hello-hubspot` (public, PKCE, redirect
  `/integrations/hubspot/callback`, scopes `calls` and `events`) and keeps its
  token in its own storage. Each user signs in once; **Sign out** in the widget
  forgets the token.
- Only this page may be framed, and only by `https://app.hubspot.com` and
  `https://*.hubspot.com` (every region is a subdomain of `hubspot.com`):
  nginx answers it with `frame-ancestors` for those hosts and every other page,
  the consent page included, with `frame-ancestors 'none'`.

To enable it, add a calling extension to the HubSpot app (**Calling** in the
app's settings, or the calling settings API) with the values the connector's
**HubSpot app settings** panel shows, and have each user open the phone widget
once. It needs `HELLO_PUBLIC_URL` (the first-party client exists only with
it).

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

Links are minted by the recording-link store of integrations phase P4 and
carry the connector that minted them (`recording_links.connector_id`), so the
revocation above is one statement. Zendesk, Freshdesk, HaloPSA, ConnectWise,
Autotask and Clio accept any `https` link in a note or comment.

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
`ResolveUser`, `Capabilities`), optionally `crm.OAuth` (browser consent) or
`crm.Direct` (`Authenticate` with the typed credentials: a static token or a
client-credentials grant, as Salesforce and Dynamics 365 use), `crm.ServerSide` (settings that turn server-side
work off), `crm.RefValidator` and `crm.SetupProvider` (what the customer configures in the CRM), `crm.SecretBound` (settings that choose where the
secret goes), `crm.ConfigValidator`, `crm.Pinger` and `crm.Revoker`. A
connector with no place for a call returns `crm.ErrNoTarget` and the job is
skipped, not dead (Odoo does this for a call with no contact and no owner). `Capabilities.RecordingUpload` asks the worker for the audio (`CallLog.Recording`) instead of a link; `RecordingCopy` gives a one-day link that is never renewed. A connector that writes a call in several steps returns `crm.PartialError` with the state to keep; it is saved on the job (`crm_call_logs.state`) and handed back as `CallLog.State` on the retry, also after a restart. `internal/crm/kit` has the shared request helper, the phone
comparison and the call text, and `kit/kittest` the fake server for tests, register it with `crm.Register(kind, factory)`
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
- [ ] The calling iframe: `onDialNumber`, the `outgoingCall` / `callAnswered` /
      `callEnded` / `callCompleted` messages and `incomingCall` are available to
      an unlisted app; the iframe loads in every region's HubSpot host
      (`frame-ancestors`); the sign-in popup opens from inside HubSpot's frame
      and its token survives a reload; the engagement handshake adopts the
      iframe's engagement; and the recording link appears in that engagement.

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

## Lab checklist for the helpdesk, PSA and Clio connectors

The tests mock every API. Each item needs a real account or sandbox and none
blocks the merge. Request shapes follow each vendor's published reference and
the **verify** items are the ones most likely to need a correction.

- [ ] **Zendesk**: the API token signs in; `type:user phone:+E164` finds a user
      whose phone is stored in E.164; a ticket is created with a private first
      comment, the requester, the assignee agent and the tags; an unknown
      caller becomes an end user; the recording comment appears once.
- [ ] **Freshdesk**: `GET /contacts?phone=` and `?mobile=` match the formats
      your contacts use (E.164, bare digits or `00`); a phone ticket without a
      contact creates one (it needs the name Hello sends); status mapping
      (open 2, pending 3, resolved 4); the private note appears once.
- [ ] **HaloPSA**: the client-credentials token request with `scope=all` (and
      `?tenant=` for a hosted instance); the `users?search=` term finds the
      user by the last four digits and the phone fields are returned; the ticket
      needs `client_id`, `user_id`, `agent_id` and `tickettype_id` as sent; the
      hidden action with `note_html` is accepted.
- [ ] **ConnectWise**: the `clientId` header plus `company+public:private`
      Basic auth; the `childConditions` wildcard (`like "*1234*"`) on
      `communicationItems/value`; the `Call` activity type is found by name; an
      activity with `type`, `company`, `contact`, `assignTo`, `dateStart`,
      `dateEnd` is accepted; the JSON Patch of `/notes` works.
- [ ] **Autotask**: zone discovery from the API user; the `contains` filter on
      `phone`, `mobilePhone` and `alternatePhone`; the `CompanyNotes` create and
      PATCH shape (fields and the action type pick list); `Resources/query` by
      email.
- [ ] **Clio**: the authorize and token hosts per region; `contacts.json?query=`
      finds a contact by the last four digits of a formatted number;
      `communications.json` accepts a `PhoneCommunication` with `senders` and
      `receivers` and the fields Hello sends; the PATCH of `body` works.
- [ ] **Sidebar apps**: the Zendesk zip uploads as a private app and shows the
      requester; the Freshdesk package builds with `fdk pack`; Call opens
      `/dial` for the number.
- [ ] A recording link written into a ticket or note plays, expires and is
      revoked when the connector is disabled.
