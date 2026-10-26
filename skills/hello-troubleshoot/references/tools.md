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
| `drainNode`            | admin | Ask a member to finish its calls and take no new ones.               |
| `undrainNode`          | admin | Cancel a drain.                                                      |
| `getVersion`           | read  | Build, commit and configuration revision of the answering node.      |
| `getMe`                | read  | Who the credentials belong to, with role and scopes.                 |
| `cdrConcurrency`       | read  | How many calls were up at once over 1h, 6h or 24h.                   |
| `listSkills`           | read  | The skills this server ships.                                        |
| `getAIStatus`          | read  | Whether the AI agent is on, budget and open counts.                  |
| `getAISettings`        | read  | External AI access, MCP and API URLs, what each scope allows.        |
| `listAIFindings`       | read  | Findings raised by the detectors, ranked.                            |
| `getAIFinding`         | read  | One finding with evidence and history.                               |
| `listAIProposals`      | read  | Changes the AI agent proposed; none apply by themselves.             |
| `getAIProposal`        | read  | One proposal with before, after and current state.                   |
| `listAIAgents`         | read  | Background agents with last and next run.                            |
| `listAISessions`       | read  | The caller's assistant sessions.                                     |
| `getAISession`         | read  | One session with its messages.                                       |
| `getAITask`            | read  | An AI task's status.                                                 |
| `getTtsSettings`       | admin | Whether a text-to-speech key is set, default model and voice.        |
| `listTtsVoices`        | write | Models and voices offered by the provider.                           |
| `deleteTtsKey`         | admin | Remove the stored text-to-speech key.                                |
