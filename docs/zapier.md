# Zapier and other automation tools

Hello ships a **Zapier private integration** (spec `.procoder/specs/integrations.md`,
I-52, phase P7) in `integrations/zapier/`. A private integration is shared with
your own Zapier account or team by an invite link: it needs no Zapier review and
no listing, and Hello does not host or relay anything. Zapier talks straight to
your Hello console, so the console must be reachable from the internet over
`https` (Zapier's servers call it, and its hook URLs `https://hooks.zapier.com/...`
are called by Hello).

Any other tool (Make, n8n, a script) can use the same two pieces without
Zapier: [plain webhooks](#other-tools-plain-webhooks) and the REST API.

## What the integration offers

| Kind    | Name                   | Hello API it uses                           |
| ------- | ---------------------- | ------------------------------------------- |
| Trigger | Call Ringing           | webhook on `call.ringing`                   |
| Trigger | Call Answered          | webhook on `call.answered`                  |
| Trigger | Call Missed            | webhook on `call.missed`                    |
| Trigger | Call Completed         | webhook on `call.completed` (has `cdrId`)   |
| Trigger | Call Recording Ready   | webhook on `call.recording.ready`           |
| Action  | Place Call             | `POST /api/v1/calls/originate`              |
| Search  | Find Contact by Number | `GET /api/v1/lookup/number`                 |
| Search  | Find Call              | `GET /api/v1/cdrs/{id}`, `GET /api/v1/cdrs` |

A trigger event is the [call event](integrations.md#what-is-emitted) as Zapier
fields: `data__from__number`, `data__extension`, `data__durationMs` and so on.
Zapier de-duplicates on the event `id`, which is stable across Hello's retries.

## Set it up

1. **Create a token.** In **Integrations → API access** create a personal API
   token (`hello_pat_...`) or a service account.
   - Triggers create and delete webhooks for you, so the credential must be an
     **administrator's** and hold the `admin`, `secrets`, `events` and `read`
     scopes (`secrets` because creating a webhook returns its signing secret).
   - Place Call needs the `calls` scope. A personal token rings the extension
     linked to its owner; an administrator, operator or service account without
     an extension limit may name any extension. Click-to-call limits and the
     emergency-number refusal apply as for any
     [click-to-call](click-to-call.md).
   - Find Contact needs `read`; Find Call needs `read` and a viewer or higher
     role.
     A token that is only for actions and searches does not need `admin` or `secrets`.
2. **Deploy the app** with the Zapier CLI (once, by whoever owns the Zapier
   account):

   ```sh
   cd integrations/zapier
   npm install -g zapier-platform-cli
   zapier login
   zapier register "Hello PBX"     # first time only
   zapier push
   zapier invite you@example.com   # or: zapier users:add / zapier promote
   ```

   Zapier gives you an invite link; open it to add the app to your account.

3. **Connect.** When a Zap asks for an account, enter the **Hello URL** (for
   example `https://pbx.example.com`) and the **API token**. Zapier checks it with
   `GET /api/v1/auth/me`. The token is sent only to that Hello URL.

## How the triggers work

When a Zap using a trigger is switched on, Zapier calls Hello's REST-hook
subscribe: `POST /api/v1/webhooks` with a name (`Zapier: Call Missed`), Zapier's
hook URL and the one event type. The call is idempotent on `(url, events)`, so a
repeated subscription returns the same endpoint. Switching the Zap off calls
`DELETE /api/v1/webhooks/{id}`; an endpoint that is already gone is not an
error. While you build a Zap, the sample events come from the event history
(`GET /api/v1/events?types=...&order=desc`), so the editor shows real calls.

The endpoints appear under **Integrations → Webhooks**, with the delivery log,
retries and dead letters of any [webhook](integrations.md#webhooks). Zapier hook
URLs are unguessable secrets and Zapier cannot verify a signature, so Hello's
`Hello-Signature` is not checked on this route; keep the hook URL private.

## Find Call

Find Call takes a **CDR ID** (the `data__cdrId` of Call Completed) or a
**number**. A number is matched against the caller and called number of the
latest 200 calls, newest first, comparing as written or on the last nine digits.
It finds nothing older than that window; use the CDR ID for an exact call.

## Other tools: plain webhooks

Any tool that can receive an HTTPS `POST` works without Zapier:

1. Create an endpoint under **Integrations → Webhooks**, or with
   `POST /api/v1/webhooks` (`{"name": "...", "url": "https://...", "events": ["call.missed"]}`).
   The signing secret is shown once.
2. The tool receives the [signed request](integrations.md#the-request). Verify
   `Hello-Signature` with the snippets in
   [verifying the signature](integrations.md#verifying-the-signature) (Go, Node
   and Python), dedupe on `Hello-Event-Id`, and answer `2xx` quickly.
3. To act on a call, use the REST API with a token: `POST /api/v1/calls/originate`
   to place one, `GET /api/v1/lookup/number?number=` to name a number, and
   `GET /api/v1/cdrs/{id}` for the record.

For a tool that cannot expose a public URL, read the
[live stream](integrations.md#the-live-stream) instead.

## Not covered

- Real-Zapier behaviour (an actual invite, a Zap run end to end) cannot be tested
  without a Zapier account; the app is covered by unit tests with a fake Zapier
  runtime (`cd integrations/zapier && node --test test/`).
- The app is a private integration. Publishing it in Zapier's public directory
  would need Zapier's review and is not planned.
