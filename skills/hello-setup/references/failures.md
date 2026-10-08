# What failures mean

Tool errors carry the API's status, error code, message and, for validation
failures, every failing field.

| Status and code               | Meaning                                                                                                       | What to do                                                                   |
| ----------------------------- | ------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------- |
| 400 `bad_request` with fields | A value is invalid (number format, SIP username characters, MAC).                                             | Fix the named fields and retry; do not guess around them.                    |
| 404 `not_found`               | The id does not exist (any more).                                                                             | List again and use a current id.                                             |
| 409 `conflict`                | The number, SIP username or MAC is taken, or the item is in use (an extension a route or ring group targets). | Tell the user what clashes; pick another value or change the referrer first. |
| 403 `insufficient_scope`      | Your token lacks the scope (usually `write`).                                                                 | Ask the user to reconnect and grant `write`.                                 |
| 403 `forbidden_role`          | The user's role is below the operation's.                                                                     | An operator or administrator must do it.                                     |
| 401                           | The grant was revoked or expired.                                                                             | Reconnect; the client re-runs consent.                                       |
| 503 `unavailable`             | PostgreSQL or Valkey is down.                                                                                 | Use the hello-troubleshoot skill to check the cluster.                       |

A secret value shown as withheld is expected: it is never sent to an agent.
