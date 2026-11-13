# Tools for CRM connectors

| Tool                  | Scope | Role     | What it does                                                                                 |
| --------------------- | ----- | -------- | -------------------------------------------------------------------------------------------- |
| `listCRMConnectors`   | admin | admin    | Connectors with capabilities and connection state (no secrets).                              |
| `getCRMConnector`     | admin | admin    | One connector, with the redirect URI to register in the CRM app.                             |
| `getCRMSetup`         | admin | admin    | What the customer configures in the CRM: call center definition, channel provider.           |
| `listCRMUsers`        | admin | admin    | Hello users and the CRM user each one's calls are logged for.                                |
| `listCRMLogs`         | write | operator | Logging jobs of a connector, newest first, filterable by `status`, paged by `before`.        |
| `getSlackDestination` | admin | admin    | A Slack destination with its health and the count of pending and dead posts (no credential). |
| `listSlackPosts`      | admin | admin    | Posts of a Slack destination, newest first, filterable by `status`.                          |
| `listMyConnections`   | read  | user     | The caller's own Outlook and Google connections and calendar DND state.                      |

Not available over MCP (console only): creating or changing a connector,
signing in, disconnecting, Slack destinations and credentials, personal sign-ins and the calendar DND choice, testing the connection, retrying a job, and setting
a manual user mapping.
