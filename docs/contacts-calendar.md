# Outlook and Google: contacts and calendar

Each user can connect **their own** Outlook (Microsoft 365) or Google account
to Hello (spec `.procoder/specs/integrations.md`, phase P18, I-56):

- **Contacts name callers.** An inbound call from a number in the called user's
  contacts rings their phone with that contact's name, and the name reaches
  `/dial` and the extension through the call events.
- **Calendar sets Do Not Disturb.** While the user's calendar says busy or out
  of office, DND is on at their extensions; the end of the event switches it
  off.

Both build on the CRM connector framework ([CRM connectors](crm.md)) as
connectors that sign in **per user** instead of once for the organisation. Hello
only reads: it never writes a contact or an event, and the Google calendar
permission is free/busy only, so Hello never sees an event's title or attendees.

## Set it up (administrator)

You create the OAuth app in your own Microsoft or Google tenant (no marketplace
listing, nothing hosted by anyone else). Set `HELLO_PUBLIC_URL` first.

Open **Integrations, CRM, Add a CRM** and choose **Outlook** or **Google**.
Enter the client id and secret, choose what users may connect (**Contacts name
callers**, **Calendar sets DND**, or both: the sign-in asks only for what is on)
and save. The connector shows **Per user**; there is no organisation sign-in.
Changing the features asks users to connect again.

**Outlook (Microsoft Entra).** Register an application, platform **Web**, redirect
URI `<HELLO_PUBLIC_URL>/api/v1/integrations/crm/oauth/callback`, create a client
secret, and add the delegated Microsoft Graph permissions `User.Read`,
`offline_access` and, per feature, `Contacts.Read` and `Calendars.Read`. They are
read-only and need no administrator consent. **Tenant** is `organizations` (any
work or school account, the default), a tenant id or your domain.

**Google.** In the Google Cloud console create an OAuth client (web application)
with the same redirect URI, enable the **People API** and the **Google Calendar
API**, and on the consent screen add `contacts.readonly` and `calendar.freebusy`.
Keep the app internal to your Workspace domain, or in Testing for named users:
Google's verification for sensitive scopes is only needed for a public app.

## Connect (user)

Open **My phone, Connected accounts**, press **Connect** and approve. Hello
stores your tokens under your own user, sealed so that they do not open under
anyone else's row. **Disconnect** removes them, revokes the Google token and
switches off a DND the calendar set. If the provider later refuses the stored
sign-in (you changed your password, an administrator revoked the app) the card
says **Sign in again** and your contacts and calendar are not used until you do.

## Contacts

For an inbound call from a trunk, after the organisation's CRM (HubSpot) found
nothing, Hello looks the caller's number up in the contacts of the user who
owns the called extension:

- A call to a ring group, queue or IVR is not named from anyone's contacts: only
  a call routed straight to a user's extension is.
- The carrier's display name still wins when it is a real name (a number-only
  name is replaced).
- Hello reads the whole address book once (up to 20,000 contacts: the first
  contacts folder of Outlook, **My contacts** of Google) and keeps it in memory
  for 10 minutes, matching on the **last eight digits** so that `+32 50 12 34 56`
  and `050 12 34 56` are the same number. Two contacts with one number name nobody:
  a caller is never given a wrong name.
- The first call after a quiet spell may ring without the name (the read takes
  longer than the 250 ms a call waits), later ones are named. The name is cached
  per user for 5 minutes (a miss for 60 seconds).
- Other people's address books are never searched: each user is named only from
  their own.

## Calendar DND

Under **Connected accounts** turn on **Do Not Disturb while my calendar says
busy**. Every two minutes Hello asks the calendar whether you are busy now:

| Provider | Counts as busy |
| -------- | -------------- |
| Outlook  | an event shown as **busy** or **out of office** that is not cancelled and that you did not decline; an all-day event only when it is out of office (a free all-day birthday is not). Tentative and free events do not count. |
| Google   | a busy block of the primary calendar (free/busy; an out-of-office event is one). |

When you become busy Hello sets DND on every extension you own (`dndSource` is
`calendar`), and when you stop being busy it clears the DND it set.
Back-to-back meetings are one stretch. The rules that keep it from surprising you:

- A DND **you** set (the console, a feature code, the phone's DND key) is never
  overridden and never cleared by the calendar. An extension that already has DND
  on is left alone, and if you switch DND on yourself during a meeting it becomes
  yours (`dndSource` `manual`) and stays after the meeting.
- If you switch DND off yourself during a meeting, the calendar does not turn it
  back on until your next busy stretch.
- A calendar that cannot be read keeps the DND until the meeting's known end
  plus 5 minutes, then clears it, so a network blip does not drop it mid-meeting
  and a broken connection cannot hold it for ever.
- With two connected calendars, DND stays on while either is busy.
- Disconnecting, or turning the setting off, clears a DND the calendar set at
  once.

`dndSource` (`manual` or `calendar`) is part of the extension in the API
(`/extensions`, `/me/extensions`) and is set by every path that changes DND.
Hello polls every two minutes from one control replica at a time; Graph and
Google push notifications would cut the delay but need a public callback and
subscription renewals, so they are not used.

## API

Users manage their own connection with `GET /api/v1/me/connections`,
`POST /api/v1/me/connections/{id}/connect`, `DELETE /api/v1/me/connections/{id}`
and `PUT /api/v1/me/connections/{id}/calendar-dnd` (the connect, disconnect and
DND routes are not offered over MCP: they are browser sign-ins and a personal
choice). The administrator's connector settings are the CRM routes
(`/api/v1/integrations/crm`); `capabilities.userScope` and `capabilities.calendar`
tell the two kinds apart. No token or secret is ever returned.

## Not covered (needs real accounts)

- A real Microsoft 365 tenant and Google account: the connectors are tested
  against fake Graph, People and Calendar servers that answer as the references
  document. Facts to confirm live: that `GET /me/contacts` with `$top=999` pages
  as `@odata.nextLink`, that `calendarView` honours `Prefer: outlook.timezone`, that
  Google returns a refresh token with `prompt=consent`, and that the People
  `connections` list returns `canonicalForm` for numbers.
- Shared and delegated mailboxes, contact folders other than the first (Outlook),
  and "Other contacts" (Google) are not read.
