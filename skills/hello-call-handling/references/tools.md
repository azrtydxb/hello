# Tools for call handling

| Tool                        | Scope | What it does                                                       |
| --------------------------- | ----- | ------------------------------------------------------------------ |
| `listQueues`                | read  | Queues with their members.                                         |
| `getQueue`                  | read  | One queue.                                                         |
| `createQueue`               | write | Create a queue.                                                    |
| `putQueue`                  | write | Replace a queue including members.                                 |
| `deleteQueue`               | write | Delete; refused while something names it.                          |
| `listQueueStatus`           | read  | Live waiting callers and agent states.                             |
| `listQueueAgents`           | read  | Agents with login and pause state.                                 |
| `setQueueAgent`             | write | Log an agent in or out, pause or resume.                           |
| `listConferences`           | read  | Rooms with live state; PINs never returned.                        |
| `getConference`             | read  | One room with who is in it.                                        |
| `createConference`          | admin | Create a room.                                                     |
| `updateConference`          | admin | Replace a room's settings.                                         |
| `deleteConference`          | admin | Delete a room not used by a route.                                 |
| `controlConference`         | write | Mute, kick, lock or end a live room.                               |
| `listParkLots`              | read  | Park lots.                                                         |
| `getParkLot`                | read  | One lot.                                                           |
| `createParkLot`             | write | Create a lot with a slot range.                                    |
| `putParkLot`                | write | Replace a lot.                                                     |
| `deleteParkLot`             | write | Delete a lot.                                                      |
| `listParkedCalls`           | read  | Calls parked right now.                                            |
| `listPickupGroups`          | read  | Pickup groups.                                                     |
| `getPickupGroup`            | read  | One group.                                                         |
| `createPickupGroup`         | write | Create a group of extensions.                                      |
| `putPickupGroup`            | write | Replace a group.                                                   |
| `deletePickupGroup`         | write | Delete a group.                                                    |
| `listPagingGroups`          | read  | Paging groups.                                                     |
| `getPagingGroup`            | read  | One group.                                                         |
| `createPagingGroup`         | admin | Create a group.                                                    |
| `putPagingGroup`            | admin | Replace a group.                                                   |
| `deletePagingGroup`         | admin | Delete a group.                                                    |
| `listMulticastChannels`     | read  | The multicast channel catalogue.                                   |
| `getMulticastChannel`       | read  | One channel.                                                       |
| `createMulticastChannel`    | admin | Add a channel.                                                     |
| `putMulticastChannel`       | admin | Replace a channel.                                                 |
| `deleteMulticastChannel`    | admin | Delete a channel.                                                  |
| `getMessageSettings`        | admin | Messaging switch, TTL, retention, limits.                          |
| `updateMessageSettings`     | admin | Replace the messaging settings.                                    |
| `sendMessage`               | write | Queue a text message to an extension, group, everyone or a number. |
| `listPresence`              | read  | Live state of every device.                                        |
| `listVoicemailBoxes`        | read  | Boxes with message counts.                                         |
| `listVoicemailMessages`     | read  | Messages of a box.                                                 |
| `markVoicemailMessageHeard` | write | Mark a message heard or unheard.                                   |
| `deleteVoicemailMessage`    | write | Delete a message and its audio.                                    |
| `listRecordings`            | read  | Call recordings, newest first.                                     |
| `deleteRecording`           | write | Delete a recording and its audio.                                  |
