# Slack notifications

Hello can post **missed calls** and **new voicemail messages** to a Slack
channel (spec `.procoder/specs/integrations.md`, phase P17). You create the
Slack app in your own workspace; Hello keeps its credential sealed (AES-256-GCM
under `HELLO_SECRET_KEY`, bound to the destination) and never shows it again,
logs it or sends it over MCP.

A post carries the caller's number and name as the carrier sent them, the
extension that was called and, for a voicemail, how long the message is. It
never carries audio, a recording link or SIP data.

## Set it up

Open **Integrations, Slack** and **Add a Slack channel**. Pick one of two ways.

**Incoming webhook** (simplest, one channel):

1. In Slack create an app (from scratch), turn on **Incoming Webhooks** and
   **Add New Webhook to Workspace**, choosing the channel.
2. Copy the URL (`https://hooks.slack.com/services/...`) into Hello as the
   **Webhook URL**. Hello only accepts a `hooks.slack.com` URL.

**Bot token** (one app, any channel the bot is in):

1. Create an app, add the bot scope `chat:write`, install it to the workspace
   and copy the **Bot User OAuth Token** (`xoxb-...`).
2. Invite the bot to the channel (`/invite @name`).
3. In Hello choose **Bot token**, paste the token and enter the channel (a name
   such as `#phone`, or an id such as `C0123456789`).

Then choose what to post (missed calls, voicemail messages or both) and,
optionally, limit it to some extensions, and press **Send a test message**.

## What a post looks like

> :telephone_receiver: Missed call from **Acme Ltd** (+3250123456) to
> **Reception** (200) (nobody answered)
>
> :mailbox_with_mail: New voicemail for **Reception** (200) from
> **+3250123456**, 1 min 23 s

Each post links to the call history or the voicemail box when
`HELLO_PUBLIC_URL` is set. Names and numbers come from a caller and are
untrusted: Hello escapes them for Slack, so a caller cannot ping `@channel`,
add a link or break the formatting, and each is cut to 80 characters.

## Delivery

Posts are queued in the same transaction that stores the event, so one is never
lost between the call and Slack, and a repeated event posts once. A worker on
every control replica sends the due posts, at most about one a second to a
destination (Slack's limit).

A failed post is retried on the webhook schedule (10 s, 30 s, 2 min, 10 min,
1 h, 4 h, 12 h, each jittered; eight attempts in about 17 hours), honouring
Slack's `Retry-After`. After that it is **dead** and stays in the **Posts** tab
with its last error until you press **Retry** (dead posts are kept 30 days,
finished ones 7).

| Slack says                                                      | Hello does                                                     |
| --------------------------------------------------------------- | -------------------------------------------------------------- |
| 200 (`ok`, or `{"ok":true}` for a bot)                          | Done.                                                          |
| `429`, `5xx`, `ratelimited`, `internal_error`, a network error  | Retries later.                                                 |
| `channel_not_found`, `not_in_channel`, `is_archived`, other 4xx | The post is dead (the channel stays on; fix and press Retry). |
| `invalid_auth`, `token_revoked`, `401`, `403`                   | The post is dead and the channel is switched off (`auth_failed`). A new token switches it on again. |
| `404` or `410` from a webhook                                   | The post is dead and the channel is switched off (`destination_gone`). |
| 50 failed posts in a row                                        | The channel is switched off (`consecutive_failures`).          |

The `hello_slack_posts_total{result}` metric counts attempts.

## API

Admin routes under `/api/v1/integrations/slack` (list, create, get, patch,
delete, `POST .../{id}/test`, `GET .../{id}/posts`,
`POST .../posts/{id}/retry`), listed in `/api/v1/openapi.json`. The credential is
the write-only `secret` field. They are not offered over MCP (the credential is
managed in the console).

Missed calls and voicemail messages are the events `call.missed` and
`voicemail.received` ([events](integrations.md#what-is-emitted)); anything else
that wants them (Zapier, n8n, Make, a script) uses a
[webhook](integrations.md#webhooks) instead.

## Not covered

- A real Slack workspace: the posting is tested against a fake Slack that
  answers as Slack documents (webhook text answers, `ok:false` codes,
  `Retry-After`); the Block Kit message and the date token have not been seen in
  a real channel. Check the test message in yours.
- Threads, buttons and replies. Hello posts; it does not read Slack.
