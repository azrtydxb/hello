# Tools for troubleshooting

| Tool                   | Scope | What it does                                                         |
| ---------------------- | ----- | -------------------------------------------------------------------- |
| `getCluster`           | read  | Members, PostgreSQL and Valkey health, configuration revision.       |
| `listClusterNodes`     | read  | Every member, live and offline.                                      |
| `listCDRs`             | read  | Call records, newest first, filterable.                              |
| `countCDRs`            | read  | How many calls exist and how many failed.                            |
| `getCDR`               | read  | One call with its routing trace and failure explanation.             |
| `listCalls`            | read  | Calls in progress across the cluster.                                |
| `testRouting`          | read  | Decide a call against the saved configuration without placing it.    |
| `listRegistrations`    | read  | Every registered contact.                                            |
| `listDevices`          | read  | Devices, to find a device id.                                        |
| `getDeviceDiagnostics` | read  | Recent REGISTER attempts and the verdict for one device.             |
| `listAuthFailures`     | read  | Source IPs with failed authentications and whether they are blocked. |
| `clearAuthFailures`    | write | Unblock a source IP.                                                 |
| `listTrunks`           | read  | Trunks.                                                              |
| `getTrunk`             | read  | One trunk's configuration.                                           |
| `listTrunkStatus`      | read  | Registration, destination health and active calls per trunk.         |
| `listPhones`           | read  | Provisioned phones.                                                  |
| `listPhoneFetches`     | read  | A phone's provisioning fetches.                                      |
| `getProvSettings`      | read  | Provisioning URLs and DHCP option values.                            |
| `listPresence`         | read  | Device presence (idle, ringing, on call, DND).                       |
