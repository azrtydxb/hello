# Tools for directory sync

Tool names are the API's operation ids. Every directory tool needs the
`admin` scope and the `admin` role. Setting up connectors, testing them,
starting runs, applying and confirming are done in the console, never over
MCP: they carry the bind password or change who can use the PBX.

| Tool                      | Scope | What it does                                                                                       |
| ------------------------- | ----- | -------------------------------------------------------------------------------------------------- |
| `listDirectoryConnectors` | admin | Every connector with its status, last success and the number of users it manages.                  |
| `getDirectoryConnector`   | admin | One connector: config, mappings, group rules, options; never the bind password.                    |
| `listDirectoryRuns`       | admin | A connector's runs, newest first (`limit`).                                                        |
| `getDirectoryRun`         | admin | One run: status, counts by action, error code, diff hash.                                          |
| `listDirectoryChanges`    | admin | A run's rows: action, user, field before and after, reason. Filter by `action`; page with `after`. |
