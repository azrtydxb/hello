# Tools for phones, groups and phonebooks

| Tool                            | Scope | What it does                                             |
| ------------------------------- | ----- | -------------------------------------------------------- |
| `listPhones`                    | read  | Provisioned phones by MAC.                               |
| `getPhone`                      | read  | One phone.                                               |
| `createPhone`                   | write | Add a phone bound to an extension.                       |
| `importPhones`                  | write | Check or create many phones from JSON rows, all or none. |
| `updatePhone`                   | write | Change the sent fields of a phone.                       |
| `deletePhone`                   | write | Delete a phone; its device stays.                        |
| `listPhoneFetches`              | read  | When a phone fetched its configuration.                  |
| `previewPhoneFile`              | read  | Render a provisioning file with secrets masked.          |
| `resyncPhone`                   | write | Check-sync one phone.                                    |
| `resyncPhoneGroup`              | write | Check-sync a group.                                      |
| `resyncAllPhones`               | write | Check-sync every bound, enabled phone.                   |
| `listPhonePushes`               | read  | Recent pushes of one phone.                              |
| `listLatestPhonePushes`         | read  | The latest push of each phone.                           |
| `listOnboardingCandidates`      | write | Unadopted phones that asked for a configuration.         |
| `adoptOnboardingCandidate`      | write | Create a phone for a candidate.                          |
| `bulkAdoptOnboardingCandidates` | write | Adopt several candidates, all or none.                   |
| `dismissOnboardingCandidate`    | write | Remove a candidate, optionally ignoring its MAC.         |
| `listOnboardingIgnored`         | write | The ignore list.                                         |
| `deleteOnboardingIgnored`       | write | Take a MAC off the ignore list.                          |
| `listProvTemplates`             | read  | Built-in and stored templates.                           |
| `getProvTemplate`               | read  | One template.                                            |
| `copyProvTemplate`              | write | Copy a template into a new stored one.                   |
| `validateProvTemplate`          | write | Check a template without saving it.                      |
| `createProvTemplate`            | write | Create a stored template.                                |
| `updateProvTemplate`            | write | Change a stored template.                                |
| `deleteProvTemplate`            | write | Delete a stored template.                                |
| `getProvSettings`               | read  | Provisioning URLs, CA fingerprint and DHCP options.      |
| `listFirmware`                  | read  | Uploaded firmware files.                                 |
| `putFirmwarePins`               | write | Replace every firmware pin.                              |
| `deleteFirmware`                | write | Delete a firmware file.                                  |
| `listRedirectAccounts`          | admin | Vendor redirect accounts; credentials never returned.    |
| `putRedirectAccount`            | admin | Set a vendor's redirect account.                         |
| `deleteRedirectAccount`         | admin | Remove it.                                               |
| `checkRedirectAccount`          | admin | Test its credentials with the vendor.                    |
| `listPhoneGroups`               | read  | Phone groups.                                            |
| `getPhoneGroup`                 | read  | One group with its books.                                |
| `createPhoneGroup`              | admin | Create a group.                                          |
| `updatePhoneGroup`              | admin | Replace name, description and switches.                  |
| `deletePhoneGroup`              | admin | Delete a group.                                          |
| `setPhoneGroupBooks`            | admin | Set the ordered named books.                             |
| `setPhoneGroupMulticast`        | admin | Set the multicast paging channels.                       |
| `setPhoneGroupPTT`              | admin | Set push-to-talk and group paging.                       |
| `listPhonebooks`                | read  | Address books with entry counts.                         |
| `getPhonebook`                  | read  | One book.                                                |
| `createPhonebook`               | admin | Create a named book.                                     |
| `updatePhonebook`               | admin | Rename a book.                                           |
| `deletePhonebook`               | admin | Delete a named book.                                     |
| `lookupPhonebook`               | admin | Which entry names a caller.                              |
| `listPhonebookEntries`          | read  | Entries of a book.                                       |
| `createPhonebookEntry`          | write | Add an entry.                                            |
| `updatePhonebookEntry`          | write | Replace an entry.                                        |
| `deletePhonebookEntry`          | write | Delete an entry.                                         |
| `importPhonebook`               | write | Check or import JSON rows, all or nothing.               |
