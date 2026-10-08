# Cluster states

Hello runs SIP nodes and control nodes. Each publishes a membership record
with a state.

| State     | Meaning                                           | Effect                                                                     |
| --------- | ------------------------------------------------- | -------------------------------------------------------------------------- |
| JOINING   | The node is starting and loading configuration.   | It takes no new calls yet.                                                 |
| READY     | Healthy.                                          | Normal.                                                                    |
| DRAINING  | An administrator asked it to drain.               | It finishes its calls and takes no new ones; phones re-register elsewhere. |
| UNHEALTHY | A dependency check failed; the reason says which. | Calls go to other nodes.                                                   |
| OFFLINE   | Its heartbeat expired.                            | Its registrations move as phones re-register.                              |

- **Revision lag**: the configuration revision minus the node's. Above zero for
  more than a few seconds means the node has not picked up the latest change,
  so a routing change may not apply on it yet.
- **PostgreSQL down**: configuration cannot change and management calls
  answer 503 `unavailable`; nodes keep routing from the configuration they
  loaded.
- **Valkey down**: live views (registrations, calls, presence) answer 503 and
  the members list is empty; calls still route from each node's loaded
  configuration.
