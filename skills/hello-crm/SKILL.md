---
name: hello-crm
description: Inspect the CRM connectors of a Kuvryn Hello PBX through its MCP server - connection state of HubSpot, Pipedrive, Zoho CRM, Odoo, Bitrix24, Zendesk, Freshdesk, ConnectWise, Autotask, HaloPSA and Clio, which Hello users map to which CRM users, and the logging jobs that write calls into the CRM, including dead letters and skipped calls. Use when someone asks why a call is missing from the CRM, why a caller shows no name, whether the CRM connection still works, who owns calls in the CRM, or why a recording link is not on a CRM record.
license: Apache-2.0
metadata:
  hello-version: "v1"
---

# Hello CRM connectors

Hello can write every external call into a CRM (HubSpot, Pipedrive, Zoho CRM, Odoo, Bitrix24, Clio), a helpdesk
(Zendesk, Freshdesk, HaloPSA) or a PSA (ConnectWise, Autotask) and look up the
caller's name there. A **connector** is the customer's own app or API
credentials (set up in the console); its **connection** is the OAuth sign-in an
administrator completed, or, for the token-based kinds (`auth` is `direct`:
Zendesk, Freshdesk, ConnectWise, Autotask, HaloPSA, Odoo, Bitrix24), the credentials Hello
signed in with (also Odoo, an API key, and Bitrix24, an inbound webhook). Helpdesk kinds (`capabilities.ticket`) write each call as a
ticket; ConnectWise writes an activity, Autotask a company note, Clio a phone
communication. Calls are written by background **logging jobs**
that retry on failure. You can read all of this; changing connector settings,
signing in and retrying a job are done in the console under Integrations, CRM,
because they handle credentials.

## Connecting

- MCP server: `https://<hello host>/mcp` (on kw: `https://hello.kw.watteel.lab/mcp`).
- `listCRMConnectors`, `getCRMConnector` and `listCRMUsers` need scope `admin`
  and the `admin` role; `listCRMLogs` needs scope `write` and the `operator`
  role. Client secrets and tokens are never in any result.

## Is the connection healthy?

`listCRMConnectors` returns each connector with `enabled`, its `capabilities`
and its `connection`:

| `connection.status` | Meaning                                                                                                                                                                                  |
| ------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `connected`         | Tokens are valid and are refreshed before they expire.                                                                                                                                   |
| `needs_reauth`      | The CRM refused the refresh token (the app was uninstalled or its secret changed). No job runs and no call is lost: jobs wait. An administrator must press Connect again in the console. |
| (null connection)   | Never signed in, or disconnected. Jobs wait.                                                                                                                                             |

`connection.lastError` says why. `connection.grantedScopes` should include the
contact and owner scopes of the app; a missing scope shows up as dead jobs
with a 403 in `lastError`.

## A call is missing in the CRM

1. Find the call id with `getCDR` (its correlation id is the job's `callId`).
2. `listCRMLogs` with the connector id, filtered by `status`, lists the jobs
   newest first (`nextBefore` pages on). Read the job:

| `status`  | Meaning and what to do                                                                                                                                                                                                                                                                                                                         |
| --------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `pending` | Due at `nextAttemptAt`; `attempts` and `lastError` show earlier failures. Retries go 10 s, 30 s, 2 min, 10 min, 1 h, 4 h, 12 h.                                                                                                                                                                                                                |
| `logged`  | Written; `externalId` is the CRM record id.                                                                                                                                                                                                                                                                                                    |
| `update`  | Written, and a patch (a recording link) is due.                                                                                                                                                                                                                                                                                                |
| `waiting` | Its recording or the HubSpot iframe reported first; the call data has not arrived yet.                                                                                                                                                                                                                                                         |
| `skipped` | Deliberately not logged: `lastError` says why (no extension owns the call, the user has no CRM user, or the CRM has no contact with the caller's number: ConnectWise, Autotask, Clio and HaloPSA without a fallback client only log calls for a known contact). Map the user (below) or set a fallback owner in the console, then retry there. |
| `dead`    | Out of attempts, or the CRM refused it for good (a 4xx). `lastError` is the CRM's reason. Fix the cause, then retry in the console.                                                                                                                                                                                                            |

Internal calls are never logged. A missed call is logged as a no-answer call
when the connector has "log missed calls" on. A call that rang several people
is logged once.

## Who owns the call in the CRM?

`listCRMUsers` lists every Hello user with `externalUserId`, the CRM user
their calls belong to, and `source`: `email` (found automatically by matching
the Hello account's email to the CRM user's email; display names are never
used) or `manual` (set in the console, never replaced automatically). A user
with no email, or whose email no CRM user has, is unmapped: their calls go to
the connector's `fallbackOwner`, or are skipped when that is empty.

## Caller names and recordings

- Inbound calls from a number the CRM knows ring with the contact's name as
  caller ID. Lookups are cached for 5 minutes (a miss for 1 minute) and are
  bounded so a slow CRM never delays a call; a number can therefore show no
  name on the first call after a slow CRM answer.
- `recordings` on a connector is off by default. When it is on, a call's
  record gets a link to the recording that expires after `linkTtlHours` and is
  renewed while the call is recent. A job's `linkExpiresAt` shows when. A
  `logged` job with a `recordingId` but no `linkExpiresAt` means the link
  could not be minted: check that recording links are available.
- Pipedrive stores an audio file, not a link: a `logged` job with a
  `recordingId` and no `linkExpiresAt` is normal there. If `lastError` says
  "recording not sent", the file was larger than the upload limit
  (`HELLO_CRM_UPLOAD_MAX_MB`, 50 by default) or the recording was deleted; the
  call itself is logged.
- Bitrix24 fetches its recording from a one-day link once and keeps a copy, so
  it also has no `linkExpiresAt`.
- Odoo logs a call as a note in the contact's chatter, or in the call owner's
  own when the caller is unknown; Zoho writes a record in the Calls module.

See [tools](references/tools.md) and [failures](references/failures.md).
