# Failures

| Answer                                              | Meaning and fix                                                                                         |
| --------------------------------------------------- | ------------------------------------------------------------------------------------------------------- |
| 403 on `listCRMConnectors`                          | CRM administration needs the `admin` role and scope; `listCRMLogs` needs `operator` and `write`.        |
| 404                                                 | No connector with that id; list them first.                                                             |
| 400 on `listCRMLogs`                                | `status` is not one of waiting, pending, logged, update, dead, skipped, or `limit` is outside 1 to 200. |
| 503                                                 | CRM connectors are not configured on this deployment.                                                   |
| `lastError` "the connection needs re-authorisation" | The connection is `needs_reauth`; sign in again in the console. Jobs resume by themselves.              |
| `lastError` 403 from the CRM                        | The CRM app lacks a scope (HubSpot: contacts read and write, owners read). Add it and sign in again.    |
| `lastError` 429                                     | The CRM rate-limited Hello; the job retries after the CRM's Retry-After (at most an hour).              |
| `lastError` "recording not sent: ..."               | Pipedrive only: the recording was over the upload limit or deleted. The call is logged without it.      |
| skipped job "no Odoo contact or call owner ..."     | Odoo: the caller is unknown and the call's owner has no partner. Map the user to an Odoo user.          |
| `lastError` "the Pipedrive account address ..."     | The stored Pipedrive account address is not a pipedrive.com address; connect again.                     |
| `lastError` from Bitrix24 "insufficient_scope"      | The inbound webhook lacks CRM, Telephony or Users permission. Fix it in Bitrix24, then Connect again.   |
