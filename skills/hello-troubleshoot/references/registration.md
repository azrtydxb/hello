# Registration verdicts

`getDeviceDiagnostics` returns a verdict (absent while the device is
registered) with a code and a message computed from what Hello measured.

| Verdict code         | Meaning                                                                                                  | What to do                                                                                                     |
| -------------------- | -------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------- |
| disabled             | The device is disabled; Hello ignores its REGISTERs.                                                     | Enable it with `updateDevice` (hello-setup) if it should register.                                             |
| throttled            | The source IP is blocked after too many failed authentications; Hello answers 403 until the window ends. | Fix the credentials first, then unblock with `clearAuthFailures`.                                              |
| no_register          | No REGISTER arrived in the retention window.                                                             | The phone does not reach Hello: wrong server or port, firewall or NAT, or the phone is off. Hello is UDP only. |
| expired              | The last REGISTER was accepted but the binding has expired.                                              | The phone stopped refreshing; check its network and registration interval.                                     |
| stale_nonce          | Hello answered 401 with stale=true and the phone did not retry.                                          | Usually transient; reboot the phone if it repeats.                                                             |
| auth_failed          | The credentials were wrong (401 after a challenge).                                                      | The administrator rotates the device secret in the console, or the phone re-provisions.                        |
| challenge_unanswered | The phone never answered the digest challenge.                                                           | The phone has no password configured.                                                                          |
| forbidden            | Hello refused the REGISTER with 403.                                                                     | Read the message; often a SIP domain mismatch.                                                                 |
| interval_too_brief   | The phone asked for an interval below Hello's minimum (423).                                             | Raise the phone's registration interval.                                                                       |
| unavailable          | Hello answered 503: the node was not ready or Valkey was unreachable.                                    | Check `getCluster`.                                                                                            |
| rejected             | Any other final response; the message has the code.                                                      | Look up the code in the attempts list.                                                                         |

The attempts list shows each REGISTER's source, node, user agent, whether it
carried credentials and the response code. Attempts are kept only for a
retention window, so ask the user to re-register the phone when it is empty.
