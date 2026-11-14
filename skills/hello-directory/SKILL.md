---
name: hello-directory
description: Inspect directory sync on a Kuvryn Hello PBX - the Active Directory / LDAP connectors that create, update and disable users from a company directory - and explain a run's preview, conflicts and guards to an administrator. Use when someone asks why a user was created, changed or disabled by the directory, what a sync would do, why a run is waiting for confirmation, or which fields a directory owns.
license: Apache-2.0
metadata:
  hello-version: "v1"
---

# Hello directory sync

A **directory connector** reads users (and groups) from an Active Directory
or LDAP server on a schedule and keeps Hello's users in step: it creates a
user, links it to the extension named by an attribute, sets role and ring-group
membership from group rules, and disables the user when the directory drops or
disables the account. It never deletes and never writes to the directory.

This skill is read-only. You look and explain; the administrator sets up
connectors, tests them, previews, applies and confirms in the console
(**Integrations → Directory**). The bind password is never visible to you.

## Connecting

- Scopes: `admin` (every directory tool needs it). Your user must be an
  administrator.
- `listDirectoryConnectors` and `getDirectoryConnector` show a connector's
  settings, mappings, group rules, status and the number of users it manages.
  `hasCredential` says a bind password is stored; its value is never returned.

## What a run is

Every run has a kind (`preview`, `apply`, `scheduled`) and a status:

| Status               | Meaning                                                                                   |
| -------------------- | ----------------------------------------------------------------------------------------- |
| `queued`, `running`  | Waiting for, or being run by, the replica that holds the connector's lock.                |
| `preview`            | Finished a dry run; nothing was changed. Valid for 15 minutes.                            |
| `succeeded`          | Applied. Rows with action `error` failed individually; the rest went through.             |
| `needs_confirmation` | Halted before applying anything by a guard (see below). A person must review and confirm. |
| `failed`             | The source could not be read; `errorCode` says why. Nothing was changed.                  |
| `stale`              | A preview replaced by a fresher one because the source changed or it aged.                |

`listDirectoryRuns` lists a connector's runs, newest first; `getDirectoryRun`
reads one with its `counts` by action. `listDirectoryChanges` reads the rows:
for each user the `action` (`create`, `update`, `adopt`, `disable`, `enable`,
`conflict`, `skip`, `error`), the field-level `fieldDiffs` (before and after)
and the `reason`. Filter by `action`, page with `after` and `nextAfter`.

## Reading the reasons

- **Conflicts** leave that user unchanged while the rest of the run goes on:
  `extension_linked` (another user has the extension; the administrator may
  override it on apply), `duplicate_extension` (two source users map to one
  number), `extension_missing` or `extension_invalid` (the mapped number does
  not exist or is not 2 to 10 digits), `extension_kind` (it is a door
  station), `email_in_use`, `username_collision`, `username_invalid`,
  `claimed_by_other_connector`, `last_admin` (the change would leave no
  enabled admin).
- **Skips**: `inactive_in_source` (a disabled account that Hello does not
  know is not created), `absent_pending` (missing from the source, counted
  `1/2`: a user is disabled only after being absent from two complete runs in
  a row), `link_only_no_match`, `local_admin_not_adopted` (a local admin is
  never adopted, demoted or disabled by sync).
- **Adopt** rows list a local user the connector would take over, matched by
  email, then username. They apply only when the administrator confirms them.
- **Disable** reasons: `inactive_in_source` (immediate) or
  `absent_from_source` (after the absence rule). A disabled user keeps its
  extension, voicemail and call records; its sessions and tokens end and, by
  default, its devices are switched off. A user that reappears is enabled
  again.

## The guards

A run halts as `needs_confirmation` and applies nothing when it would disable
more than 20 percent of the linked users (and more than 5), or when the source
returned no users although users are linked (`errorCode` `empty_result`). That
usually means a wrong base DN, an empty filter or a partial outage: tell the
administrator to look at the changes first and confirm only if they are right.

## Which fields a directory owns

A user managed by a connector carries `managedBy` with the connector and the
fields it owns (`username`, `email`, `displayName`, `extension`, `role`,
`enabled`). Those are read-only in the console and the API (409
`managed_by_directory`); change them in the directory. Fields the mappings do
not cover stay editable. A directory user has no local password.

## When something fails

`lastErrorCode` on the connector and `errorCode` on a run are classified:
`auth_failed` (bind refused), `tls_untrusted` (the server certificate is not
trusted: supply the CA certificate or the server name), `unreachable`,
`timeout`, `quota` (the server's size limit: lower the page size),
`bad_filter` (base DN or filter rejected), `user_cap_exceeded`, `replica_lost`.
Nothing is changed by a failed run, and after three in a row the
administrators are e-mailed when SMTP is set up.

## References

- [Tools for directory sync](references/tools.md) - every tool this skill uses, with its scope.
