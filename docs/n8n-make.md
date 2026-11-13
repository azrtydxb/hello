# n8n and Make

Hello has a ready-made **n8n community node** (`integrations/n8n`) and a
**Make custom app** (`integrations/make`) (spec `.procoder/specs/integrations.md`,
phase P17). Both are thin clients of Hello's generic surface, the same one the
[Zapier integration](zapier.md) uses; nothing is hosted or relayed by anyone.
Your Hello console must be reachable from the tool over https, and for the
triggers the tool's webhook address must be reachable from Hello.

| What                      | Hello API it uses                                         | n8n                  | Make                  |
| ------------------------- | --------------------------------------------------------- | -------------------- | --------------------- |
| Trigger on call events    | `POST/DELETE /api/v1/webhooks` ([webhooks](integrations.md#webhooks)) | Hello PBX Trigger    | Watch Call Events     |
| Place a call              | `POST /api/v1/calls/originate`                            | Call: Place          | Place a Call          |
| Get a call record         | `GET /api/v1/cdrs/{id}`                                   | Call: Get            | Get a Call Record     |
| List the latest calls     | `GET /api/v1/cdrs`                                        | Call: Get Many       | List Calls            |
| Find a contact by number  | `GET /api/v1/lookup/number` (added with the Zapier integration) | Contact: Find by Number | Find a Contact by Number |
| Anything else             | any route with the same token                             | HTTP Request node    | Make an API Call      |

Events you can start on: `call.ringing`, `call.answered`, `call.missed`,
`call.ended`, `call.completed` (carries the `cdrId` to fetch the record),
`call.recording.ready` and `voicemail.received`. An event is the
[envelope](integrations.md#what-is-emitted) as JSON; its `id` is stable across
Hello's retries.

## The token

Create a token in Hello under **Integrations, API access**: a personal token
(`hello_pat_...`) or a service account.

- The **triggers** create and delete webhooks for you, so the token must be an
  **administrator's** and hold the `admin`, `secrets`, `events` and `read`
  scopes (`secrets` because creating a webhook returns its signing secret).
- **Place a call** needs `calls`. A personal token rings the extension linked to
  its owner; click-to-call limits and the emergency-number refusal apply as for
  any [click-to-call](click-to-call.md).
- **Get/List a call** needs `read` and a viewer role or higher.
- A token used only for actions and searches needs neither `admin` nor `secrets`.

## n8n

`integrations/n8n` is a community node package with no dependencies and no
build step.

1. Install it on your n8n (self-hosted): copy or `npm install` the directory
   into `~/.n8n/custom` (or install the package from your registry under
   **Settings, Community nodes**).
2. Add the **Hello PBX API** credential: the Hello URL (`https://pbx.example.com`)
   and the token. n8n tests it with `GET /api/v1/auth/me`.
3. Use **Hello PBX Trigger** to start a workflow and **Hello PBX** for actions.

How the trigger works: activating the workflow subscribes Hello to this
workflow's own n8n webhook URL, and deactivating deletes the subscription. Hello
returns the signing secret once; n8n keeps it in the workflow's static data and
**verifies every delivery** (`Hello-Signature`, HMAC-SHA256 over the raw body,
five minutes of tolerance, see
[verifying the signature](integrations.md#verifying-the-signature)). A request
without a valid signature starts nothing. Set n8n's `WEBHOOK_URL` to the public
address Hello can reach.

Run the tests with `node --test integrations/n8n/test/` (a fake n8n runtime; no
dependencies).

## Make

`integrations/make` holds the app as the files of Make's custom-app editor
(`makecomapp.json`, `general/base.imljson`, `connections/`, `webhooks/`,
`modules/`).

1. In Make open **Custom apps** and create an app (the VS Code extension imports
   a directory like this one; or recreate the pieces in the web editor from the
   files, they are short).
2. Create a connection with the Hello URL and the token.
3. Use **Watch Call Events** to start a scenario, and the other modules as
   steps. **Make an API Call** reaches any other route.

Make's webhook address is an unguessable secret and Make cannot verify a
signature, so on this route Hello's `Hello-Signature` is **not** checked, as with
Zapier. Keep the address private; the subscription is deleted when the
scenario is switched off.

Run the tests with `node --test integrations/make/test/`: they check that every
file parses, that the manifest lists what exists, and that every request the
modules make is an operation in `internal/api/openapi.json` with parameters
Hello accepts.

## Plain webhooks and scripts

Everything above is also available without these tools: see
[other tools: plain webhooks](zapier.md#other-tools-plain-webhooks).

## Not covered

- A real n8n or Make account. The n8n node runs against a fake n8n context and
  the Make app is checked as data. How n8n exposes the raw request body to a
  community node's webhook (needed for the signature check), whether Make
  accepts every field type of the app as written, and the look of the modules
  in each editor have not been seen. The trigger refuses a delivery whose raw
  body is missing rather than skip the check.
