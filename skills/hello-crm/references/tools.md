# Tools for CRM connectors

| Tool                | Scope | Role     | What it does                                                                          |
| ------------------- | ----- | -------- | ------------------------------------------------------------------------------------- |
| `listCRMConnectors` | admin | admin    | Connectors with capabilities and connection state (no secrets).                       |
| `getCRMConnector`   | admin | admin    | One connector, with the redirect URI to register in the CRM app.                      |
| `listCRMUsers`      | admin | admin    | Hello users and the CRM user each one's calls are logged for.                         |
| `listCRMLogs`       | write | operator | Logging jobs of a connector, newest first, filterable by `status`, paged by `before`. |

Not available over MCP (console only): creating or changing a connector,
signing in, disconnecting, testing the connection, retrying a job, and setting
a manual user mapping.
