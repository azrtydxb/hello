# What failures and rejections mean

## Tool errors

| Status and code               | Meaning                                                                                                                                                  | What to do                              |
| ----------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------- |
| 400 `bad_request` with fields | A pattern, transform template, CIDR or destination is invalid (for example a bare `$1` instead of `${1}` in a template, or a regex over 500 characters). | Fix the named fields.                   |
| 409 `conflict`                | A name is taken, or a trunk or extension is still used by a route or group.                                                                              | Change the referrer first, then retry.  |
| 404 `not_found`               | A trunk, route or group id is stale.                                                                                                                     | List again.                             |
| 403 `insufficient_scope`      | The token lacks `write`.                                                                                                                                 | Ask the user to reconnect with `write`. |
| 403 `forbidden_role`          | The user is a viewer.                                                                                                                                    | An operator must do it.                 |

## Routing decisions

| Decision | Meaning                                                                                                                                                                                                               |
| -------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| internal | The number is an extension; no route was consulted.                                                                                                                                                                   |
| outbound | A route matched; the trunks are tried in order, failing over on the route's failover codes.                                                                                                                           |
| inbound  | An inbound route matched and names the destination.                                                                                                                                                                   |
| reject   | The call would be refused; the reject code and reason say why: no outbound or inbound route matched (404), no usable trunk on the matched route, an invalid rewritten number, or an unknown or disabled source trunk. |

When a decision is surprising, read the trace from the top: the first route
that matched is the answer, and a broader route above the intended one is the
usual cause.
