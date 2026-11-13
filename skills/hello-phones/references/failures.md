# Failures

| Answer                                            | Meaning and fix                                                                                                                         |
| ------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------- |
| `importPhones` 400 with `rows`                    | At least one row failed; each row lists its errors (bad MAC, unknown vendor, unknown extension, duplicate MAC). Nothing was created.    |
| `importPhonebook` 400 with `errors`               | At least one row failed; nothing was written. The row number is the position in `rows` plus one.                                        |
| 409 on a phonebook                                | The extensions book is computed and read-only, or the book would pass 5000 entries.                                                     |
| 400 on `setPhoneGroupBooks`                       | A book does not exist, is not a named book or is listed twice; the message names its position.                                          |
| `listPhonePushes` says not_registered             | The phone is not registered, so there is nobody to notify; see `hello-troubleshoot`.                                                    |
| `listPhoneFetches` is empty                       | The phone never reached Hello: DHCP option 66 or 43, the vendor redirect, or the network. `getProvSettings` has the values.             |
| 403                                               | Phone groups, new phonebooks and redirect accounts need `admin`.                                                                        |
| 503 on provisioning tools                         | Provisioning is not enabled on this deployment.                                                                                         |
| 409 on `setSCEPCA`                                | A device CA exists; `replace` replaces it and phones enrol again.                                                                       |
| 409 on `deleteDot1XProfile`                       | A phone group names the profile; change the group first.                                                                                |
| A phone does not enrol                            | SCEP is off for its group, no CA exists, `getSCEP` says the server is off, or the model has no SCEP keys (only WP8x6 and GRP260x).      |
| Answer                                            | Meaning and fix                                                                                                                         |
| ------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------- |
| `importPhones` 400 with `rows`                    | At least one row failed; each row lists its errors (bad MAC, unknown vendor, unknown extension, duplicate MAC). Nothing was created.    |
| `importPhonebook` 400 with `errors`               | At least one row failed; nothing was written. The row number is the position in `rows` plus one.                                        |
| 409 on a phonebook                                | The extensions book is computed and read-only, or the book would pass 5000 entries.                                                     |
| 400 on `setPhoneGroupBooks`                       | A book does not exist, is not a named book or is listed twice; the message names its position.                                          |
| `listPhonePushes` says not_registered             | The phone is not registered, so there is nobody to notify; see `hello-troubleshoot`.                                                    |
| `listPhoneFetches` is empty                       | The phone never reached Hello: DHCP option 66 or 43, the vendor redirect, or the network. `getProvSettings` has the values.             |
| 403                                               | Phone groups, new phonebooks and redirect accounts need `admin`.                                                                        |
| 503 on provisioning tools                         | Provisioning is not enabled on this deployment.                                                                                         |
| 400 on `createWifiProfile` or `updateWifiProfile` | The field named is wrong: a pre-shared key of 8 to 63 characters, an identity and password for enterprise, an SSID of at most 32 bytes. |
| 409 on `deleteWifiProfile`                        | A phone group or phone still uses the profile; the message counts them. Remove it with `putPhoneGroupWifi` or `putPhoneWifi`.           |
| `pushing` is false                                | No profile of the phone's set is `verified`, so no network is written. Verify one on a test phone first.                                |
