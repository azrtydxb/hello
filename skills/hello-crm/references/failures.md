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
| `lastError` `INSUFFICIENT_ACCESS` (Salesforce)      | The integration user may not create or edit that Task or read that record; fix its permission set.      |
| `lastError` `REQUEST_LIMIT_EXCEEDED` (Salesforce)   | The org's API limit; the job retries on the usual schedule.                                             |
| `lastError` privilege (Dynamics 365)                | The application user's security role lacks a privilege (read contacts, create or write phone calls).    |
