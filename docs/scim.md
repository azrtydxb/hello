# SCIM 2.0 provisioning

Hello is a SCIM 2.0 service provider (RFC 7643 and RFC 7644). An identity
provider (Microsoft Entra ID, Okta, JumpCloud, Google Workspace through a
SCIM-capable app) pushes users and groups to Hello, and Hello keeps its own
users, their extension links, roles and ring-group memberships in step, with
the same mapping, group rules and modes as a [directory sync](integrations.md#directory-sync-active-directory-and-ldap).
Hello never deletes a user it was given: a SCIM `DELETE` or `active: false`
disables it.

Everything is under **Integrations > Directory** (administrators only), on a
connector of kind **SCIM**, and `/api/v1/directory/*`. The protocol itself is
served at `/scim/v2`, outside `/api/v1`, and needs `HELLO_PUBLIC_URL` (without
it `/scim/v2` answers `404` and no token can be issued).

## Setting it up

1. **New SCIM connector.** Give it a name and, if needed, change the mapping
   and the group rules (below), and choose how its users sign in: pushed users
   have no local password, so an enabled SCIM connector needs single sign-on
   with Microsoft or Google (`options.loginMethod` `oidc`, `oidcProvider`
   `entra` or `google`), set up under Single sign-on first (see
   [integrations](integrations.md)). It is enabled by default; switched off,
   every SCIM request answers `403`.
2. **Issue token.** The console shows the base URL
   (`<HELLO_PUBLIC_URL>/scim/v2`) and a token `hello_scim_…` once. Hello keeps
   only its SHA-256; the token is valid for `/scim/v2` of this connector and
   carries no API scope, so it cannot call `/api/v1`. A connector has at most
   two active tokens: to replace one, issue a new token, put it in the identity
   provider, then revoke the old one. Issuing needs the `secrets` scope;
   issuing and revoking are audited.
3. Configure the identity provider (below), test its connection and start
   provisioning.
4. Watch the **SCIM** panel of the connector (pushed users and groups, the last
   request, the guard) and its runs: every change a SCIM request makes is
   recorded on the connector's SCIM run of the hour, with the same actions and
   reasons as a sync run.

### Microsoft Entra ID

Enterprise applications > New application > Create your own application
("Integrate any other application you don't find in the gallery"). Then
Provisioning > Get started:

- Provisioning Mode: Automatic.
- Tenant URL: the base URL, for example
  `https://hello.example.com/scim/v2`.
- Secret Token: the `hello_scim_…` token. **Test Connection** must succeed.
- Mappings > Provision Microsoft Entra ID Users: keep `userPrincipalName ->
userName`, `Switch([IsSoftDeleted]...) -> active`, `displayName`, `mail ->
emails[type eq "work"].value`, `objectId -> externalId`. To give Hello the
  extension, add a mapping to
  `urn:hello:params:scim:schemas:extension:pbx:2.0:User:extension` (add it under
  "Edit attribute list for customappsso" first), for example from
  `extensionAttribute1`.
- Provision Microsoft Entra ID Groups: on when group rules are used.
- Scope: "Sync only assigned users and groups" and assign them, then turn
  Provisioning Status on.

Entra sends `PATCH` with capitalised operations (`"Replace"`), string booleans
(`"False"`), filtered paths (`emails[type eq "work"].value`) and member removals
that list the members; Hello accepts all of them. It disables a user with
`active: "False"` and deletes it after the soft-delete period; both disable the
Hello user.

### Okta

Applications > Create App Integration > SAML 2.0 or SWA (any sign-on), then
General > Provisioning: SCIM. On the Provisioning tab:

- SCIM connector base URL: the base URL.
- Unique identifier field for users: `userName`.
- Supported provisioning actions: Push New Users, Push Profile Updates, Push
  Groups.
- Authentication Mode: HTTP Header, Authorization: the token (Okta adds
  `Bearer`).
- **Test Connector Configuration**, then To App: enable Create Users, Update
  User Attributes and Deactivate Users.

To send the extension, add an attribute to the app's profile in the Profile
Editor with the external name `extension` and the external namespace
`urn:hello:params:scim:schemas:extension:pbx:2.0:User`.

Okta deactivates with a no-path `PATCH` (`{"op": "replace", "value": {"active":
false}}`), updates a profile with `PUT`, renames a group with a no-path replace
that repeats the id, and removes members with `members[value eq "…"]`.

### JumpCloud and others

Any client of RFC 7644 works: base URL, bearer token, `userName` as the unique
field. `/ServiceProviderConfig`, `/ResourceTypes` and `/Schemas` describe what
Hello supports.

## What Hello supports

| Endpoint                                | Methods                         |
| --------------------------------------- | ------------------------------- |
| `/Users`                                | `GET` (list, filter), `POST`    |
| `/Users/{id}`                           | `GET`, `PUT`, `PATCH`, `DELETE` |
| `/Groups`                               | `GET` (list, filter), `POST`    |
| `/Groups/{id}`                          | `GET`, `PUT`, `PATCH`, `DELETE` |
| `/ServiceProviderConfig`                | `GET`                           |
| `/Schemas`, `/Schemas/{id}`             | `GET`                           |
| `/ResourceTypes`, `/ResourceTypes/{id}` | `GET`                           |
| `/Bulk`, `/Me`, `/.search`              | `501`                           |

- **Filters**: `eq`, `ne`, `co`, `sw`, `ew`, `gt`, `ge`, `lt`, `le`, `pr`,
  `and`, `or`, `not`, parentheses and value paths (`emails[type eq "work"]`,
  `members[value eq "…"]`), on any attribute, including extension-qualified
  ones. Strings compare without regard to case except `id` and `externalId`.
  A bad filter answers `400 invalidFilter`.
- **Paging**: `startIndex` (1-based) and `count` (default 100, capped at 200;
  `count=0` returns only `totalResults`). No sorting.
- **attributes** and **excludedAttributes** on reads (Entra asks groups with
  `excludedAttributes=members`).
- **PATCH**: `add`, `replace`, `remove`, with or without `path`, value-path
  filters and sub-attributes. A replace of a filtered value that does not exist
  yet adds it.
- **ETags**: `meta.version` and the `ETag` header are `W/"<n>"`; `If-Match` on
  `PUT`, `PATCH` and `DELETE` answers `412` when the resource changed;
  `If-None-Match` on `GET` answers `304`.
- **Schemas**: the core User and Group, the enterprise User extension and
  Hello's `urn:hello:params:scim:schemas:extension:pbx:2.0:User` with
  `extension`. Unknown attributes are ignored; `password` is never stored or
  returned (a provisioned user signs in through its identity provider);
  `groups` on a user is read-only (set through the groups).
- Responses are `application/scim+json`; errors carry `status`, `scimType`
  (`uniqueness`, `invalidValue`, `invalidFilter`, `invalidPath`, `noTarget`,
  `mutability`, `invalidSyntax`) and `detail`.

## How a push becomes a Hello user

Hello keeps each pushed resource as it was sent, and every write runs the
directory engine on it: the connector's mapping turns the SCIM attributes into
username, e-mail, display name and extension, the group rules into a role and
ring groups, and the mode decides what Hello may overwrite. The user's SCIM
`id` (a UUID Hello assigns) is its stable link.

The mapping reads these attribute names:

| Name                                                                | Value                                                              |
| ------------------------------------------------------------------- | ------------------------------------------------------------------ |
| `userName`, `externalId`, `displayName`, `title`, `nickName`, …     | the attribute                                                      |
| `name.givenName`, `name.familyName`, `name.formatted`               | the sub-attribute                                                  |
| `name.full`                                                         | given and family name joined                                       |
| `emails`, `emails[primary]`, `emails[work]`, …                      | every value (primary first), the primary one, the one of that type |
| `phoneNumbers`, `phoneNumbers[work]`, `phoneNumbers[mobile]`, …     | likewise                                                           |
| `active`                                                            | `true` or `false` (absent is `true`)                               |
| `urn:ietf:params:scim:schemas:extension:enterprise:2.0:User:<attr>` | an enterprise attribute (`manager` is its id)                      |
| `urn:hello:params:scim:schemas:extension:pbx:2.0:User:extension`    | Hello's extension attribute                                        |

The defaults are `userName`; `emails[primary]`, `emails[work]`, `emails`;
`displayName`, `name.formatted`, `name.full`; Hello's extension attribute; and
`active` as a boolean. To take the extension from the work phone, add
`phoneNumbers[work]` with "keep only digits" and "keep the last N digits". The
extension must exist in Hello (`400 invalidValue` otherwise); range
auto-provisioning is a later phase. **Test** on the connector lists the pushed
users with every attribute name above and the mapped result, so a mapping can
be checked before it is saved.

Group rules name a pushed group by its Hello id or by its `externalId` (Entra
sends the group's object id). Adding or removing a member, renaming a group's
`externalId` or deleting a group re-plans its members at once. A rule granting
`admin` is ignored unless **Allow a group to grant admin** is on; SCIM `roles`
never give a Hello role.

A changed mapping or rule applies to the users pushed later; to re-map every
pushed user at once, **Preview now** and apply the run, which reads the stored
resources (the source of a SCIM connector).

### What a push refuses

A push that the engine would turn into a conflict is refused, so the identity
provider shows the error and retries after it is fixed; nothing is changed:

| Answer             | When                                                                                                                                                                 |
| ------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `409 uniqueness`   | another resource of the connector has the `userName`, `displayName` (groups) or `externalId`                                                                         |
| `409 uniqueness`   | a Hello user with the username or e-mail exists and is managed by another connector, or is another SCIM user                                                         |
| `409 uniqueness`   | a local user matches and **Take over matching local users** is off; or a local administrator matches (never taken over)                                              |
| `409 uniqueness`   | the extension is linked to another user, or another pushed user has it                                                                                               |
| `400 invalidValue` | the extension is not 2 to 10 digits, does not exist, or is a door station; the username has characters Hello does not allow; a member is not a user of the connector |
| `400 mutability`   | the change would disable or demote the last enabled administrator                                                                                                    |

**Take over matching local users** (`adoptExisting`, off by default) lets a
pushed user take over a local, non-admin Hello user with the same e-mail
address, then username. A sync shows such a user in a preview for an
administrator to tick; a push has no preview, so the switch is the
confirmation. The taken-over user keeps its row; its mapped fields become
managed by the connector.

A user created with `active: false` is kept as a resource without a Hello user
until it becomes active. A `DELETE` keeps the resource as deleted (it answers
`404`) and disables the Hello user; the same `userName` or `externalId` pushed
again revives it with the same id and enables the same Hello user.

## The mass-deprovision guard

There is no preview for a push, so the guard counts the users SCIM disabled in
the last hour. When a request would take that count over 20 percent of the
linked users (at least 5), Hello refuses it with `429`, applies nothing, pauses
the connector and records a run that needs confirmation. While paused, every
write answers `429` (`Retry-After: 300`); reads still work. The console shows
the pause; **Resume SCIM** (or confirming the run in the Runs list, or
`POST /api/v1/directory/runs/{id}/confirm`) resumes writes and restarts the
hour. The refused requests are not replayed: the identity provider sends them
again (Entra on its next cycle, Okta when its task is retried). This protects
against a scoping change or an unassigned group that would disable half the
company.

## Limits and security

- 600 requests per minute per token (per `hello-control` replica), then `429`
  with `Retry-After: 60`.
- Bodies up to 2 MB; at most 10,000 PATCH operations; filters up to 4,096
  characters.
- Writes of one connector are serialised with the directory engine's lock
  (`hello:dirsync:<id>`), across replicas: a SCIM write waits for a manual run
  that is applying (up to 15 s, then `503` with `Retry-After`).
- Tokens are compared by hash; a missing, unknown or revoked token answers
  `401` with `WWW-Authenticate: Bearer`. Tokens and request bodies are never
  logged; audit rows name the connector (`directory:<name>`) for each user-level
  change.

| Metric                                                | Meaning                                                   |
| ----------------------------------------------------- | --------------------------------------------------------- |
| `hello_scim_requests_total{method,resource,status}`   | SCIM requests (resource: Users, Groups, discovery, other) |
| `hello_directory_changes_total{kind="scim",action}`   | changes SCIM made                                         |
| `hello_directory_sync_runs_total{kind="scim",result}` | pauses (`needs_confirmation`)                             |

## API

| Method and path                                      | Scope / role    | What                                  |
| ---------------------------------------------------- | --------------- | ------------------------------------- |
| `POST /api/v1/directory/connectors` (`kind: "scim"`) | admin / admin   | create the connector                  |
| `POST /api/v1/directory/connectors/{id}/scim-token`  | secrets / admin | issue a token (shown once)            |
| `GET /api/v1/directory/connectors/{id}/scim-tokens`  | admin / admin   | list the tokens (never the token)     |
| `DELETE /api/v1/directory/scim-tokens/{id}`          | admin / admin   | revoke a token                        |
| `GET /api/v1/directory/connectors/{id}/scim`         | admin / admin   | base URL, guard, pushed counts, pause |

Tables are in migration `00160` (`scim_resources`, `scim_group_members`,
`scim_tokens`, and `paused_at` and `guard_since` on `directory_connectors`).

## Lab checklist

Not yet run against real tenants (they need accounts):

- Entra ID: the provisioning test connection, an on-demand provision of one
  user, an initial cycle of a group, a group membership change with a role
  rule, a soft delete and a hard delete, a restore, and the Microsoft SCIM
  validator (`scimvalidator.microsoft.com`) against the base URL.
- Okta: Test Connector Configuration, Push New Users, Push Profile Updates
  (`PUT`), deactivate and reactivate, Push Groups with a membership change and
  a rename, and the Okta SCIM 2.0 test suite (Runscope/Spec tests).
- JumpCloud: a user and a group push.
- The guard: unassign more than 20 percent of a test group in Entra and check
  the pause, the `429`s in the provisioning log and the resume.
