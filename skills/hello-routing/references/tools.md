# Tools for routing

| Tool                    | Scope | What it does                                                                                                 |
| ----------------------- | ----- | ------------------------------------------------------------------------------------------------------------ |
| `testRouting`           | read  | Decide a call against the saved configuration without placing it; returns the decision and a numbered trace. |
| `listTrunks`            | read  | Every trunk; never its password.                                                                             |
| `getTrunk`              | read  | One trunk.                                                                                                   |
| `createTrunk`           | write | Create a registration or IP trunk.                                                                           |
| `updateTrunk`           | write | Change a trunk; an omitted password keeps the stored one.                                                    |
| `deleteTrunk`           | write | Delete a trunk; refused while a route uses it.                                                               |
| `listTrunkStatus`       | read  | Live registration state, destination health and active calls per trunk.                                      |
| `listOutboundRoutes`    | read  | Outbound routes in position order.                                                                           |
| `getOutboundRoute`      | read  | One outbound route.                                                                                          |
| `createOutboundRoute`   | write | Append an outbound route.                                                                                    |
| `updateOutboundRoute`   | write | Change an outbound route.                                                                                    |
| `deleteOutboundRoute`   | write | Delete an outbound route.                                                                                    |
| `reorderOutboundRoutes` | write | Set the order of every outbound route at once.                                                               |
| `listInboundRoutes`     | read  | Inbound routes in position order.                                                                            |
| `getInboundRoute`       | read  | One inbound route.                                                                                           |
| `createInboundRoute`    | write | Append an inbound route.                                                                                     |
| `updateInboundRoute`    | write | Change an inbound route.                                                                                     |
| `deleteInboundRoute`    | write | Delete an inbound route.                                                                                     |
| `reorderInboundRoutes`  | write | Set the order of every inbound route at once.                                                                |
| `listRingGroups`        | read  | Ring and hunt groups.                                                                                        |
| `getRingGroup`          | read  | One group.                                                                                                   |
| `createRingGroup`       | write | Create a group.                                                                                              |
| `updateRingGroup`       | write | Change a group.                                                                                              |
| `deleteRingGroup`       | write | Delete a group.                                                                                              |
| `listFeatureCodes`      | read  | DTMF feature codes.                                                                                          |
| `putFeatureCodes`       | write | Replace every feature code.                                                                                  |
| `listExtensions`        | read  | Extensions, for destinations and members.                                                                    |
