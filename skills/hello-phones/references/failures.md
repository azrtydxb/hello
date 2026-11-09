# Failures

| Answer                                | Meaning and fix                                                                                                                      |
| ------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------ |
| `importPhones` 400 with `rows`        | At least one row failed; each row lists its errors (bad MAC, unknown vendor, unknown extension, duplicate MAC). Nothing was created. |
| `importPhonebook` 400 with `errors`   | At least one row failed; nothing was written. The row number is the position in `rows` plus one.                                     |
| 409 on a phonebook                    | The extensions book is computed and read-only, or the book would pass 5000 entries.                                                  |
| 400 on `setPhoneGroupBooks`           | A book does not exist, is not a named book or is listed twice; the message names its position.                                       |
| `listPhonePushes` says not_registered | The phone is not registered, so there is nobody to notify; see `hello-troubleshoot`.                                                 |
| `listPhoneFetches` is empty           | The phone never reached Hello: DHCP option 66 or 43, the vendor redirect, or the network. `getProvSettings` has the values.          |
| 403                                   | Phone groups, new phonebooks and redirect accounts need `admin`.                                                                     |
| 503 on provisioning tools             | Provisioning is not enabled on this deployment.                                                                                      |
