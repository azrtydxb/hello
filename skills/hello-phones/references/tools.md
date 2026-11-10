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
| `getPhoneLDAP`                  | read  | The phone's LDAP directory settings (the password is never shown). |
| `getPhoneGroupLDAP`             | read  | A phone group's LDAP directory settings.                           |
| `setPhoneGroupLDAP`             | admin | Set a phone group's LDAP directory settings.                       |
| `createPhoneGroup`              | admin | Create a group.                                          |
| `updatePhoneGroup`              | admin | Replace name, description and switches.                  |
| `deletePhoneGroup`              | admin | Delete a group.                                          |
| `setPhoneGroupBooks`            | admin | Set the ordered named books.                             |
| `setPhoneGroupMulticast`        | admin | Set the multicast paging channels.                       |
| `setPhoneGroupPTT`              | admin | Set push-to-talk and group paging.                       |
| `listPhoneGroupSettings`        | read  | A group's setting overrides, secrets masked.             |
| `putPhoneGroupSettings`         | admin | Replace a group's setting overrides.                     |
| `listPhoneSettings`             | read  | A phone's setting overrides, secrets masked.             |
| `putPhoneSettings`              | admin | Replace a phone's setting overrides.                     |
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
| `listRingTags`                  | read  | Built-in and custom ring tags.                           |
| `getRingTag`                    | read  | One ring tag.                                            |
| `createRingTag`                 | write | Create a custom ring tag.                                |
| `putRingTag`                    | write | Change a ring tag.                                       |
| `deleteRingTag`                 | write | Delete a custom ring tag.                                |
| `listRingRules`                 | read  | The ring tag rules in order.                             |
| `putRingRules`                  | write | Replace the ring tag rules.                              |
| `evaluateRingRules`             | read  | Which ring tag a call would get.                         |
| `getRingMap`                    | read  | The ring map: ringer lists, defaults, choices, overrides.          |
| `putRingMap`                    | write | Replace the global ring map choices.                               |
| `putRingMapGroup`               | write | Replace one phone group's ring map overrides.                      |
| `listRingtones`                 | read  | The tone library and each tone's use.                              |
| `createRingtone`                | write | Add a WAV tone to the library.                                     |
| `deleteRingtone`                | write | Delete a tone no ring map choice uses.                             |
| `listFirmwareMinimums`          | read  | Minimum and recommended firmware per vendor.                       |
| `putFirmwareMinimums`           | write | Replace every firmware minimum rule.                               |
