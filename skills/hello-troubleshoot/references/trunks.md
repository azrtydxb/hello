# Trunk states

| Registration state | Meaning                                                                                                         | What to do                                                                  |
| ------------------ | --------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------- |
| registered         | The carrier accepted Hello's REGISTER.                                                                          | Look at destinations and routes instead.                                    |
| registering        | A REGISTER is in flight or being retried.                                                                       | Wait one interval, then look again.                                         |
| failed             | The carrier refused; the last code says why (401/403: credentials, 404: unknown account, 408/503: unreachable). | Check the account with the carrier; update the password with `updateTrunk`. |
| misconfigured      | Hello cannot even try (missing username, realm or destination).                                                 | Fix the trunk configuration.                                                |
| disabled           | The trunk is switched off.                                                                                      | Enable it if it should carry calls.                                         |

IP-mode trunks have no registration; only destination health applies.

Destination health comes from OPTIONS pings: down means no answer or an
error code; the latency is from the last good ping. All destinations down
makes outbound calls on routes using only this trunk fail with 503.

Inbound calls from a carrier whose source address is outside the trunk's
source ranges are refused as an unknown trunk (403 in the routing trace).
