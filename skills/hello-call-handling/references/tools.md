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
| `listQueueCallbacks`        | read  | Open callback requests of a queue.                                 |
| `cancelQueueCallback`       | write | Cancel one callback request.                                       |
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
| `listFaxDestinations`       | read  | Fax destinations: an owner extension and an optional email.        |
| `getFaxDestination`         | read  | One fax destination.                                               |
| `createFaxDestination`      | admin | Create a fax destination for routes and IVR keys to name.          |
| `updateFaxDestination`      | admin | Change a destination's owner and email (not its name).            |
| `deleteFaxDestination`      | admin | Delete a destination no route or IVR key names.                    |
| `listFaxes`                 | read  | Received and sent faxes, newest first; filter direction, status.   |
| `getFax`                    | read  | One fax with its state.                                            |
| `sendFax`                   | write | Queue a PDF to send to an E.164 number.                            |
| `deleteFax`                 | write | Delete a fax and its PDF, or cancel one being sent.                |
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
