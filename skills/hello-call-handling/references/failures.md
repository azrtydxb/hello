# Failures

| Answer                                        | Meaning and fix                                                                                                     |
| --------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| 400 with `error.fields`                       | Each failing field is listed; fix those and resend the whole body.                                                  |
| 400 on a queue failover                       | It leads back to the queue, or names an extension, announcement or group that does not exist.                       |
| 400 on a park lot                             | The slot range overlaps another lot, an extension number or a ring group name.                                      |
| 403                                           | The call needs `admin` (conferences, paging, multicast, messaging settings) or `operator`.                          |
| 409 on a delete or rename                     | A route, ring group, IVR menu, queue or phone group names it; the message says which.                               |
| 409 on a multicast channel                    | Its kind cannot change, or it cannot be deleted, while a group, paging group or phone uses it.                      |
| 503 on `listParkedCalls` or `listPresence`    | The live state store is unavailable; try again shortly and check `getCluster`.                                      |
| `sendMessage` answers 202 but nothing arrives | The recipient has no registered phone yet, messaging is off in `getMessageSettings`, or the trunk has no messaging. |
